// Package gatewaycontract is the wire contract between ploegd and the
// credential gateway of ADR-0197.
package gatewaycontract

import "time"

// The headers a credential gateway sends ploegd. ploegd trusts the pin
// headers only on a request whose gateway token it verified.
const (
	AuthorizationHeader = "X-Ploeg-Gateway-Authorization"
	PodNameHeader       = "X-Ploeg-Gateway-Pod-Name"
	PodUIDHeader        = "X-Ploeg-Gateway-Pod-Uid"
	PodIPHeader         = "X-Ploeg-Gateway-Pod-Ip"
	RefusalHeader       = "X-Ploeg-Refusal"
	Audience            = "ploegd"
	GrantsPath          = "/api/v1/credential-gateway/grants"
	SessionGrantsPath   = "/api/v1/credential-gateway/session-grants"
	AuditPath           = "/api/v1/credential-gateway/audit"
)

// Refusal reasons the gateway and ploegd name.
const (
	RefusedGatewayMode  = "credential_gateway_mode"
	RefusedUnavailable  = "credential_gateway_unavailable"
	RefusedNoCapability = "missing_capability"
	RefusedCapability   = "capability_refused"
	RefusedRunSettled   = "run_settled"
	RefusedPin          = "pinned_to_another_pod"
	RefusedRateLimit    = "rate_limited"
	RefusedRoute        = "forbidden_route"
	RefusedClaim        = "claim_refused"
	RefusedNoForge      = "no_forge_credential"
	RefusedNoSession    = "missing_session_token"
	RefusedSession      = "session_refused"
	RefusedSessionNone  = "session_unknown"
)

// Pin is the pod that claimed a Run through a credential gateway: its
// capability is served only from this pod.
type Pin struct {
	Namespace string `json:"namespace"`
	PodName   string `json:"podName"`
	PodUID    string `json:"podUid"`
	PodIP     string `json:"podIp"`
}

// GrantRequest asks ploegd for the grant of the Run a capability belongs to.
type GrantRequest struct {
	Capability string `json:"capability"`
	SourceIP   string `json:"sourceIp"`
	SourceUID  string `json:"sourceUid"`
}

// SessionGrantRequest asks ploegd for the grant of the routine Run a Claude
// session token belongs to, from the runner pod the gateway saw it come from
// (ADR-0197 section 7). ploegd verifies the token and pins the grant to that
// pod on first use.
type SessionGrantRequest struct {
	SessionToken string `json:"sessionToken"`
	SourceIP     string `json:"sourceIp"`
	SourceUID    string `json:"sourceUid"`
	PodName      string `json:"podName"`
}

// Grant is what a credential gateway attaches for one Run. It never leaves
// ploegd except to the gateway of the Run's namespace.
type Grant struct {
	Run                 string     `json:"run"`
	WorkItem            string     `json:"workItem"`
	Team                string     `json:"team"`
	Role                string     `json:"role"`
	Writes              bool       `json:"writes"`
	Branch              string     `json:"branch,omitempty"`
	Prepares            bool       `json:"prepares,omitempty"`
	RepositoryHooks     bool       `json:"repositoryHooks,omitempty"`
	ForgeDialect        string     `json:"forgeDialect,omitempty"`
	ForgeURL            string     `json:"forgeUrl,omitempty"`
	Owner               string     `json:"owner,omitempty"`
	Repo                string     `json:"repo,omitempty"`
	BaseBranch          string     `json:"baseBranch,omitempty"`
	ForgeToken          string     `json:"forgeToken,omitempty"`
	ForgeTokenExpiresAt *time.Time `json:"forgeTokenExpiresAt,omitempty"`
	ModelKey            string     `json:"modelKey,omitempty"`
	ModelAlias          string     `json:"modelAlias,omitempty"`
	MCPAccessGroups     []string   `json:"mcpAccessGroups,omitempty"`
	// MCP are the registered MCP servers the Run's Role was given, each
	// with the credential the gateway attaches (ADR-0199 section 4).
	MCP      []MCPServer `json:"mcp,omitempty"`
	Pin      Pin         `json:"pin"`
	Deadline time.Time   `json:"deadline"`
	// ClaudeSession is the session id, without its prefix, of a routine
	// Run's grant; Title and Reference let its runner open the pull request.
	ClaudeSession string `json:"claudeSession,omitempty"`
	Title         string `json:"title,omitempty"`
	Reference     string `json:"reference,omitempty"`
	// TransactionToken is the Run's transaction token, signed for this
	// answer only. The gateway attaches it to the Run's worker-control
	// calls in place of the capability, so it never enters the Run pod.
	TransactionToken string `json:"transactionToken,omitempty"`
}

// MCPServer is one MCP server a Run may reach through the gateway's mcp
// route family, keyed by Key in the path /mcp/<key>.
type MCPServer struct {
	ID         string            `json:"id"`
	Key        string            `json:"key"`
	URL        string            `json:"url"`
	AuthKind   string            `json:"authKind"`
	Header     string            `json:"header,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Tools      map[string]string `json:"tools"`
}

// RunTools is what a worker installs and configures for one Run: the MCP
// server keys its harness reaches through the gateway and the skill
// bundles it installs read-only, by digest (ADR-0199).
type RunTools struct {
	MCPServers []string    `json:"mcpServers"`
	Withheld   string      `json:"withheld,omitempty"`
	Skills     []RunSkills `json:"skills"`
}

// RunSkills is one skill bundle of a Run.
type RunSkills struct {
	Key    string   `json:"key"`
	Digest string   `json:"digest"`
	Skills []string `json:"skills,omitempty"`
}

// Withheld reasons of RunTools.
const WithheldNoGateway = "mcp_requires_credential_gateway"

// Use is one batch of a gateway's attached uses for one Run, route family and
// credential kind.
type Use struct {
	RunToken   string `json:"run"`
	Family     string `json:"family"`
	Credential string `json:"credential"`
	Requests   int64  `json:"requests"`
	Refused    int64  `json:"refused"`
	Bytes      int64  `json:"bytes"`
}

// Refusal is one request a credential gateway refused for a known Run.
type Refusal struct {
	RunToken string `json:"run"`
	Family   string `json:"family"`
	Method   string `json:"method"`
	Route    string `json:"route"`
	Reason   string `json:"reason"`
	Status   int    `json:"status"`
}

// AuditBatch is what a gateway sends ploegd's audit route.
type AuditBatch struct {
	Uses     []Use     `json:"uses"`
	Refusals []Refusal `json:"refusals"`
	MCPCalls []MCPCall `json:"mcpCalls,omitempty"`
}

// MCPCall is one MCP request the gateway's mcp route family answered for a
// Run: the server, method, tool and access, and "allowed" or the reason the
// gateway refused it (ADR-0199 section 4).
type MCPCall struct {
	RunToken   string `json:"run"`
	ServerID   string `json:"serverId"`
	Server     string `json:"server"`
	Method     string `json:"method"`
	Tool       string `json:"tool,omitempty"`
	Access     string `json:"access,omitempty"`
	Outcome    string `json:"outcome"`
	Status     int    `json:"status"`
	BytesIn    int    `json:"bytesIn"`
	BytesOut   int    `json:"bytesOut"`
	DurationMS int64  `json:"durationMs"`
}
