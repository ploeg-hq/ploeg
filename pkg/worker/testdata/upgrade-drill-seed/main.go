package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/httpapi"
	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/shiftengine"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
	"github.com/ploeg-hq/ploeg/pkg/worker"
)

type event struct {
	Event      string `json:"event"`
	Token      string `json:"token,omitempty"`
	ExternalID string `json:"externalId,omitempty"`
	WorkItemID int64  `json:"workItemId,omitempty"`
	Err        string `json:"err,omitempty"`
	Failures   int    `json:"failures,omitempty"`
}

var subcommands = map[string]func(context.Context, []string) error{
	"capabilities": capabilities,
	"migrate":      migrate,
	"serve":        serve,
	"ingest":       ingest,
	"run":          run,
	"settle":       settle,
	"hold":         hold,
	"bulk":         bulk,
}

func main() {
	if len(os.Args) < 2 || subcommands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, "usage: seed capabilities|migrate|serve|ingest|run|settle|hold|bulk [flags]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := subcommands[os.Args[1]](ctx, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func emit(e event) {
	line, _ := json.Marshal(e)
	fmt.Println(string(line))
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func callAdapting(fn reflect.Value, available ...any) []reflect.Value {
	kind := fn.Type()
	args := make([]reflect.Value, kind.NumIn())
	used := make([]bool, len(available))
	for i := range args {
		param := kind.In(i)
		args[i] = reflect.Zero(param)
		for j, value := range available {
			if !used[j] && value != nil && reflect.TypeOf(value).AssignableTo(param) {
				args[i] = reflect.ValueOf(value)
				used[j] = true
				break
			}
		}
	}
	return fn.Call(args)
}

func asError(v reflect.Value) error {
	if v.IsNil() {
		return nil
	}
	return v.Interface().(error)
}

func openStore(ctx context.Context, dsn string) (*store.Store, error) {
	out := callAdapting(reflect.ValueOf(store.New), ctx, dsn)
	if err := asError(out[1]); err != nil {
		return nil, err
	}
	return out[0].Interface().(*store.Store), nil
}

func newClient(api, workerVersion string) *worker.APIClient {
	client := &worker.APIClient{Base: api, HC: &http.Client{Timeout: 10 * time.Second}}
	if field := reflect.ValueOf(client).Elem().FieldByName("WorkerVersion"); field.IsValid() && field.Kind() == reflect.String {
		field.SetString(workerVersion)
	}
	return client
}

func renew(ctx context.Context, client *worker.APIClient, token string) (bool, error) {
	out := callAdapting(reflect.ValueOf(client).MethodByName("Renew"), ctx, token)
	if err := asError(out[1]); err != nil {
		return false, err
	}
	if out[0].Kind() == reflect.Bool {
		return out[0].Bool(), nil
	}
	return out[0].FieldByName("Gone").Bool(), nil
}

func capabilities(context.Context, []string) error {
	_, sendsVersion := reflect.TypeOf(worker.APIClient{}).FieldByName("WorkerVersion")
	return json.NewEncoder(os.Stdout).Encode(map[string]bool{"workerVersion": sendsVersion})
}

func migrate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "database URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	st, err := openStore(ctx, *dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Migrate(ctx)
}

func apiServer(st *store.Store, plans plan.Plans, ttl time.Duration, log *slog.Logger) *httpapi.Server {
	engine := &shiftengine.Engine{Store: st, Plans: plans, Log: log}
	return &httpapi.Server{Store: st, LeaseTTL: ttl, Log: log, RoleCaps: plans, Engine: engine,
		WorkerSecurity: &httpapi.WorkerSecurity{AllowLegacy: true}}
}

func bulk(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("bulk", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "database URL")
	plans := flags.String("plans", "", "PLOEG_PLANS")
	team := flags.String("team", "", "shift-less team the items are assigned to")
	count := flags.Int("count", 0, "items to take through claim, outcome and settlement")
	spend := flags.Float64("spend", 0, "USD settled per Run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	st, err := openStore(ctx, *dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	parsed, err := plan.Parse(*plans)
	if err != nil {
		return err
	}
	server := httptest.NewServer(apiServer(st, parsed, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client := newClient(server.URL, "")
	outcomes := []work.Outcome{work.OutcomeNoChangeNeeded, work.OutcomeStuck, work.OutcomePROpened}
	for i := range *count {
		external := "bulk-" + strconv.Itoa(i)
		if _, _, err := st.IngestAssigned(ctx, work.WorkItem{Provider: "vikunja", ExternalID: external, Team: *team, Title: "bulk"}); err != nil {
			return err
		}
		claimed, err := client.Claim(*team, "")
		if err != nil || claimed == nil {
			return fmt.Errorf("claim %s: %v", external, err)
		}
		alias := "ploeg-drill-" + external
		if err := issueAccountIn(ctx, st, claimed.RunToken, alias); err != nil {
			return err
		}
		if err := client.Checkpoint(claimed.RunToken, work.Checkpoint{Phase: "branch_created", Branch: "agent/vik-" + claimed.WorkItem.ExternalID}); err != nil {
			return fmt.Errorf("checkpoint %s: %w", external, err)
		}
		report := harness.OutcomeReport{Outcome: outcomes[i%len(outcomes)], Summary: "bulk"}
		switch report.Outcome {
		case work.OutcomePROpened:
			report.Links = []string{"https://forge.invalid/pr/" + strconv.Itoa(i)}
		case work.OutcomeStuck:
			report.StuckReason = "bulk ticket needs a person"
		}
		if err := client.Outcome(claimed.RunToken, report); err != nil {
			return fmt.Errorf("outcome %s: %w", external, err)
		}
		if err := st.RecordLLMBlocked(ctx, claimed.RunToken, spend); err != nil {
			return err
		}
		if _, err := st.SettleLLMAccount(ctx, claimed.RunToken, store.LLMSettlement{Spend: *spend, Evidence: "upgrade drill spend log", CostKnown: true}); err != nil {
			return err
		}
	}
	emit(event{Event: "bulk", Failures: *count})
	return nil
}

func serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "database URL")
	plans := flags.String("plans", "", "PLOEG_PLANS")
	ttl := flags.Duration("ttl", 5*time.Second, "lease TTL")
	addrFile := flags.String("addr-file", "", "file the listen address is written to")
	if err := flags.Parse(args); err != nil {
		return err
	}
	st, err := openStore(ctx, *dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	parsed, err := plan.Parse(*plans)
	if err != nil {
		return err
	}
	server := apiServer(st, parsed, *ttl, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	pending := *addrFile + ".pending"
	if err := os.WriteFile(pending, []byte("http://"+listener.Addr().String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(pending, filepath.Clean(*addrFile)); err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	if err := httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func ingest(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "database URL")
	plans := flags.String("plans", "", "PLOEG_PLANS")
	team := flags.String("team", "", "team")
	external := flags.String("external", "", "tracker id")
	planned := flags.Bool("planned", false, "open the item's Shift")
	if err := flags.Parse(args); err != nil {
		return err
	}
	st, err := openStore(ctx, *dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	item := work.WorkItem{Provider: "vikunja", ExternalID: *external, Team: *team, Title: "t"}
	id, _, err := st.IngestAssigned(ctx, item)
	if err != nil {
		return err
	}
	if *planned {
		parsed, err := plan.Parse(*plans)
		if err != nil {
			return err
		}
		engine := &shiftengine.Engine{Store: st, Plans: parsed, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		if err := engine.EnsureShift(ctx, id, item); err != nil {
			return err
		}
	}
	emit(event{Event: "ingested", ExternalID: *external, WorkItemID: id})
	return nil
}

func issueAccount(ctx context.Context, dsn, token, alias string) error {
	st, err := openStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	return issueAccountIn(ctx, st, token, alias)
}

func issueAccountIn(ctx context.Context, st *store.Store, token, alias string) error {
	if err := st.ReserveLLMAccount(ctx, store.LLMAccount{RunToken: token, Alias: alias, Authorized: 2, Models: []string{"drill-model"}, TTLSeconds: 3600}); err != nil {
		return fmt.Errorf("reserve %s: %w", alias, err)
	}
	if _, err := st.BeginLLMMint(ctx, token); err != nil {
		return fmt.Errorf("mint %s: %w", alias, err)
	}
	return st.RecordLLMIssued(ctx, token, "key-"+alias)
}

func settle(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("settle", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "database URL")
	token := flags.String("token", "", "run token")
	spend := flags.Float64("spend", 0, "settled USD")
	if err := flags.Parse(args); err != nil {
		return err
	}
	st, err := openStore(ctx, *dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.RecordLLMBlocked(ctx, *token, spend); err != nil {
		return err
	}
	settled, err := st.SettleLLMAccount(ctx, *token, store.LLMSettlement{Spend: *spend, Evidence: "upgrade drill spend log", CostKnown: true})
	if err != nil {
		return err
	}
	if !settled {
		return fmt.Errorf("account of %s did not settle", *token)
	}
	emit(event{Event: "settled", Token: *token})
	return nil
}

type runFlags struct {
	api, dsn, team, role, workerVersion, account, branch string
}

func (r *runFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&r.api, "api", "", "run API base URL")
	flags.StringVar(&r.dsn, "dsn", "", "database URL")
	flags.StringVar(&r.team, "team", "", "team")
	flags.StringVar(&r.role, "role", "", "role")
	flags.StringVar(&r.workerVersion, "worker-version", "", "the release this worker reports")
	flags.StringVar(&r.account, "account", "", "LLM account alias issued after the claim")
	flags.StringVar(&r.branch, "branch", "", "checkpoint branch written after the claim")
}

func (r *runFlags) start(ctx context.Context, client *worker.APIClient) (*worker.ClaimResponse, error) {
	claimed, err := client.Claim(r.team, r.role)
	if err != nil {
		emit(event{Event: "claim_refused", Err: err.Error()})
		return nil, nil
	}
	if claimed == nil {
		emit(event{Event: "claim_empty"})
		return nil, nil
	}
	if r.account != "" {
		if err := issueAccount(ctx, r.dsn, claimed.RunToken, r.account); err != nil {
			return nil, err
		}
	}
	if r.branch != "" {
		r.branch = strings.ReplaceAll(r.branch, "{externalId}", claimed.WorkItem.ExternalID)
		if err := client.Checkpoint(claimed.RunToken, work.Checkpoint{Phase: "branch_created", Branch: r.branch}); err != nil {
			return nil, fmt.Errorf("checkpoint: %w", err)
		}
	}
	emit(event{Event: "claimed", Token: claimed.RunToken, ExternalID: claimed.WorkItem.ExternalID})
	return claimed, nil
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	var r runFlags
	r.register(flags)
	outcome := flags.String("outcome", "", "outcome reported after the claim; empty keeps the Run running")
	stuckReason := flags.String("stuck-reason", "", "reason a stuck outcome names")
	if err := flags.Parse(args); err != nil {
		return err
	}
	client := newClient(r.api, r.workerVersion)
	claimed, err := r.start(ctx, client)
	if err != nil || claimed == nil || *outcome == "" {
		return err
	}
	return client.Outcome(claimed.RunToken, harness.OutcomeReport{Outcome: work.Outcome(*outcome), Summary: "reported by the older release", StuckReason: *stuckReason})
}

func hold(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("hold", flag.ContinueOnError)
	var r runFlags
	r.register(flags)
	every := flags.Duration("renew-every", time.Second, "renewal interval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	client := newClient(r.api, r.workerVersion)
	claimed, err := r.start(ctx, client)
	if err != nil || claimed == nil {
		return err
	}
	token := claimed.RunToken
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*every):
		}
		gone, err := renew(ctx, client, token)
		switch {
		case err != nil:
			failures++
			if failures == 1 {
				emit(event{Event: "renew_failed", Token: token, Err: err.Error()})
			}
			continue
		case gone:
			emit(event{Event: "gone", Token: token, Failures: failures})
		default:
			if failures > 0 {
				emit(event{Event: "reconnected", Token: token, Failures: failures})
			}
			failures = 0
			emit(event{Event: "renewed", Token: token})
			continue
		}
		break
	}
	emit(event{Event: "checkpoint_after_gone", Token: token,
		Err: errText(client.Checkpoint(token, work.Checkpoint{Phase: "changes_made", Branch: r.branch}))})
	emit(event{Event: "outcome_after_gone", Token: token,
		Err: errText(client.Outcome(token, harness.OutcomeReport{Outcome: work.OutcomePROpened, Summary: "a report from before the upgrade", Links: []string{"https://forge.invalid/pr/1"}}))})
	if !bufio.NewScanner(os.Stdin).Scan() {
		return nil
	}
	r.account, r.branch = "", ""
	again, err := r.start(ctx, client)
	if err != nil || again == nil {
		return err
	}
	emit(event{Event: "reclaimed_reported", Token: again.RunToken,
		Err: errText(client.Outcome(again.RunToken, harness.OutcomeReport{Outcome: work.OutcomeNoChangeNeeded, Summary: "the older worker's new attempt"}))})
	return nil
}
