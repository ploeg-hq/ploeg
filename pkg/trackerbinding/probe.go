package trackerbinding

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/config"
	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Refusal codes a probe can answer with (ADR-0186 section 2).
const (
	RefuseUnknownTarget       = "unknown_target"
	RefuseConflictsWithStatic = "conflicts_with_static"
	RefuseScopeBoundElsewhere = "scope_bound_elsewhere"
	RefuseCapabilityMissing   = "capability_missing"
	RefuseProbeFailed         = "probe_failed"
	RefuseTargetNotReady      = "target_not_ready"
)

// Check names, in the order a probe runs them.
const (
	CheckTargets        = "targets"
	CheckConflicts      = "conflicts"
	CheckBoundElsewhere = "scope_bound_elsewhere"
	CheckCapabilities   = "capabilities"
	CheckScopeExists    = "scope_exists"
	CheckScopeReadable  = "scope_readable"
	CheckScopeWritable  = "scope_writable"
	CheckAgentVisible   = "agent_can_see_scope"
	CheckStatusesExist  = "statuses_exist"
	CheckTargetsReady   = "targets_ready"
	CheckWebhook        = "webhook"
)

// Check is one result a probe reports, failed or passed.
type Check struct {
	Name    string
	OK      bool
	Code    string
	Message string
}

// Request is a binding to probe: the Team, the connection, the Scope by id
// or by name, and the destination and options.
type Request struct {
	Team       string
	Connection Connection
	ScopeID    string
	ScopeName  string
	Fields     store.TrackerBindingFields
	// Assignees are the Team's tracker users (teams.<team>.assignees).
	Assignees []string
	// Except is the binding a replacement probes for, left out of the
	// scope_bound_elsewhere check.
	Except int64
}

// Result is every check's outcome and, when one failed, the refusal it
// answers with.
type Result struct {
	ScopeID   string
	ScopeName string
	Checks    []Check
	Refusal   string
	Message   string
	Conflict  *store.StaticEntry
	Elsewhere *store.TrackerBinding
	// Webhook is what the probe found for the Scope's webhook.
	Webhook store.BindingWebhook
}

// Failed returns the checks that failed.
func (r Result) Failed() []Check {
	out := []Check{}
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

func (r *Result) add(name string, ok bool, code, format string, args ...any) {
	r.Checks = append(r.Checks, Check{Name: name, OK: ok, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (r *Result) refuse(code, message string) {
	if r.Refusal == "" {
		r.Refusal, r.Message = code, message
	}
}

// Probe runs every check a binding must pass (ADR-0186 section 3) without
// writing anything or registering a webhook.
func (r *Registry) Probe(ctx context.Context, req Request) Result {
	res := Result{ScopeID: req.ScopeID, Webhook: store.BindingWebhook{State: "unchecked", Source: store.BindingWebhookNone}}
	unknown := ""
	if err := config.CheckTargetKeys(req.Fields.Default, req.Fields.Allow, r.Known); err != nil {
		var u *config.UnknownTargetError
		if errors.As(err, &u) {
			unknown = u.Key
		}
		res.add(CheckTargets, false, RefuseUnknownTarget, "target %q is in neither the static targets nor the active runtime targets", unknown)
	} else {
		res.add(CheckTargets, true, "", "every target key is registered")
	}

	tracker := req.Connection.Tracker
	prober, canProbe := tracker.(provider.ScopeProber)
	namer, canName := tracker.(provider.ScopeNamer)
	missing := []string{}
	if !canProbe {
		missing = append(missing, "ScopeProber")
	}
	if req.ScopeID == "" && !canName {
		missing = append(missing, "ScopeNamer")
	}
	if len(missing) > 0 {
		res.add(CheckCapabilities, false, RefuseCapabilityMissing, "connection %s cannot be bound at runtime: its adapter lacks %s", req.Connection.ID, strings.Join(missing, " and "))
	} else {
		res.add(CheckCapabilities, true, "", "the adapter can probe and name Scopes")
	}

	if res.ScopeID == "" && canName {
		id, err := namer.ScopeIDByName(ctx, req.ScopeName)
		switch {
		case errors.Is(err, provider.ErrScopeNotFound):
			res.add(CheckScopeExists, false, RefuseProbeFailed, "no Scope is named %q", req.ScopeName)
		case errors.Is(err, provider.ErrScopeAmbiguous):
			res.add(CheckScopeExists, false, RefuseProbeFailed, "more than one Scope is named %q; bind it by id", req.ScopeName)
		case err != nil:
			res.add(CheckScopeExists, false, RefuseProbeFailed, "the tracker could not resolve the name %q", req.ScopeName)
		default:
			res.ScopeID = id
		}
	}

	if res.ScopeID != "" {
		if entry := r.StaticConflict(req.Connection.Kind, res.ScopeID, req.Team); entry != nil {
			res.Conflict = entry
			res.add(CheckConflicts, false, RefuseConflictsWithStatic, "static route %s already routes this Scope", entry.Entry)
		} else {
			res.add(CheckConflicts, true, "", "no static route conflicts")
		}
		if other := r.boundElsewhere(req.Connection.ID, res.ScopeID, req.Except); other != nil {
			res.Elsewhere = other
			res.add(CheckBoundElsewhere, false, RefuseScopeBoundElsewhere, "the Scope is bound through connection %s", other.Connection)
		} else {
			res.add(CheckBoundElsewhere, true, "", "no other connection binds the Scope")
		}
	}

	if canProbe && res.ScopeID != "" {
		r.probeScope(ctx, prober, req, &res)
	}
	r.checkReadiness(ctx, req.Fields, &res)
	if canProbe && res.ScopeID != "" {
		r.checkWebhook(ctx, req.Connection, &res)
	}

	for _, code := range []string{RefuseUnknownTarget, RefuseConflictsWithStatic, RefuseScopeBoundElsewhere, RefuseCapabilityMissing, RefuseProbeFailed, RefuseTargetNotReady} {
		for _, c := range res.Checks {
			if !c.OK && refusalOf(c.Code) == code {
				res.refuse(code, c.Message)
			}
		}
	}
	return res
}

// AgentCannotSeeScope is the code of the failed agent check; the probe
// answers it as probe_failed.
const AgentCannotSeeScope = "agent_cannot_see_scope"

func refusalOf(code string) string {
	if code == AgentCannotSeeScope {
		return RefuseProbeFailed
	}
	return code
}

func (r *Registry) boundElsewhere(connection, scope string, except int64) *store.TrackerBinding {
	for _, b := range r.Bindings() {
		if b.ScopeID == scope && b.Connection != connection && b.ID != except {
			return &b
		}
	}
	return nil
}

func (r *Registry) probeScope(ctx context.Context, prober provider.ScopeProber, req Request, res *Result) {
	probe, err := prober.ProbeScope(ctx, res.ScopeID)
	switch {
	case errors.Is(err, provider.ErrScopeNotFound):
		res.add(CheckScopeExists, false, RefuseProbeFailed, "the tracker knows no Scope %s", res.ScopeID)
		return
	case err != nil:
		res.add(CheckScopeExists, false, RefuseProbeFailed, "the tracker could not be asked about Scope %s", res.ScopeID)
		return
	}
	res.add(CheckScopeExists, true, "", "the Scope exists")
	res.ScopeName = probe.Name
	if !probe.CanRead {
		res.add(CheckScopeReadable, false, RefuseProbeFailed, "connection %s cannot read the Scope", req.Connection.ID)
		return
	}
	res.add(CheckScopeReadable, true, "", "the connection reads the Scope")
	if probe.CanWrite {
		res.add(CheckScopeWritable, true, "", "the connection writes comments and statuses in the Scope")
	} else {
		res.add(CheckScopeWritable, false, RefuseProbeFailed, "connection %s cannot write comments and statuses in the Scope", req.Connection.ID)
	}
	r.checkAgents(req, probe, res)
	if len(req.Fields.Statuses) == 0 || probe.Statuses == nil {
		res.add(CheckStatusesExist, true, "", "no status to check against the Scope")
		return
	}
	var absent []string
	for _, name := range sortedValues(req.Fields.Statuses) {
		if !slices.ContainsFunc(probe.Statuses, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), name) }) {
			absent = append(absent, name)
		}
	}
	if len(absent) > 0 {
		res.add(CheckStatusesExist, false, RefuseProbeFailed, "the Scope has no status %s", strings.Join(quoted(absent), ", "))
		return
	}
	res.add(CheckStatusesExist, true, "", "every named status exists in the Scope")
}

func (r *Registry) checkAgents(req Request, probe provider.ScopeProbe, res *Result) {
	if req.Connection.HandOverAgent {
		res.add(CheckAgentVisible, true, "", "the connection's own account hands work over and reads the Scope")
		return
	}
	candidates := append(slices.Clone(req.Connection.Agents), req.Assignees...)
	if len(candidates) == 0 {
		res.add(CheckAgentVisible, true, "", "no agent or assignee is named; only the tracker token's own account hands work over")
		return
	}
	if probe.Members == nil {
		res.add(CheckAgentVisible, false, AgentCannotSeeScope, "the Scope's members could not be read, so no agent is known to see it")
		return
	}
	for _, want := range candidates {
		for _, member := range probe.Members {
			if normalIdentity(member) == normalIdentity(want) {
				res.add(CheckAgentVisible, true, "", "an agent or assignee can see the Scope")
				return
			}
		}
	}
	res.add(CheckAgentVisible, false, AgentCannotSeeScope, "none of the agents or the Team's assignees can see the Scope, so no assignment there can hand work over")
}

func (r *Registry) checkReadiness(ctx context.Context, f store.TrackerBindingFields, res *Result) {
	var notReady []string
	for _, key := range destinationKeys(f.Default, f.Allow) {
		if _, static := r.Static.Targets[key]; !static || r.Readiness == nil {
			continue
		}
		if v := r.Readiness.Admit(ctx, key); !v.Ready && !v.Unknown {
			notReady = append(notReady, fmt.Sprintf("%s (%s)", key, v.Reason))
		}
	}
	if len(notReady) > 0 {
		res.add(CheckTargetsReady, false, RefuseTargetNotReady, "not ready: %s", strings.Join(notReady, "; "))
		return
	}
	res.add(CheckTargetsReady, true, "", "every named target is ready")
}

func (r *Registry) checkWebhook(ctx context.Context, c Connection, res *Result) {
	checker, ok := c.Tracker.(provider.WebhookChecker)
	if !ok {
		res.add(CheckWebhook, true, "", "the tracker cannot report webhooks; the binding waits for one")
		return
	}
	found := checker.CheckWebhooks(ctx, []string{res.ScopeID}, c.WebhookEndpoint)
	if len(found) == 0 {
		return
	}
	res.Webhook.State = found[0].Webhook
	switch found[0].Webhook {
	case provider.WebhookPresent:
		res.Webhook.Source = store.BindingWebhookExisting
		res.add(CheckWebhook, true, "", "a webhook already delivers the Scope's events to Ploeg")
	case provider.WebhookMissing:
		res.add(CheckWebhook, true, "", "no webhook delivers the Scope's events to Ploeg yet")
	default:
		res.add(CheckWebhook, true, "", "the Scope's webhooks could not be read")
	}
}

// Wire finds or registers the webhook a new binding relies on (ADR-0186
// section 7). A webhook another live binding on the same connection and
// Scope registered is reused, so one Scope never collects a webhook per
// binding. It returns the webhook, the state the binding starts in and
// whether this call registered the webhook, which the caller deletes again
// when the binding is not stored.
func (r *Registry) Wire(ctx context.Context, c Connection, scope string, found store.BindingWebhook) (store.BindingWebhook, string, bool) {
	if found.State == provider.WebhookPresent {
		return found, store.BindingActive, false
	}
	if id, err := r.Store.RegisteredTrackerBindingWebhook(ctx, c.ID, scope); err == nil && id != "" {
		return store.BindingWebhook{State: provider.WebhookPresent, ID: id, Source: store.BindingWebhookRegistered}, store.BindingActive, false
	}
	registrar, ok := c.Tracker.(provider.ScopeWebhookRegistrar)
	if !ok || !c.RegisterWebhooks || c.Secrets == nil || c.WebhookEndpoint == "" {
		return found, store.BindingAwaitingWebhook, false
	}
	id, err := registrar.RegisterScopeWebhook(ctx, scope, c.WebhookEndpoint, c.Secrets)
	if err != nil {
		r.log().Warn("tracker binding webhook registration failed; the binding waits for a webhook", "connection", c.ID, "scope", scope, "err", err)
		return found, store.BindingAwaitingWebhook, false
	}
	return store.BindingWebhook{State: provider.WebhookPresent, ID: id, Source: store.BindingWebhookRegistered}, store.BindingActive, true
}

// UnwirePlan says what removing b does to the webhook it relies on:
// "remove" when this binding registered it, no other live binding holds the
// same webhook id, no static route covers the Scope and the adapter can
// delete it; "left" when it stays; "none" when the binding registered none.
// The webhook is counted by id in the store, not in this replica's
// snapshot, so neither a binding another replica made nor a second binding
// on the Scope can leak or orphan it.
func (r *Registry) UnwirePlan(ctx context.Context, b store.TrackerBinding) string {
	_, plan := r.unwirePlan(ctx, b)
	return plan
}

func (r *Registry) unwirePlan(ctx context.Context, b store.TrackerBinding) (provider.WebhookRemover, string) {
	if b.Webhook.Source != store.BindingWebhookRegistered || b.Webhook.ID == "" {
		return nil, "none"
	}
	users, err := r.Store.TrackerBindingWebhookUsers(ctx, b.Connection, b.Webhook.ID, b.ID)
	if err != nil || users > 0 {
		return nil, "left"
	}
	for _, rule := range r.Static.Rules {
		if rule.Scope == b.ScopeID {
			return nil, "left"
		}
	}
	c, ok := r.connection(ctx, b.Connection)
	if !ok {
		return nil, "left"
	}
	remover, ok := c.Tracker.(provider.WebhookRemover)
	if !ok {
		return nil, "left"
	}
	return remover, "remove"
}

// Unwire deletes the webhook a removed binding registered when UnwirePlan
// says so, and reports whether it did.
func (r *Registry) Unwire(ctx context.Context, b store.TrackerBinding) bool {
	remover, plan := r.unwirePlan(ctx, b)
	if plan != "remove" {
		return false
	}
	if err := remover.RemoveWebhook(ctx, b.Webhook.ID); err != nil {
		r.log().Warn("tracker binding webhook not removed", "binding", b.ID, "webhook_id", b.Webhook.ID, "err", err)
		return false
	}
	return true
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, strings.TrimSpace(v))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func quoted(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = fmt.Sprintf("%q", v)
	}
	return out
}
