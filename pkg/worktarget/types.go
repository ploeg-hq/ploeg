// Package worktarget keeps the Work Targets a proven operator registers at
// runtime (ADR-0193): it probes a repository through the forge connection
// that holds its grant, stores the target, merges the live ones behind the
// static targets, decides each one's state, and answers the claim from the
// row rather than from a replica's cache. It names no vendor; each kind's
// provider.RepositoryGrantProber does the forge's part.
package worktarget

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Intervals of ADR-0193 section 4 and section 7.
const (
	PollEvery      = 30 * time.Second
	ReconcileEvery = time.Hour
	MaxLabel       = 200
	DefaultTenant  = "default"
)

// Readiness reasons, as ADR-0194 names them.
const (
	ReasonArchived               = "archived"
	ReasonMirror                 = "mirror"
	ReasonInstructionFileMissing = "instruction_file_missing"
)

// Sources a target in the effective registry comes from.
const (
	SourceStatic  = "static"
	SourceRuntime = "runtime"
)

var (
	keyPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,127}$`)
	operatorName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+){1,20}$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,254}$`)
)

// ValidRepository reports whether s is an owner/name path, GitLab subgroups
// included, and nothing shaped like a URL, a clone address or a token.
func ValidRepository(s string) bool {
	if len(s) > 512 || !repositoryPattern.MatchString(s) || strings.Contains(s, "..") || strings.HasSuffix(strings.ToLower(s), ".git") {
		return false
	}
	for _, segment := range strings.Split(s, "/") {
		if strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}

// ValidBranch reports whether s is a branch name a target may be pinned to.
func ValidBranch(s string) bool {
	return branchPattern.MatchString(s) && !strings.Contains(s, "..") && !strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".lock")
}

// LooksLikeSecret reports whether free text carries something shaped like a
// token or key, which a request is refused for rather than storing it.
func LooksLikeSecret(s string) bool {
	lower := strings.ToLower(s)
	for _, marker := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-", "gloas-", "-----begin", "bearer ", "://", "x-access-token"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Error is a refusal with the operator API's status and stable code.
type Error struct {
	Status   int
	Code     string
	Message  string
	Checks   []Check
	Static   *store.StaticEntry
	Current  *store.WorkTarget
	Bindings []BindingUse
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func refuse(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Check is one probe check and its result: pass, fail or unknown.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// BindingUse is a live tracker binding that names a target key.
type BindingUse struct {
	ID   string `json:"id"`
	Team string `json:"team"`
}

// Request is the body of a registration.
type Request struct {
	Connection string
	Repository string
	Branch     string
	Key        string
	Label      string
}

// ConnectionInfo is a forge connection as runtime registration sees it.
type ConnectionInfo struct {
	ID       string
	Kind     string
	Instance string
	State    string
	Runtime  bool
	// CanMint is what the connection's last probe found: it can mint a
	// per-Run credential (ADR-0203).
	CanMint bool
}

// ConnectionActive is the state of a usable connection.
const ConnectionActive = "active"

// Connections is the seam to the connections of ADR-0195.
type Connections interface {
	// ForgeConnection returns the forge connection id; found is false when
	// no forge connection, static or runtime, has it.
	ForgeConnection(ctx context.Context, id string) (ConnectionInfo, bool, error)
	// Credential returns a runtime connection's credential, refreshed.
	Credential(ctx context.Context, id string) (provider.ConnectionCredential, error)
	// ConnectionsOfInstallation returns the runtime connections of one App
	// installation.
	ConnectionsOfInstallation(ctx context.Context, appID, installationID string) ([]string, error)
}

// HookSecrets is the part of the connections seam that keeps a
// connection's webhook secret (ADR-0203). A Connections without it registers
// no repository webhook.
type HookSecrets interface {
	WebhookSecret(ctx context.Context, id string) (secret string, held bool, err error)
}

// Store is the persistence the service needs.
type Store interface {
	LiveWorkTargets(ctx context.Context, tenant string) ([]store.WorkTarget, error)
	WorkTargetRevision(ctx context.Context) (int64, error)
	LiveWorkTarget(ctx context.Context, tenant, key string) (store.WorkTarget, error)
	WorkTargetForRepository(ctx context.Context, tenant, forge, repository string) (store.WorkTarget, bool, error)
	LiveWorkTargetsByConnection(ctx context.Context, tenant, connection string) ([]store.WorkTarget, error)
	CreateWorkTarget(ctx context.Context, in store.NewWorkTarget, audit store.WorkTargetAudit) (store.WorkTarget, bool, error)
	ReplaceWorkTarget(ctx context.Context, r store.WorkTargetReplacement, audit store.WorkTargetAudit) (store.WorkTarget, error)
	RemoveWorkTarget(ctx context.Context, tenant, key string, audit store.WorkTargetAudit, extra map[string]any) (store.WorkTarget, bool, error)
	SetWorkTargetState(ctx context.Context, id int64, from store.WorkTarget, to store.WorkTargetStateChange, action string, audit store.WorkTargetAudit, extra map[string]any) (bool, error)
	SetWorkTargetReadiness(ctx context.Context, id int64, readiness store.TargetReadiness) error
	AuditWorkTarget(ctx context.Context, action string, t store.WorkTarget, audit store.WorkTargetAudit, extra map[string]any) error
	OpenWorkItemsOnRepository(ctx context.Context, forge, owner, repo string, limit int) ([]string, error)
	LiveTrackerBindings(ctx context.Context) ([]store.TrackerBinding, error)
}

// Bindings is the seam to ADR-0186's registry: its rebuild and the live
// bindings that name target keys.
type Bindings interface {
	Rebuild(ctx context.Context) error
	Bindings() []store.TrackerBinding
}
