// ploegd is Ploeg's single daemon: webhook ingest, provider SPI host,
// lease manager, outcome ingestion. Prototype: Vikunja ingest + run API +
// expiry sweeper over Postgres.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/litellm"
	"github.com/ploeg-hq/ploeg/pkg/llmbroker"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/provider/forgejo"
	"github.com/ploeg-hq/ploeg/pkg/provider/gitlab"
	"github.com/ploeg-hq/ploeg/pkg/provider/vikunja"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

var version = "0.0.0-dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("ploegd exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	dbURL := os.Getenv("PLOEG_DATABASE_URL")
	if dbURL == "" {
		return errors.New("PLOEG_DATABASE_URL is required")
	}
	listen := envOr("PLOEG_LISTEN", ":8080")
	leaseTTL := durationOr("PLOEG_LEASE_TTL", 60*time.Second)
	sweepEvery := durationOr("PLOEG_SWEEP_INTERVAL", 15*time.Second)
	settleAfter := durationOr("PLOEG_LLM_SETTLE_AFTER", 15*time.Minute)
	correctionWindow := durationOr("PLOEG_LLM_CORRECTION_WINDOW", 24*time.Hour)
	if correctionWindow < 0 {
		return errors.New("PLOEG_LLM_CORRECTION_WINDOW must not be negative")
	}
	reviewEvery := durationOr("PLOEG_REVIEW_RECONCILE_INTERVAL", 10*time.Minute)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	// The database may still be bootstrapping when ploegd starts (fresh CNPG
	// cluster, compose cold start); retry instead of crash-looping.
	for deadline := time.Now().Add(2 * time.Minute); ; {
		if err = st.Ping(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("database unreachable after retries: %w", err)
		}
		log.Warn("database not ready; retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	// File-backed configuration (PLOEG_CONFIG). Replaces three env-var DSLs
	// with one reviewable YAML file; absent = the env vars still apply, so
	// this rolls out without a flag day.
	cfg, err := config.Load(os.Getenv("PLOEG_CONFIG"))
	if err != nil {
		return fmt.Errorf("PLOEG_CONFIG: %w", err)
	}

	// Tracker write-backs are opt-in by credential: without a URL and token
	// the provider keeps its logging no-op, so a deployment that has not been
	// given one still finishes runs — it just does not update the board.
	vik := &vikunja.Provider{
		Secret:      os.Getenv("PLOEG_VIKUNJA_SECRET"),
		DefaultTeam: envOr("PLOEG_DEFAULT_TEAM", "default"),
		TeamMap:     mergeTeamMap(cfg.AssigneeTeams(), parseTeamMap(os.Getenv("PLOEG_TEAM_MAP"))),
		BaseURL:     trimSlash(os.Getenv("PLOEG_VIKUNJA_URL")),
		Token:       os.Getenv("PLOEG_VIKUNJA_TOKEN"),
		Log:         log,
	}
	if vik.BaseURL != "" && vik.Token != "" {
		log.Info("vikunja write-backs enabled", "url", vik.BaseURL)
	} else {
		log.Info("vikunja write-backs disabled (PLOEG_VIKUNJA_URL/PLOEG_VIKUNJA_TOKEN unset)")
	}
	if vik.Secret == "" {
		log.Warn("PLOEG_VIKUNJA_SECRET is unset; every Vikunja webhook is rejected")
	}

	// The tracker registry. Keyed by dialect name, which is what the webhook
	// route names (/webhooks/tracker/{provider}) and what a mirrored WorkItem
	// carries in Provider. More than one may be live at once: a board
	// migration runs both for a while, and nothing in the core cares which one
	// an item came from.
	trackers := map[string]provider.TrackerProvider{vik.Name(): vik}

	// ClickUp is registered only when a secret or a token says the deployment
	// actually uses it. Without a secret its route rejects every delivery.
	if cuSecret, cuToken := os.Getenv("PLOEG_CLICKUP_SECRET"), os.Getenv("PLOEG_CLICKUP_TOKEN"); cuSecret != "" || cuToken != "" {
		cu := &clickup.Provider{
			Secret:      cuSecret,
			DefaultTeam: envOr("PLOEG_DEFAULT_TEAM", "default"),
			TeamMap:     mergeTeamMap(cfg.AssigneeTeams(), parseTeamMap(os.Getenv("PLOEG_TEAM_MAP"))),
			BaseURL:     trimSlash(os.Getenv("PLOEG_CLICKUP_URL")),
			Token:       cuToken,
			DoneStatus:  os.Getenv("PLOEG_CLICKUP_DONE_STATUS"),
			Log:         log,
		}
		trackers[cu.Name()] = cu
		if cuSecret == "" {
			log.Warn("PLOEG_CLICKUP_SECRET is unset; every ClickUp webhook is rejected")
		}
		log.Info("clickup tracker configured",
			"write_backs", cuToken != "",
			"done_status", cu.DoneStatus != "")
	}

	// The forge seam. Ploeg comments as the same bot that opens the pull
	// requests; commenting is not pushing, so it needs no second credential.
	//
	// Registered under the forge ID a Work Target carries, not under the
	// provider's dialect name. Those are different things (ADR-0016): the id
	// identifies an INSTANCE, the name identifies the API dialect, and one
	// deployment could have two Forgejo instances with different ids. The
	// engine looks up `Forges[target.Forge]`, so keying this by Name() would
	// silently match nothing and skip every publication. The route is
	// registered under both, since a webhook path names the dialect.
	forgeID := forgeIDFromEnv()
	forges := map[string]provider.ForgeProvider{}
	if forgeURL := trimSlash(os.Getenv("PLOEG_FORGEJO_URL")); forgeURL != "" {
		fj := &forgejo.Provider{
			BaseURL: forgeURL,
			Token:   os.Getenv("PLOEG_FORGEJO_TOKEN"),
			Secret:  os.Getenv("PLOEG_FORGEJO_SECRET"),
			Log:     log,
		}
		forges[fj.Name()] = fj
		log.Info("forge provider configured", "dialect", fj.Name(), "url", forgeURL)
		if fj.Secret == "" {
			log.Warn("PLOEG_FORGEJO_SECRET is unset; every Forgejo webhook is rejected")
		}
	}
	if glURL := trimSlash(os.Getenv("PLOEG_GITLAB_URL")); glURL != "" {
		gl := &gitlab.Provider{
			BaseURL: glURL,
			Token:   os.Getenv("PLOEG_GITLAB_TOKEN"),
			Secret:  os.Getenv("PLOEG_GITLAB_SECRET"),
			Log:     log,
		}
		forges[gl.Name()] = gl
		log.Info("forge provider configured", "dialect", gl.Name(), "url", glURL)
		if gl.Secret == "" {
			log.Warn("PLOEG_GITLAB_SECRET is unset; every GitLab webhook is rejected")
		}
	}
	// Bind the INSTANCE id (ADR-0016) to a dialect. PLOEG_TARGET_FORGE names
	// the instance a Work Target carries; when it happens to equal a dialect
	// name that binding is already exact. Otherwise a single configured forge
	// is unambiguous and takes the id. With two forges and an id matching
	// neither, nothing is bound: guessing which one a Target meant would
	// publish findings to the wrong forge, which is worse than not publishing.
	switch {
	case forges[forgeID] != nil:
		// PLOEG_TARGET_FORGE already names a configured dialect.
	case len(forges) == 1:
		for _, only := range forges {
			forges[forgeID] = only
		}
		log.Info("forge instance bound to the only configured dialect", "forge_id", forgeID)
	case len(forges) > 1:
		log.Warn("PLOEG_TARGET_FORGE matches no configured dialect; targets naming it will not publish",
			"forge_id", forgeID)
	}
	if len(forges) == 0 {
		log.Info("no forge provider configured (PLOEG_FORGEJO_URL/PLOEG_GITLAB_URL unset); findings will not reach a pull request")
	}

	forgeCreds, forgeSweeper := forgeCredentials(log)

	// Gateway credential sweeper (llmbroker.Sweeper) for per-run key
	// lifecycle: nil = no gateway configured, revocation disabled.
	var sweeper llmbroker.Sweeper
	if url := os.Getenv("LITELLM_ADMIN_URL"); url != "" {
		key := os.Getenv("LITELLM_MASTER_KEY")
		if key != "" {
			sweeper = llmbroker.NewLiteLLM(litellm.NewClient(url, key))
			log.Info("litellm sweeper configured", "url", url)
		} else {
			log.Warn("LITELLM_ADMIN_URL set but LITELLM_MASTER_KEY is empty — key revocation disabled")
		}
	} else {
		log.Info("LITELLM_ADMIN_URL not set — key revocation disabled")
	}

	// Work Target resolution: the repository belongs to the work item, not to
	// the team (R11). Entries are rendered from the git org.yaml roster
	// manifest. Empty = nothing resolves and workers keep using their
	// env-configured repo, which is exactly the pre-decoupling behavior.
	// Routing rules come from the config file when it names projects — which
	// is where the project IDs get resolved from names, so nothing in cluster
	// config is a bare number. The env var remains as the fallback.
	targets, readiness, err := routing(ctx, log, cfg, vik, forges, forgeID)
	if err != nil {
		return err
	}

	// Team plans (run-multi-agent-shifts): config for the shift engine, parsed
	// and validated at boot so a plan that could open a malformed Round never
	// starts. Empty = every team is plan-less and dispatch is unchanged. The
	// engine that consumes these lands in a follow-up change; parsing first
	// means a bad values edit fails loudly at rollout, not at first dispatch.
	plans := cfg.Plans()
	if len(plans) == 0 {
		if plans, err = plan.Parse(os.Getenv("PLOEG_TEAM_PLANS")); err != nil {
			return fmt.Errorf("PLOEG_TEAM_PLANS: %w", err)
		}
	}
	log.Info("team plans loaded", "teams", len(plans))

	envCaps, err := config.ParseRunCaps(os.Getenv("PLOEG_TEAM_MAX_RUNNING"))
	if err != nil {
		return fmt.Errorf("PLOEG_TEAM_MAX_RUNNING: %w", err)
	}
	runCaps := cfg.RunCaps(envCaps)
	log.Info("team concurrency caps loaded", "caps", runCaps)

	// Uniform dispatch: a team with no plan still gets a Shift — one Round,
	// one writer — so every item has exactly one answer to "what is happening
	// with this". Default on; PLOEG_SHIFTS_UNIFORM=false is the kill switch
	// back to the pre-Shift path, and needs only a ploegd restart.
	uniform := envOr("PLOEG_SHIFTS_UNIFORM", "true") != "false"

	// The usage and evidence report on every agent pull request. Default on;
	// PLOEG_USAGE_REPORT=false silences it without a rollback (the report is
	// additive transport and changes no lifecycle state). The two base URLs
	// are where its links point; unset omits the links section.
	usageReport := usageReportFromEnv()

	// The engine is nil only when it would have nothing to do: no plans AND
	// no uniform dispatch. Then dispatch is exactly the pre-Shift path.
	var engine *shiftengine.Engine
	if len(plans) > 0 || uniform {
		engine = &shiftengine.Engine{
			Store: st, Plans: plans, Log: log,
			Forges:       forges,
			DefaultForge: forgeID,
			Trackers:     trackers,
			Uniform:      uniform,
			UsageReport:  usageReport,
			ReportLinks: shiftengine.ReportLinks{
				WorkItem:       os.Getenv("PLOEG_REPORT_WORK_ITEM_URL"),
				TeamDashboard:  os.Getenv("PLOEG_REPORT_TEAM_DASHBOARD_URL"),
				RunDashboard:   os.Getenv("PLOEG_REPORT_RUN_DASHBOARD_URL"),
				SpendDashboard: os.Getenv("PLOEG_REPORT_SPEND_DASHBOARD_URL"),
			},
		}
		log.Info("shift engine enabled", "planned_teams", len(plans), "uniform", uniform, "usage_report", usageReport)
	} else {
		log.Info("shift engine disabled (no plans, PLOEG_SHIFTS_UNIFORM=false)")
	}

	workerSecurity, err := httpapi.NewWorkerSecurity(os.Getenv("PLOEG_WORKER_BOOTSTRAPS"), os.Getenv("PLOEG_WORKER_SIGNING_KEY"), os.Getenv("PLOEG_WORKER_AUTH_MODE") == "legacy")
	if err != nil {
		return fmt.Errorf("worker security: %w", err)
	}
	var llmControl *httpapi.LLMControl
	if !workerSecurity.AllowLegacy {
		managed, ok := sweeper.(httpapi.ManagedLLMBroker)
		if !ok {
			return errors.New("managed worker mode requires a LiteLLM management broker")
		}
		llmControl, err = httpapi.NewLLMControl(st, managed, os.Getenv("PLOEG_WORKER_LLM_POLICIES"))
		if err != nil {
			return fmt.Errorf("worker inference policy: %w", err)
		}
		llmControl.CorrectionWindow = correctionWindow
	}
	operator, err := operatorConfig(cfg, plans)
	if err != nil {
		return fmt.Errorf("operator configuration: %w", err)
	}
	deploys, err := deployAuth()
	if err != nil {
		return err
	}
	log.Info("deploy endpoint", "enabled", deploys != nil)
	createdWork, err := cfg.CreatedWorkPolicies()
	if err != nil {
		return fmt.Errorf("created work: %w", err)
	}
	gates, err := cfg.GateBoards(ctx, vik, log)
	if err != nil {
		return fmt.Errorf("gates: %w", err)
	}
	statusBoards, err := cfg.FlowBoards(ctx, vik, log)
	if err != nil {
		return fmt.Errorf("status kinds: %w", err)
	}
	calendars, err := cfg.WorkingCalendars()
	if err != nil {
		return fmt.Errorf("working hours: %w", err)
	}
	for team, c := range calendars {
		log.Info("working calendar loaded", "team", team, "calendar", c.String())
	}
	contextMax, err := byteCountFromEnv("PLOEG_CONTEXT_MAX_BYTES", httpapi.DefaultContextMaxBytes)
	if err != nil {
		return err
	}
	contextMaxTotal, err := byteCountFromEnv("PLOEG_CONTEXT_MAX_TOTAL_BYTES", httpapi.DefaultContextMaxTotalBytes)
	if err != nil {
		return err
	}
	srv := &httpapi.Server{
		OperatorConfig: operator,
		WorkerSecurity: workerSecurity,
		LLMControl:     llmControl,
		Store:          st,
		Trackers:       trackers,
		Targets:        targets,
		Readiness:      readiness,
		ScopeTeams:     cfg.ScopeTeams(),
		LeaseTTL:       leaseTTL,
		Log:            log,
		RoleCaps:       plans,
		TeamCaps:       runCaps,
		Forges:         forges,
		ForgeCreds:     forgeCreds,
		CreatedWork:    createdWork,

		MetricsCacheTTL:  durationOr("PLOEG_METRICS_CACHE_TTL", httpapi.DefaultMetricsCacheTTL),
		FollowUps:        cfg.ForgeFollowUps(),
		ForgeBots:        forgeBots(),
		Deploys:          deploys,
		Gates:            gates,
		StatusBoards:     statusBoards,
		WorkingCalendars: calendars,
		CardRules:        cardRules(cfg.TeamCardRules()),
		CardsDisabled:    !cfg.CardsEnabled(),

		ContextMaxBytes:      contextMax,
		ContextMaxTotalBytes: contextMaxTotal,
	}
	log.Info("forge follow-ups loaded", "teams", len(srv.FollowUps))
	log.Info("run card work", "enabled", !srv.CardsDisabled)
	if engine != nil {
		srv.Engine = engine
	}
	var reviews *shiftengine.ReviewWatch
	if len(forges) > 0 {
		reviews = &shiftengine.ReviewWatch{
			Store:           st,
			Forges:          forges,
			DefaultForge:    forgeID,
			Trackers:        trackers,
			MarkTrackerDone: envOr("PLOEG_TRACKER_DONE_ON_MERGE", "false") == "true",
			Bots:            forgeBots(),
			Log:             log,
		}
		srv.Reviews = reviews
		log.Info("pull request merge detection enabled", "reconcile_every", reviewEvery,
			"tracker_done_on_merge", reviews.MarkTrackerDone)
	}
	webhookCheck, err := newVikunjaWebhookCheck(vik, cfg.Trackers.Vikunja.Projects, log)
	if err != nil {
		return err
	}
	if webhookCheck != nil {
		srv.TrackerWebhooks = webhookCheck.coverage
		go webhookCheck.loop(ctx)
	}

	httpSrv := newHTTPServer(listen, srv.Handler(), defaultHTTPTimeouts)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	orphanSweep(ctx, log, st, sweeper)
	forgeOrphanSweep(ctx, log, st, forgeSweeper)
	if err := srv.ReconcileOperatorExecutions(ctx); err != nil {
		log.Error("operator execution reconciliation failed at startup")
	}
	managedBlockSweep(ctx, log, srv, 0, newAccountRetries(sweepEvery))

	go sweepLoop(ctx, log, st, sweeper, forgeSweeper, engine, srv, sweepEvery, settleAfter)
	if reviews != nil {
		go reviewLoop(ctx, reviews, reviewEvery)
	}
	go deployCheckLoop(ctx, srv, deployCheckEvery)

	log.Info("ploegd listening", "version", version, "addr", listen, "lease_ttl", leaseTTL)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("ploegd stopped")
	return nil
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }

// mergeTeamMap prefers the config file's roster and keeps env entries it does
// not define, so a deployment can move one team at a time.
func mergeTeamMap(fromFile, fromEnv map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range fromEnv {
		out[k] = v
	}
	for k, v := range fromFile {
		out[k] = v
	}
	return out
}

// parseTeamMap parses "user1=teamA,user2=teamB" (usernames lowercased).
func parseTeamMap(s string) map[string]string {
	m := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" && v != "" {
			m[strings.ToLower(k)] = v
		}
	}
	return m
}

func cardRules(teams map[string]config.TeamCards) map[string]httpapi.CardRules {
	out := make(map[string]httpapi.CardRules, len(teams))
	for team, cards := range teams {
		out[team] = httpapi.CardRules{Referees: cards.Referees, HotfixLabels: cards.HotfixLabels, PRComment: cards.PRComment}
	}
	return out
}

func forgeBots() []string {
	bots := []string{envOr("PLOEG_FORGEJO_BOT", "agent-builder")}
	if gl := os.Getenv("PLOEG_GITLAB_BOT"); gl != "" {
		bots = append(bots, gl)
	}
	return bots
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// usageReportFromEnv reads PLOEG_USAGE_REPORT. Default on: the outcome is
// "every agent pull request", and the value exists so a noisy deployment can
// silence the report without a rollback.
func usageReportFromEnv() bool {
	return envOr("PLOEG_USAGE_REPORT", "true") != "false"
}

// byteCountFromEnv reads a positive byte count; unset is def.
func byteCountFromEnv(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: want a positive byte count, got %q", key, v)
	}
	return n, nil
}

func durationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
