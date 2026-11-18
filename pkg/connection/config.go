// Package connection makes and manages the tracker and forge connections
// of ADR-0195: flows a proven person starts and a consumer completes by
// relaying a code, connections whose secrets live only in the SecretStore
// (ADR-0184), and their probes, reconnects and removal. It names no vendor;
// each kind's provider.Connector does the vendor's part.
package connection

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// Methods a flow may use.
const (
	MethodOAuth                 = "oauth"
	MethodGitHubAppManifest     = "github_app_manifest"
	MethodGitHubAppInstallation = "github_app_installation"
	MethodToken                 = "token"
)

// Ports a connection serves.
const (
	PortTracker = "tracker"
	PortForge   = "forge"
)

// States of a connection (ADR-0195 section 5).
const (
	StateActive          = "active"
	StateAwaitingConsent = "awaiting_consent"
	StateInvalid         = "invalid"
	StateTokenMissing    = "token_missing"
)

// Config is the configuration file's `connections` section (ADR-0195
// section 7). Without Enabled every changing route answers
// connections_not_accepted.
type Config struct {
	Enabled       bool                `yaml:"enabled"`
	Max           int                 `yaml:"max"`
	Allow         []Allow             `yaml:"allow"`
	RequireClaims map[string][]string `yaml:"requireClaims"`
	StepUp        *StepUp             `yaml:"stepUp"`
	Instances     map[string]Instance `yaml:"instances"`
}

// Allow names one connection that may be made at runtime and how.
type Allow struct {
	Port     string   `yaml:"port"`
	Kind     string   `yaml:"kind"`
	Instance string   `yaml:"instance"`
	Methods  []string `yaml:"methods"`
}

// StepUp is the authentication a person's token must show, as RFC 9470
// names it: an acr among AcrValues, an amr among AMR, and an auth_time no
// older than MaxAge seconds.
type StepUp struct {
	AcrValues []string `yaml:"acrValues"`
	AMR       []string `yaml:"amr"`
	MaxAge    int      `yaml:"maxAge"`
}

// Instance is one tracker or forge instance: its kind, its URLs and, for
// OAuth, its statically configured OAuth application. Its id is a forge
// registry id or a configured tracker instance.
type Instance struct {
	Kind   string `yaml:"kind"`
	URL    string `yaml:"url"`
	APIURL string `yaml:"apiUrl"`
	OAuth  *OAuth `yaml:"oauth"`
}

// OAuth is an instance's OAuth application. The client secret is read from
// the environment variable ClientSecretEnv names, never from this file.
type OAuth struct {
	ClientID        string   `yaml:"clientId"`
	ClientSecretEnv string   `yaml:"clientSecretEnv"`
	RedirectURIs    []string `yaml:"redirectUris"`
	Scopes          []string `yaml:"scopes"`
	AuthorizeURL    string   `yaml:"authorizeUrl"`
	TokenURL        string   `yaml:"tokenUrl"`
}

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	envPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	methods     = []string{MethodOAuth, MethodGitHubAppManifest, MethodGitHubAppInstallation, MethodToken}
)

// DefaultMax is the default limit on live runtime connections per Tenant.
const DefaultMax = 20

// Resolve validates c and returns its instances with their client secrets
// read through lookup.
func (c *Config) Resolve(lookup func(string) (string, bool)) (map[string]provider.ConnectionInstance, error) {
	if c == nil {
		return nil, nil
	}
	if c.Max == 0 {
		c.Max = DefaultMax
	}
	if c.Max < 1 || c.Max > 1000 {
		return nil, errors.New("connections.max must be between 1 and 1000")
	}
	if c.StepUp != nil && c.StepUp.MaxAge < 0 {
		return nil, errors.New("connections.stepUp.maxAge must not be negative")
	}
	out := map[string]provider.ConnectionInstance{}
	for id, inst := range c.Instances {
		if !namePattern.MatchString(id) || !namePattern.MatchString(inst.Kind) {
			return nil, fmt.Errorf("connections.instances.%s needs a valid id and kind", id)
		}
		for _, raw := range []string{inst.URL, inst.APIURL} {
			if raw != "" && !httpsURL(raw, true) {
				return nil, fmt.Errorf("connections.instances.%s: url and apiUrl must be absolute http(s) URLs", id)
			}
		}
		resolved := provider.ConnectionInstance{ID: id, Kind: inst.Kind, URL: inst.URL, APIURL: inst.APIURL}
		if o := inst.OAuth; o != nil {
			if o.ClientID == "" || !envPattern.MatchString(o.ClientSecretEnv) || len(o.RedirectURIs) == 0 || len(o.RedirectURIs) > 10 {
				return nil, fmt.Errorf("connections.instances.%s.oauth needs clientId, clientSecretEnv and 1 to 10 redirectUris", id)
			}
			for _, u := range o.RedirectURIs {
				if !httpsURL(u, false) {
					return nil, fmt.Errorf("connections.instances.%s.oauth.redirectUris must be exact https URLs", id)
				}
			}
			for _, u := range []string{o.AuthorizeURL, o.TokenURL} {
				if u != "" && !httpsURL(u, false) {
					return nil, fmt.Errorf("connections.instances.%s.oauth: authorizeUrl and tokenUrl must be https URLs, because the token request carries the client secret", id)
				}
			}
			secret, ok := lookup(o.ClientSecretEnv)
			if !ok || strings.TrimSpace(secret) == "" {
				return nil, fmt.Errorf("connections.instances.%s.oauth: %s is unset", id, o.ClientSecretEnv)
			}
			resolved.OAuth = &provider.OAuthApp{ClientID: o.ClientID, ClientSecret: secret, RedirectURIs: slices.Clone(o.RedirectURIs),
				Scopes: slices.Clone(o.Scopes), AuthorizeURL: o.AuthorizeURL, TokenURL: o.TokenURL}
		}
		out[id] = resolved
	}
	for i, a := range c.Allow {
		if a.Port != PortTracker && a.Port != PortForge {
			return nil, fmt.Errorf("connections.allow[%d].port must be tracker or forge", i)
		}
		inst, ok := out[a.Instance]
		if !ok || inst.Kind != a.Kind {
			return nil, fmt.Errorf("connections.allow[%d] names instance %q of kind %q, which connections.instances does not configure", i, a.Instance, a.Kind)
		}
		if len(a.Methods) == 0 {
			return nil, fmt.Errorf("connections.allow[%d].methods is empty", i)
		}
		for _, m := range a.Methods {
			if !slices.Contains(methods, m) {
				return nil, fmt.Errorf("connections.allow[%d].methods has unknown method %q", i, m)
			}
			if m == MethodOAuth && inst.OAuth == nil {
				return nil, fmt.Errorf("connections.allow[%d] allows oauth but instance %s has no oauth application", i, a.Instance)
			}
		}
	}
	return out, nil
}

func httpsURL(raw string, allowHTTP bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (allowHTTP && u.Scheme == "http")
}

// ValidRedirectURI reports whether raw is an exact HTTPS URL a consumer may
// list in connectionRedirectUris.
func ValidRedirectURI(raw string) bool {
	return len(raw) <= 2048 && httpsURL(raw, false)
}

func (c *Config) allowed(port, kind, instance string) (Allow, bool) {
	for _, a := range c.Allow {
		if a.Port == port && a.Kind == kind && a.Instance == instance {
			return a, true
		}
	}
	return Allow{}, false
}
