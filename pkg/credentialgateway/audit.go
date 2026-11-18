package credentialgateway

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
)

const maxPendingRefusals = 500

type entry struct {
	run        gatewaycontract.Grant
	family     string
	credential string
	method     string
	route      string
	status     int
	bytes      int64
	duration   time.Duration
	reason     string
}

type useKey struct{ run, family, credential string }

type auditor struct {
	ploegd ploegdClient
	log    *slog.Logger

	mu       sync.Mutex
	uses     map[useKey]*gatewaycontract.Use
	refusals []gatewaycontract.Refusal
	mcpCalls []gatewaycontract.MCPCall
}

const maxPendingMCPCalls = 500

func (a *auditor) mcpCall(c gatewaycontract.MCPCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.mcpCalls) < maxPendingMCPCalls {
		a.mcpCalls = append(a.mcpCalls, c)
		return
	}
	a.log.Warn("credential gateway dropped an MCP call record: too many wait for ploegd", "run", prefix(c.RunToken, 12), "server", c.ServerID, "tool", c.Tool)
}

func newAuditor(ploegd ploegdClient, log *slog.Logger) *auditor {
	return &auditor{ploegd: ploegd, log: log, uses: map[useKey]*gatewaycontract.Use{}}
}

func (a *auditor) record(e entry) {
	refused := e.reason != ""
	identity := e.run.ModelAlias
	a.log.Info("credential gateway request",
		"run", e.run.Run, "work_item", e.run.WorkItem, "team", e.run.Team, "credential", e.credential, "credential_identity", identity,
		"family", e.family, "method", e.method, "route", e.route, "status", e.status, "bytes", e.bytes,
		"duration_ms", e.duration.Milliseconds(), "refused", e.reason)
	a.mu.Lock()
	defer a.mu.Unlock()
	k := useKey{e.run.Run, e.family, e.credential}
	u, ok := a.uses[k]
	if !ok {
		u = &gatewaycontract.Use{RunToken: e.run.Run, Family: e.family, Credential: e.credential}
		a.uses[k] = u
	}
	if refused {
		u.Refused++
		if len(a.refusals) < maxPendingRefusals {
			a.refusals = append(a.refusals, gatewaycontract.Refusal{RunToken: e.run.Run, Family: e.family, Method: e.method,
				Route: e.route, Reason: e.reason, Status: e.status})
		}
		return
	}
	u.Requests++
	u.Bytes += max(e.bytes, 0)
}

func (a *auditor) anonymous(family string, r *http.Request, status int, ip string) {
	a.log.Warn("credential gateway refused a request it could not tie to a Run",
		"family", family, "method", r.Method, "status", status, "source_ip", ip)
}

func (a *auditor) take() gatewaycontract.AuditBatch {
	a.mu.Lock()
	defer a.mu.Unlock()
	batch := gatewaycontract.AuditBatch{Refusals: a.refusals, MCPCalls: a.mcpCalls}
	for _, u := range a.uses {
		batch.Uses = append(batch.Uses, *u)
	}
	a.uses = map[useKey]*gatewaycontract.Use{}
	a.refusals = nil
	a.mcpCalls = nil
	return batch
}

func (a *auditor) putBack(b gatewaycontract.AuditBatch) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range b.Uses {
		k := useKey{u.RunToken, u.Family, u.Credential}
		if have, ok := a.uses[k]; ok {
			have.Requests += u.Requests
			have.Refused += u.Refused
			have.Bytes += u.Bytes
			continue
		}
		copied := u
		a.uses[k] = &copied
	}
	a.refusals = append(b.Refusals, a.refusals...)
	if len(a.refusals) > maxPendingRefusals {
		a.refusals = a.refusals[:maxPendingRefusals]
	}
	a.mcpCalls = append(b.MCPCalls, a.mcpCalls...)
	if len(a.mcpCalls) > maxPendingMCPCalls {
		a.mcpCalls = a.mcpCalls[:maxPendingMCPCalls]
	}
}

func (a *auditor) flush(ctx context.Context) error {
	batch := a.take()
	if len(batch.Uses) == 0 && len(batch.Refusals) == 0 && len(batch.MCPCalls) == 0 {
		return nil
	}
	status, err := a.ploegd.post(ctx, gatewaycontract.AuditPath, batch, nil)
	if err == nil && status/100 != 2 {
		err = &statusError{status: status}
	}
	if err != nil {
		a.putBack(batch)
		a.log.Warn("credential gateway audit not delivered; kept for the next flush", "err", err)
	}
	return err
}

func (a *auditor) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = a.flush(flushCtx)
			cancel()
			return
		case <-t.C:
			_ = a.flush(ctx)
		}
	}
}

type statusError struct{ status int }

func (e *statusError) Error() string { return "ploegd answered " + http.StatusText(e.status) }
