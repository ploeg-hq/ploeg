package connection

import (
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// Error is a refusal with the operator API's status and stable code.
type Error struct {
	Status     int
	Code       string
	Message    string
	Checks     []Check
	UsedBy     []Use
	Connection string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func refuse(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Caller is the consumer and proven person making a request.
type Caller struct {
	Consumer     string
	Person       string
	Via          string
	RedirectURIs []string
	Attribution  any
}

// Use is a binding or runtime target that names a connection.
type Use struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	State string `json:"state,omitempty"`
}

// Check is one probe check and its result: pass, fail or unknown.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// ProbeReport is what a probe found; never a secret.
type ProbeReport struct {
	At       time.Time                   `json:"at"`
	Result   string                      `json:"result"`
	Checks   []Check                     `json:"checks"`
	Account  *provider.ConnectionAccount `json:"account,omitempty"`
	Scopes   []string                    `json:"scopes,omitempty"`
	Missing  []string                    `json:"missing,omitempty"`
	CanMint  *bool                       `json:"canMint,omitempty"`
	Visible  *provider.VisibleScopes     `json:"visibleScopes,omitempty"`
	State    string                      `json:"-"`
	Reason   string                      `json:"-"`
	Unknown  bool                        `json:"-"`
	Accounts provider.ConnectionAccount  `json:"-"`
}

// View is a connection as the operator API answers it.
type View struct {
	ID            string                      `json:"id"`
	Port          string                      `json:"port"`
	Kind          string                      `json:"kind"`
	Instance      string                      `json:"instance"`
	Method        *string                     `json:"method"`
	Account       *provider.ConnectionAccount `json:"account"`
	Source        string                      `json:"source"`
	State         string                      `json:"state"`
	InvalidReason *string                     `json:"invalidReason"`
	CanMint       *bool                       `json:"canMint,omitempty"`
	Label         string                      `json:"label"`
	Revision      int64                       `json:"revision"`
	App           *string                     `json:"app,omitempty"`
	Secrets       map[string]secretstore.Ref  `json:"secrets,omitempty"`
	ExpiresAt     *time.Time                  `json:"expiresAt,omitempty"`
	LastProbe     *ProbeReport                `json:"lastProbe"`
	UsedBy        []Use                       `json:"usedBy"`
	CreatedBy     *string                     `json:"createdBy"`
	CreatedAt     *time.Time                  `json:"createdAt"`
	UpdatedBy     *string                     `json:"updatedBy"`
	UpdatedAt     *time.Time                  `json:"updatedAt"`
	ReconnectedBy *string                     `json:"reconnectedBy,omitempty"`
	ReconnectedAt *time.Time                  `json:"reconnectedAt,omitempty"`
}

// AppView is a connection app: a GitHub App Ploeg holds, never a secret.
type AppView struct {
	ID            string                     `json:"id"`
	Port          string                     `json:"port"`
	Kind          string                     `json:"kind"`
	Instance      string                     `json:"instance"`
	AppID         string                     `json:"appId"`
	Slug          string                     `json:"slug"`
	ClientID      string                     `json:"clientId"`
	Owner         provider.ConnectionAccount `json:"owner"`
	SettingsURL   string                     `json:"settingsUrl"`
	Installations []string                   `json:"installations"`
	Secrets       map[string]secretstore.Ref `json:"secrets"`
	CreatedBy     string                     `json:"createdBy"`
	CreatedAt     time.Time                  `json:"createdAt"`
}

// FlowView is a flow as the operator API answers it. State is set only in
// the answer that issues it.
type FlowView struct {
	ID        string    `json:"id"`
	Method    string    `json:"method"`
	Port      string    `json:"port"`
	Kind      string    `json:"kind"`
	Instance  string    `json:"instance"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
	State     string    `json:"state,omitempty"`
}

// Next is what the consumer does after starting a flow.
type Next struct {
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	Manifest     string `json:"manifest,omitempty"`
	PostURL      string `json:"postUrl,omitempty"`
	InstallURL   string `json:"installUrl,omitempty"`
}

// Started is the answer to a start.
type Started struct {
	Flow FlowView `json:"flow"`
	Next Next     `json:"next"`
}

// Completed is the answer to a completion.
type Completed struct {
	Connection     *View           `json:"connection,omitempty"`
	ConnectionApp  *AppView        `json:"connectionApp,omitempty"`
	ServiceAccount *ServiceAccount `json:"serviceAccount,omitempty"`
	Flow           *FlowView       `json:"flow,omitempty"`
	Next           *Next           `json:"next,omitempty"`
	Warnings       []Warning       `json:"warnings,omitempty"`
	Replayed       bool            `json:"replayed"`
	Status         int             `json:"-"`
}

// ServiceAccount is what a gitlab_service_account completion made at
// GitLab (ADR-0203), never a token.
type ServiceAccount struct {
	Username       string    `json:"username"`
	Group          string    `json:"group"`
	TopGroup       string    `json:"topGroup"`
	Created        bool      `json:"created"`
	TokenExpiresAt time.Time `json:"tokenExpiresAt"`
}

// Warning is something a completion could not do although the connection
// was made, such as revoking the token it was given.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// FlowStatus is the answer to a flow read.
type FlowStatus struct {
	Flow          FlowView `json:"flow"`
	Connection    *View    `json:"connection,omitempty"`
	ConnectionApp *AppView `json:"connectionApp,omitempty"`
}

// Left is something a removal could not clean at the vendor.
type Left struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	URL  string `json:"url,omitempty"`
}

// StartRequest is the body of POST /connection-flows.
type StartRequest struct {
	Port        string `json:"port"`
	Kind        string `json:"kind"`
	Instance    string `json:"instance"`
	Method      string `json:"method"`
	RedirectURI string `json:"redirectUri,omitempty"`
	App         string `json:"app,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Connection  string `json:"connection,omitempty"`
	ID          string `json:"id,omitempty"`
	Label       string `json:"label,omitempty"`
}

// CompleteRequest is the body of POST /connection-flows/complete.
type CompleteRequest struct {
	State          string `json:"state"`
	Code           string `json:"code,omitempty"`
	InstallationID string `json:"installationId,omitempty"`
	SetupAction    string `json:"setupAction,omitempty"`
	Token          string `json:"token,omitempty"`
	// Group is the full path of the GitLab group whose projects a
	// gitlab_service_account connection may reach.
	Group string `json:"group,omitempty"`
}

// PatchRequest is the body of PATCH on a connection.
type PatchRequest struct {
	Label *string `json:"label,omitempty"`
}

// Static is a connection configured through the environment or Helm.
type Static struct {
	ID       string
	Port     string
	Kind     string
	Instance string
}
