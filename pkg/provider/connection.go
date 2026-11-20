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
// GitHub App's key and an installation id. Method is the connection's
// method, so an adapter can tell an OAuth token from a personal access token.
type ConnectionCredential struct {
	Port           string
	Method         string
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

// ServiceAccountRequest is what a service account bootstrap is asked for:
// the short-lived token of a person who may create service accounts in the
// top-level group of Group, the group whose projects the account may reach,
// the account's username and display name, and when its token expires.
type ServiceAccountRequest struct {
	OwnerToken string
	Group      string
	Username   string
	Name       string
	ExpiresAt  time.Time
}

// ServiceAccountGrant is what a bootstrap made at the vendor. Token is the
// service account's own token. The Created and Added flags say what the
// bootstrap made, so an undo removes only that.
type ServiceAccountGrant struct {
	Account       ConnectionAccount
	Token         string
	TokenID       string
	ExpiresAt     time.Time
	TopGroup      string
	TopGroupID    string
	Group         string
	GroupID       string
	CreatedUser   bool
	AddedMember   bool
	PreviousLevel int
}

// ErrServiceAccountRefused is a vendor refusing a step of a service account
// bootstrap for a reason a person can fix, such as a missing role or a taken
// username. Its message names the step.
var ErrServiceAccountRefused = errors.New("connection: the vendor refused a service account step")

// ServiceAccountBootstrapper turns a person's short-lived token into a
// service account with its own token (ADR-0203): it creates or reuses the
// account, gives it a role on one group, creates its token, undoes what it
// made when a later step fails, and revokes a token presented to it.
type ServiceAccountBootstrapper interface {
	BootstrapServiceAccount(ctx context.Context, inst ConnectionInstance, req ServiceAccountRequest) (ServiceAccountGrant, error)
	UndoServiceAccount(ctx context.Context, inst ConnectionInstance, ownerToken string, grant ServiceAccountGrant) error
	RevokeOwnToken(ctx context.Context, inst ConnectionInstance, token string) error
}

// RotatedToken is a token a rotation returned.
type RotatedToken struct {
	Token     string
	ID        string
	ExpiresAt time.Time
}

// TokenRotator rotates a token with itself: the vendor revokes the old
// token and answers a new one that expires at expiresAt.
type TokenRotator interface {
	RotateOwnToken(ctx context.Context, inst ConnectionInstance, token string, expiresAt time.Time) (RotatedToken, error)
}

// RepositoryHook is one webhook Ploeg keeps on a repository: its URL and the
// secret the vendor proves each delivery with.
type RepositoryHook struct {
	URL    string
	Secret string
}

// RepositoryHooks registers and removes a repository's webhook with a forge
// connection's own credential, addressed by the forge's repository id. Both
// are idempotent: a hook at the same URL is updated, a missing one is done.
type RepositoryHooks interface {
	EnsureRepositoryHook(ctx context.Context, inst ConnectionInstance, cred ConnectionCredential, repositoryID string, hook RepositoryHook) error
	RemoveRepositoryHook(ctx context.Context, inst ConnectionInstance, cred ConnectionCredential, repositoryID, hookURL string) error
}

// Connector is everything one kind offers for runtime connections; nil
// fields are capabilities the kind lacks.
type Connector struct {
	Authorizer      ConnectionAuthorizer
	Prober          ConnectionProber
	Manifest        AppManifest
	Repos           RepositoryLister
	Grants          RepositoryGrantProber
	ServiceAccounts ServiceAccountBootstrapper
	Rotator         TokenRotator
	Hooks           RepositoryHooks
	// TokenOnly reports that the kind offers no OAuth flow at all, so a
	// pasted token is always allowed (ADR-0184 rule 1).
	TokenOnly bool
	// HandOverAgent reports that the kind's tracker intake recognises an
	// assignment to a connection's own account on a Scope bound through that
	// connection, so every connection of the kind hands work over that way
	// (ADR-0207).
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

// RunActorPrefix starts the name of every per-Run forge credential Ploeg
// mints. A forge that attributes an event to such a credential's own bot
// user reports that name as the event's actor, so Ploeg tells its own Runs'
// comments from a person's.
const RunActorPrefix = "ploeg-run-"
