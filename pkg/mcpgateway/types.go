// Package mcpgateway is the MCP route family of the credential gateway
// (ADR-0199 section 4, ADR-0197). A Run's harness reaches a registered MCP
// server only through Handler, which checks the Run's capability, forwards
// only the methods and tools the Run's Role was granted, attaches the
// server's credential outside the Run pod, rewrites the server's tool list
// to Ploeg's own, frames tool output as untrusted data (ADR-0170) and
// audits every call without its arguments or results.
package mcpgateway

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Access is what a granted tool may do.
type Access string

const (
	// AccessRead is a tool that only reads.
	AccessRead Access = "read"
	// AccessWrite is a tool that may change the server's system.
	AccessWrite Access = "write"
)

// Auth kinds a server's credential is attached with. An OAuth client
// credentials server reaches the gateway as AuthBearer with the access
// token its grant carries.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthHeader = "header"
)

// Auth says how the gateway attaches a server's credential.
type Auth struct {
	Kind   string
	Header string
}

// ServerGrant is one registered MCP server as one Run may reach it.
type ServerGrant struct {
	ID   string
	Key  string
	URL  string
	Auth Auth
	// Credential is the value the gateway attaches. It lives only in the
	// gateway's memory and never reaches the Run pod.
	Credential string
	// Tools are the tools the Run's Role was given, with Ploeg's access.
	Tools map[string]Access
	// ReadOnly refuses every write tool, whatever Tools says.
	ReadOnly bool
}

// Grant is what one Run may reach through the MCP route family.
type Grant struct {
	RunID    int64
	WorkItem string
	Tenant   string
	Servers  map[string]ServerGrant
}

// Errors a GrantSource answers for a capability it cannot serve.
var (
	// ErrUnknownCapability is a capability no live Run holds, or one
	// presented from a pod it is not pinned to. Answered 401.
	ErrUnknownCapability = errors.New("mcpgateway: unknown capability")
	// ErrRunSettled is the capability of a Run that settled, expired or was
	// withdrawn. Answered 410.
	ErrRunSettled = errors.New("mcpgateway: run settled")
)

// GrantSource resolves a Run's capability to its grant. r is the harness's
// request, so the source can check the capability's pod pin.
type GrantSource interface {
	Grant(ctx context.Context, capability string, r *http.Request) (Grant, error)
}

// Call is one audit record. It never carries a tool's arguments or result.
type Call struct {
	Run      int64
	WorkItem string
	Tenant   string
	ServerID string
	Server   string
	Method   string
	Tool     string
	Access   Access
	// Outcome is "allowed" or the reason the gateway refused the call.
	Outcome  string
	Status   int
	BytesIn  int
	BytesOut int
	Duration time.Duration
}

// AuditSink receives one Call for every request the handler answers.
type AuditSink interface {
	Record(ctx context.Context, c Call)
}

// Limits bound one request, one response and one Run's tool calls.
type Limits struct {
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxEvents        int
	CallsPerMinute   int
	CallsPerRun      int
	MaxDescription   int
}

// DefaultLimits are ADR-0199 section 4's defaults.
var DefaultLimits = Limits{
	MaxRequestBytes:  1 << 20,
	MaxResponseBytes: 1 << 20,
	MaxEvents:        1000,
	CallsPerMinute:   60,
	CallsPerRun:      1000,
	MaxDescription:   2000,
}

func (l Limits) orDefault() Limits {
	d := DefaultLimits
	if l.MaxRequestBytes > 0 {
		d.MaxRequestBytes = l.MaxRequestBytes
	}
	if l.MaxResponseBytes > 0 {
		d.MaxResponseBytes = l.MaxResponseBytes
	}
	if l.MaxEvents > 0 {
		d.MaxEvents = l.MaxEvents
	}
	if l.CallsPerMinute > 0 {
		d.CallsPerMinute = l.CallsPerMinute
	}
	if l.CallsPerRun > 0 {
		d.CallsPerRun = l.CallsPerRun
	}
	if l.MaxDescription > 0 {
		d.MaxDescription = l.MaxDescription
	}
	return d
}

// ProtocolVersions are the MCP protocol versions the handler was tested
// with. A server that answers initialize with another is refused.
var ProtocolVersions = []string{"2025-03-26", "2025-06-18", "2025-11-25"}
