package provider

import (
	"context"
	"errors"
	"time"
)

// Connection capabilities of ADR-0195. A kind's connector implements them
// for every instance of that kind; it is handed the instance's static
// configuration and the credential on each call, so one connector serves
// every runtime connection of its kind.

var (
	// ErrCredentialRefused is a vendor answering 401 or 403 to a credential.
	ErrCredentialRefused = errors.New("connection: the vendor refused the credential")
	// ErrVendorUnavailable is a vendor that could not be reached or answered
	// with a server error.
	ErrVendorUnavailable = errors.New("connection: the vendor is unavailable")
	// ErrInstallationSuspended is a GitHub App installation that is
	// suspended.
	ErrInstallationSuspended = errors.New("connection: the installation is suspended")
	// ErrNotSupported is a vendor that offers no API for the step.
	ErrNotSupported = errors.New("connection: the vendor offers no API for this")
	// ErrForeignInstallation is an installation of another App.
	ErrForeignInstallation = errors.New("connection: the installation belongs to another App")
)

// ConnectionInstance is the static configuration of one tracker or forge
// instance: never a value from a request.
type ConnectionInstance struct {
	ID     string
	Kind   string
	URL    string
	APIURL string
	OAuth  *OAuthApp
}

// OAuthApp is an instance's statically configured OAuth application.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	RedirectURIs []string
	Scopes       []string
	AuthorizeURL string
	TokenURL     string
}

// ConnectionAccount is the vendor account a credential acts as.
type ConnectionAccount struct {
	ID             string `json:"id"`
	Login          string `json:"login"`
	InstallationID string `json:"installationId,omitempty"`
}

// OAuthCredential is what an OAuth exchange or refresh returns.
type OAuthCredential struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

// ConnectionAuthorizer builds an authorize URL, exchanges a code, refreshes
// and revokes (ADR-0195 section 8).
type ConnectionAuthorizer interface {
	PKCE() bool
	AuthorizeURL(inst ConnectionInstance, redirectURI, state, challenge string) (string, error)
	Exchange(ctx context.Context, inst ConnectionInstance, redirectURI, code, verifier string) (OAuthCredential, error)
	Refresh(ctx context.Context, inst ConnectionInstance, redirectURI, refreshToken string) (OAuthCredential, error)
	Revoke(ctx context.Context, inst ConnectionInstance, token string) error
}

// ConnectionCredential is what a probe acts with: an access token, or a
// GitHub App's key and an installation id.
type ConnectionCredential struct {
	Port           string
	Token          string
	AppID          string
	PrivateKey     string
	InstallationID string
}

// ConnectionProbe is what a probe found.
type ConnectionProbe struct {
	Account ConnectionAccount
	// Scopes are the scopes or permissions the credential holds; Missing
	// are those the adapter needs and it lacks.
	Scopes  []string
	Missing []string
	// CanMint reports, for a forge, whether per-Run credentials can be
	// minted from the credential.
	CanMint *bool
	// Visible are, for a tracker, the Scopes the credential can see: their
	// count and the first 50.
	Visible *VisibleScopes
}

// VisibleScopes are the tracker Scopes a credential can see.
type VisibleScopes struct {
	Count int            `json:"count"`
	First []VisibleScope `json:"first"`
}

// VisibleScope is one tracker Scope by id and display name.
type VisibleScope struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ConnectionProber reports who a credential acts as and what it can reach.
type ConnectionProber interface {
	ProbeConnection(ctx context.Context, inst ConnectionInstance, cred ConnectionCredential) (ConnectionProbe, error)
}

// ManifestRequest is what an App manifest is built from.
type ManifestRequest struct {
	Port        string
	Name        string
	HomeURL     string
	HookURL     string
	RedirectURL string
	SetupURL    string
}

// ManifestApp is what a manifest conversion returns.
type ManifestApp struct {
	AppID         string
	Slug          string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	PrivateKey    string
	Owner         ConnectionAccount
	HTMLURL       string
}

// AppInstallation is one installation of an App.
type AppInstallation struct {
	ID        string
	AppID     string
	Account   ConnectionAccount
	Suspended bool
}

// AppManifest creates an App from a manifest, installs it and uninstalls
// it (ADR-0195 section 3).
type AppManifest interface {
	Manifest(inst ConnectionInstance, req ManifestRequest) (string, error)
	ManifestPostURL(inst ConnectionInstance, owner, state string) string
	InstallURL(inst ConnectionInstance, slug, state string) string
	SettingsURL(inst ConnectionInstance, app ManifestApp) string
	Convert(ctx context.Context, inst ConnectionInstance, code string) (ManifestApp, error)
	SetWebhookSecret(ctx context.Context, inst ConnectionInstance, appID, privateKey, secret string) error
	Installation(ctx context.Context, inst ConnectionInstance, appID, privateKey, installationID string) (AppInstallation, error)
	Uninstall(ctx context.Context, inst ConnectionInstance, appID, privateKey, installationID string) error
}

// GrantedRepository is one repository a forge connection's grant reaches.
type GrantedRepository struct {
	ID       string `json:"id"`
	FullName string `json:"fullName"`
	Private  bool   `json:"private"`
	CanWrite bool   `json:"canWrite"`
}

// RepositoryLister lists the repositories a forge connection's grant
// reaches, one page at a time (ADR-0193 section 3).
type RepositoryLister interface {
	ListGrantedRepositories(ctx context.Context, inst ConnectionInstance, cred ConnectionCredential, page, perPage int) ([]GrantedRepository, bool, error)
}

// ErrRepositoryNotFound is a forge that does not show the repository at all.
var ErrRepositoryNotFound = errors.New("connection: the forge does not show the repository")

// ErrRepositoryNotGranted is a repository the forge shows but the
// connection's grant does not reach.
var ErrRepositoryNotGranted = errors.New("connection: the grant does not reach the repository")

// RepositoryGrant is what a forge connection's grant allows on one
// repository (ADR-0193 section 4).
type RepositoryGrant struct {
	// ID is the forge's own repository id, which survives a rename.
	ID string
	// FullName is the repository's current path as the forge spells it.
	FullName string
	// CanWrite reports that the grant may read, push branches and open pull
	// requests.
	CanWrite bool
	// Permissions are the permissions the grant holds, for the report.
	Permissions []string
	// DefaultBranch is the repository's default branch.
	DefaultBranch string
}

// RepositoryGrantProber answers, for one forge connection, whether its
// grant reaches a repository and with which permissions, and inspects a
// granted repository's readiness with the connection's own credential
// (ADR-0193 section 4). A kind without it cannot register runtime targets.
type RepositoryGrantProber interface {
	ProbeRepositoryGrant(ctx context.Context, inst ConnectionInstance, cred ConnectionCredential, fullName string) (RepositoryGrant, error)
	InspectGrantedRepository(ctx context.Context, inst ConnectionInstance, cred ConnectionCredential, fullName, branch string) (RepositoryState, error)
}

// Connector is everything one kind offers for runtime connections; nil
// fields are capabilities the kind lacks.
type Connector struct {
	Authorizer ConnectionAuthorizer
	Prober     ConnectionProber
	Manifest   AppManifest
	Repos      RepositoryLister
	Grants     RepositoryGrantProber
	// TokenOnly reports that the kind offers no OAuth flow at all, so a
	// pasted token is always allowed (ADR-0184 rule 1).
	TokenOnly bool
	// HandOverAgent reports that the kind's tracker intake recognises an
	// assignment to a connection's own account on a Scope bound through that
	// connection (ADR-0195 section 9). Without it handOverAgent: true is
	// refused with capability_missing.
	HandOverAgent bool
}

// GrantEvent is a change to what a forge App installation may reach
// (ADR-0193 section 7): installation created, deleted, suspend, unsuspend,
// and installation_repositories added or removed.
type GrantEvent struct {
	AppID          string
	InstallationID string
	Event          string
	Action         string
	AccountID      string
	Added          []string
	Removed        []string
	// AddedIDs and RemovedIDs are the forge repository ids of Added and
	// Removed, in the same order.
	AddedIDs   []string
	RemovedIDs []string
}
