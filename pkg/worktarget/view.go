package worktarget

import (
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// ReadinessView is a target's readiness as the operator API answers it.
type ReadinessView struct {
	// State is ready, not_ready, unknown, or unchecked when the gate has no
	// verdict yet.
	State            string     `json:"state"`
	Reasons          []string   `json:"reasons"`
	Reason           string     `json:"reason,omitempty"`
	PrepareAvailable bool       `json:"prepareAvailable"`
	CheckedAt        *time.Time `json:"checkedAt,omitempty"`
}

// View is a static or runtime Work Target as the operator API answers it;
// never a credential.
type View struct {
	Key           string             `json:"key"`
	Source        string             `json:"source"`
	State         string             `json:"state"`
	InvalidReason *string            `json:"invalidReason"`
	ShadowedBy    *store.StaticEntry `json:"shadowedBy,omitempty"`
	Connection    *string            `json:"connection"`
	Forge         string             `json:"forge"`
	Repository    string             `json:"repository"`
	RepositoryID  *string            `json:"repositoryId"`
	Branch        string             `json:"branch"`
	Label         string             `json:"label"`
	Revision      *int64             `json:"revision"`
	Readiness     ReadinessView      `json:"readiness"`
	CreatedBy     *string            `json:"createdBy"`
	CreatedAt     *time.Time         `json:"createdAt"`
	UpdatedBy     *string            `json:"updatedBy"`
	UpdatedAt     *time.Time         `json:"updatedAt"`
	RemovedAt     *time.Time         `json:"removedAt,omitempty"`
}

func readinessView(r store.TargetReadiness) ReadinessView {
	v := ReadinessView{Reasons: r.Reasons, Reason: r.Reason, PrepareAvailable: r.PrepareAvailable, CheckedAt: r.CheckedAt}
	if v.Reasons == nil {
		v.Reasons = []string{}
	}
	switch {
	case r.CheckedAt == nil && !r.Ready:
		v.State = "unchecked"
	case r.Ready:
		v.State = "ready"
	case r.Unknown:
		v.State = "unknown"
	default:
		v.State = "not_ready"
	}
	return v
}

func ptr[T any](v T) *T { return &v }

// RuntimeView is a runtime target as the operator API answers it.
func RuntimeView(t store.WorkTarget) View {
	v := View{Key: t.Key, Source: SourceRuntime, State: t.State, ShadowedBy: t.ShadowedBy, Connection: ptr(t.Connection),
		Forge: t.Forge, Repository: t.Repository, RepositoryID: ptr(t.RepositoryID), Branch: t.Branch, Label: t.Label,
		Revision: ptr(t.Revision), Readiness: readinessView(t.Readiness), CreatedBy: ptr(t.CreatedBy), UpdatedBy: ptr(t.UpdatedBy),
		RemovedAt: t.RemovedAt}
	if !t.CreatedAt.IsZero() {
		v.CreatedAt, v.UpdatedAt = ptr(t.CreatedAt), ptr(t.UpdatedAt)
	}
	if t.InvalidReason != "" {
		v.InvalidReason = ptr(t.InvalidReason)
	}
	return v
}

// List returns every static target and every live runtime target, static
// first, each sorted by key. visible filters by key; nil shows all.
func (s *Service) List(visible func(key string) bool) []View {
	out := []View{}
	for _, k := range sortedKeys(s.Static) {
		if visible != nil && !visible(k) {
			continue
		}
		t := s.Static[k]
		v := View{Key: k, Source: SourceStatic, State: store.WorkTargetActive, Forge: s.staticForge(t),
			Repository: t.Owner + "/" + t.Repo, Branch: t.BaseBranch, Readiness: ReadinessView{State: "unchecked", Reasons: []string{}}}
		if verdict, seen := s.Gate.Verdict(k); seen {
			switch {
			case verdict.Ready:
				v.Readiness.State = "ready"
			case verdict.Unknown:
				v.Readiness.State, v.Readiness.Reason = "unknown", verdict.Reason
			default:
				v.Readiness.State, v.Readiness.Reason = "not_ready", verdict.Reason
			}
		}
		out = append(out, v)
	}
	for _, t := range s.Live() {
		if visible == nil || visible(t.Key) {
			out = append(out, RuntimeView(t))
		}
	}
	return out
}
