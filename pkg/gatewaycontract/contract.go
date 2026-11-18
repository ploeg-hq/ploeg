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
	Pin                 Pin        `json:"pin"`
	Deadline            time.Time  `json:"deadline"`
}

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
}
