package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// DeployAuth holds, as a hash, the bearer credential POST /api/v1/deploys
// accepts (ADR-0047).
type DeployAuth struct {
	hash [sha256.Size]byte
}

// NewDeployAuth returns the authentication for token. An empty token returns
// nil, which disables the endpoint. A token shorter than 32 bytes, longer
// than 4096 bytes or containing whitespace is an error.
func NewDeployAuth(token string) (*DeployAuth, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n,") {
		return nil, errors.New("the deploy token must be 32 to 4096 bytes without whitespace or commas")
	}
	return &DeployAuth{hash: sha256.Sum256([]byte(token))}, nil
}

func (a *DeployAuth) allows(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, credential, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || credential == "" || len(credential) > 4096 {
		return false
	}
	got := sha256.Sum256([]byte(credential))
	return subtle.ConstantTimeCompare(got[:], a.hash[:]) == 1
}

const (
	deployBodyLimit      = 16 << 10
	deployCandidateLimit = 50
	deployMergeSkew      = 10 * time.Minute
	deployFutureSkew     = 5 * time.Minute
	deployCheckTimeout   = 20 * time.Second

	deploySweepBatch       = 10
	deployCheckLease       = 5 * time.Minute
	deployCheckBackoff     = time.Minute
	deployCheckMaxBackoff  = time.Hour
	deployCheckMaxAttempts = 12
)

var (
	deployForge = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	deployOwner = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
	deployName  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,255}$`)
	deploySHA   = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

type deployRequest struct {
	Environment string `json:"environment"`
	Repo        struct {
		Forge string `json:"forge"`
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repo"`
	SHA        string  `json:"sha"`
	DeployedAt *string `json:"deployedAt"`
	URL        string  `json:"url"`
	Source     string  `json:"source"`
}

type deployProblem struct {
	status        int
	code, message string
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if s.Deploys == nil {
		deployError(w, http.StatusNotFound, "not_found", "This Ploeg does not accept deploys: PLOEG_DEPLOY_TOKEN is not set.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		deployError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Report a deploy with POST.")
		return
	}
	if !s.Deploys.allows(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ploeg-deploys"`)
		deployError(w, http.StatusUnauthorized, "unauthorized", "The deploy bearer token is required.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, deployBodyLimit))
	if err != nil {
		deployError(w, http.StatusRequestEntityTooLarge, "invalid_request", "A deploy report is at most 16 KiB.")
		return
	}
	d, fp, problem := s.parseDeploy(body, time.Now().UTC())
	if problem != nil {
		deployError(w, problem.status, problem.code, problem.message)
		return
	}
	ctx := r.Context()
	recorded, err := s.Store.RecordDeployment(ctx, d)
	if err != nil {
		s.Log.Error("deploy not recorded", "forge", d.Forge, "repo", d.Owner+"/"+d.Name,
			"environment", d.Environment, "sha", d.SHA, "err", err)
		deployError(w, http.StatusServiceUnavailable, "unavailable", "Ploeg could not record the deploy.")
		return
	}
	s.Log.Info("deploy recorded", "deploy_id", recorded.ID, "new", recorded.Created, "forge", d.Forge,
		"repo", d.Owner+"/"+d.Name, "environment", d.Environment, "sha", d.SHA)
	marked := s.checkDeployment(context.WithoutCancel(ctx), fp, store.PendingDeployment{ID: recorded.ID, Deployment: d})
	writeJSON(w, http.StatusAccepted, map[string]any{"deployId": strconv.FormatInt(recorded.ID, 10), "pullRequests": marked})
}

func (s *Server) parseDeploy(body []byte, now time.Time) (store.Deployment, provider.ForgeProvider, *deployProblem) {
	invalid := func(message string) *deployProblem {
		return &deployProblem{status: http.StatusBadRequest, code: "invalid_request", message: message}
	}
	var req deployRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return store.Deployment{}, nil, invalid("The body must be one JSON object with environment, repo, sha and the optional deployedAt, url and source.")
	}
	if dec.More() {
		return store.Deployment{}, nil, invalid("The body must contain one JSON object.")
	}
	environment, ok := work.NormalizeEnvironment(req.Environment)
	if !ok {
		return store.Deployment{}, nil, invalid("environment must be 1 to 63 letters, digits, dots, underscores and dashes.")
	}
	if !deployForge.MatchString(req.Repo.Forge) || len(req.Repo.Owner) > 255 || !deployOwner.MatchString(req.Repo.Owner) ||
		!deployName.MatchString(req.Repo.Name) {
		return store.Deployment{}, nil, invalid("repo needs a forge, an owner and a name.")
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA))
	if !deploySHA.MatchString(sha) {
		return store.Deployment{}, nil, invalid("sha must be the full 40 or 64 character commit hash.")
	}
	deployedAt := now
	if req.DeployedAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *req.DeployedAt)
		if err != nil {
			return store.Deployment{}, nil, invalid("deployedAt must be an RFC 3339 timestamp.")
		}
		if at.After(now.Add(deployFutureSkew)) {
			return store.Deployment{}, nil, invalid("deployedAt is in the future.")
		}
		deployedAt = at.UTC()
	}
	link, ok := deployLink(req.URL)
	if !ok {
		return store.Deployment{}, nil, invalid("url must be an absolute http or https URL of at most 2048 characters.")
	}
	switch req.Source {
	case "", "ci", "gitops", "manual":
	default:
		return store.Deployment{}, nil, invalid("source must be ci, gitops or manual.")
	}
	fp, ok := s.Forges[req.Repo.Forge]
	if !ok {
		return store.Deployment{}, nil, &deployProblem{status: http.StatusUnprocessableEntity, code: "unknown_forge",
			message: "repo.forge names no forge this Ploeg is configured for."}
	}
	return store.Deployment{Forge: fp.Name(), Owner: req.Repo.Owner, Name: req.Repo.Name, Environment: environment,
		SHA: sha, DeployedAt: deployedAt, URL: link, Source: req.Source}, fp, nil
}

func deployLink(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", false
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	link := u.String()
	return link, len(link) <= 2048
}

// SweepDeployChecks resumes up to deploySweepBatch deployments whose pull
// requests were not all compared yet: a report that hit the batch limit or
// its deadline, or whose comparisons failed (ADR-0047). Each deployment gets
// one bounded pass; failures are logged and retried with a growing pause.
func (s *Server) SweepDeployChecks(ctx context.Context) {
	if len(s.Forges) == 0 {
		return
	}
	due, err := s.Store.DueDeployChecks(ctx, deployCheckLease, deploySweepBatch)
	if err != nil {
		s.Log.Error("deploy check sweep failed", "err", err)
		return
	}
	for _, p := range due {
		if ctx.Err() != nil {
			return
		}
		fp := s.Forges[p.Forge]
		if fp == nil {
			s.Log.Warn("deploy check given up: no forge provider for the deploy", "deploy_id", p.ID, "forge", p.Forge)
			if err := s.Store.CompleteDeployCheck(ctx, p.ID); err != nil {
				s.Log.Error("deploy check not completed", "deploy_id", p.ID, "err", err)
			}
			continue
		}
		s.checkDeployment(ctx, fp, p)
	}
}

func (s *Server) checkDeployment(ctx context.Context, fp provider.ForgeProvider, p store.PendingDeployment) int {
	ctx, cancel := context.WithTimeout(ctx, deployCheckTimeout)
	defer cancel()
	log := s.Log.With("deploy_id", p.ID, "forge", p.Forge, "repo", p.Owner+"/"+p.Name, "environment", p.Environment, "sha", p.SHA)
	candidates, err := s.Store.DeployCandidates(ctx, p.ID, deployMergeSkew, deployCandidateLimit)
	if err != nil {
		log.Error("deploy recorded but its pull requests were not checked; the sweep retries", "err", err)
		return 0
	}
	marked, compared, failed, stopped := 0, 0, 0, false
	for i, c := range candidates {
		if ctx.Err() != nil {
			log.Info("deploy check stopped at its deadline; the sweep continues", "unchecked", len(candidates)-i)
			stopped = true
			break
		}
		ancestor, err := provider.IsAncestor(ctx, fp, c.Repo, c.MergeCommitSHA, p.SHA)
		if errors.Is(err, provider.ErrNoAncestry) {
			log.Warn("the forge cannot compare commits; no pull request is marked as deployed")
			s.completeDeployCheck(ctx, log, p.ID)
			return marked
		}
		if err != nil {
			failed++
			log.Warn("deploy check failed for a pull request; the sweep retries", "pr", c.Number, "merge_commit", c.MergeCommitSHA, "err", err)
			continue
		}
		newly, err := s.Store.RecordDeployCheck(ctx, p.ID, c.PullRequestID, ancestor)
		if err != nil {
			failed++
			log.Error("pull request deploy check not recorded", "pr", c.Number, "err", err)
			continue
		}
		compared++
		if newly {
			marked++
			log.Info("pull request deployed", "pr", c.Number, "merge_commit", c.MergeCommitSHA)
		}
	}
	if !stopped && failed == 0 && len(candidates) < deployCandidateLimit {
		s.completeDeployCheck(ctx, log, p.ID)
		return marked
	}
	if !stopped && failed == 0 {
		log.Info("more merged pull requests await a deploy check than one pass checks; the sweep continues",
			"checked", deployCandidateLimit)
	}
	gaveUp, err := s.Store.DeferDeployCheck(context.WithoutCancel(ctx), p.ID, compared == 0,
		deployCheckBackoff, deployCheckMaxBackoff, deployCheckMaxAttempts)
	if err != nil {
		log.Error("deploy check not rescheduled", "err", err)
	} else if gaveUp {
		log.Warn("deploy check given up after repeated forge failures; the next deploy of this environment checks again",
			"attempts", deployCheckMaxAttempts)
	}
	return marked
}

func (s *Server) completeDeployCheck(ctx context.Context, log *slog.Logger, id int64) {
	if err := s.Store.CompleteDeployCheck(context.WithoutCancel(ctx), id); err != nil {
		log.Error("deploy check not completed", "err", err)
	}
}

func deployError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
