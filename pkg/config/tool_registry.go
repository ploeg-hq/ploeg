package config

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

// DefaultMaxToolRegistryEntries is how many live runtime servers, and
// separately skill sources, a Tenant holds when toolRegistry names no max.
const DefaultMaxToolRegistryEntries = 50

// ToolRegistry is the installation's opt-in to the MCP server and skill
// source registry of ADR-0199. Omitted, or Enabled false, refuses every
// runtime change with tool_registry_not_accepted; static entries still
// load.
type ToolRegistry struct {
	Enabled         bool                    `yaml:"enabled"`
	AllowWriteTools bool                    `yaml:"allowWriteTools"`
	PlainHTTPHosts  []string                `yaml:"plainHTTPHosts"`
	PrivateHosts    []string                `yaml:"privateHosts"`
	Max             *int                    `yaml:"max"`
	RequireClaims   map[string][]string     `yaml:"requireClaims"`
	StepUp          *connection.StepUp      `yaml:"stepUp"`
	MCPServers      map[string]StaticServer `yaml:"mcpServers"`
	SkillSources    map[string]StaticSource `yaml:"skillSources"`
}

// StaticServer is an MCP server the configuration file registers. Its
// credential is read by ploegd from CredentialEnv and attached only by the
// credential gateway.
type StaticServer struct {
	Name          string              `yaml:"name"`
	URL           string              `yaml:"url"`
	Transport     string              `yaml:"transport"`
	Auth          toolregistry.Auth   `yaml:"auth"`
	CredentialEnv string              `yaml:"credentialEnv"`
	Tools         []toolregistry.Tool `yaml:"tools"`
	Adapter       string              `yaml:"adapter"`
}

// StaticSource is a skill source the configuration file registers.
type StaticSource struct {
	Name          string `yaml:"name"`
	Kind          string `yaml:"kind"`
	Ref           string `yaml:"ref"`
	URL           string `yaml:"url"`
	Commit        string `yaml:"commit"`
	Digest        string `yaml:"digest"`
	CredentialEnv string `yaml:"credentialEnv"`
}

// TeamTools is a static Team's attachment, by Role or "*".
type TeamTools map[string]toolregistry.RoleTools

// Limit is the number of live runtime entries of each kind per Tenant.
func (t *ToolRegistry) Limit() int {
	if t == nil || t.Max == nil {
		return DefaultMaxToolRegistryEntries
	}
	return *t.Max
}

// Accepting reports whether runtime changes are switched on.
func (t *ToolRegistry) Accepting() bool { return t != nil && t.Enabled }

// Policy is the validation policy the registry applies.
func (t *ToolRegistry) Policy() toolregistry.Policy {
	if t == nil {
		return toolregistry.Policy{}
	}
	return toolregistry.Policy{AllowWriteTools: t.AllowWriteTools, PlainHTTPHosts: t.PlainHTTPHosts, PrivateHosts: t.PrivateHosts}
}

var credentialEnv = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var hostName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,252}[A-Za-z0-9])?$`)

func (f *File) validateToolRegistry() error {
	t := f.ToolRegistry
	if t == nil {
		for _, name := range sortedTeamNames(f.Teams) {
			if f.Teams[name].Tools != nil {
				return fmt.Errorf("teams.%s.tools: needs toolRegistry", name)
			}
		}
		return nil
	}
	if t.Max != nil && (*t.Max < 1 || *t.Max > 1000) {
		return fmt.Errorf("toolRegistry.max: must be between 1 and 1000, got %d", *t.Max)
	}
	for field, hosts := range map[string][]string{"plainHTTPHosts": t.PlainHTTPHosts, "privateHosts": t.PrivateHosts} {
		for i, h := range hosts {
			if !hostName.MatchString(h) {
				return fmt.Errorf("toolRegistry.%s[%d]: %q is not a host name", field, i, h)
			}
		}
	}
	for name, values := range t.RequireClaims {
		if name == "" || len(values) == 0 {
			return fmt.Errorf("toolRegistry.requireClaims: claim %q needs at least one value", name)
		}
	}
	if s := t.StepUp; s != nil && s.MaxAge < 0 {
		return fmt.Errorf("toolRegistry.stepUp.maxAge: must not be negative")
	}
	policy := t.Policy()
	policy.SkipResolve = true
	for _, key := range sortedMapKeys(t.MCPServers) {
		s := t.MCPServers[key]
		if s.Transport == "" {
			s.Transport = toolregistry.TransportStreamableHTTP
		}
		if s.CredentialEnv != "" && !credentialEnv.MatchString(s.CredentialEnv) {
			return fmt.Errorf("toolRegistry.mcpServers.%s.credentialEnv: %q is not an environment variable name", key, s.CredentialEnv)
		}
		in := toolregistry.ServerInput{Key: key, Name: nameOr(s.Name, key), URL: s.URL, Transport: s.Transport, Auth: s.Auth, Tools: s.Tools, Adapter: s.Adapter, HasCredential: s.CredentialEnv != ""}
		if e := policy.CheckServer(context.Background(), in); e != nil {
			return fmt.Errorf("toolRegistry.mcpServers.%s: %s", key, e.Message)
		}
	}
	for _, key := range sortedMapKeys(t.SkillSources) {
		s := t.SkillSources[key]
		if s.CredentialEnv != "" && !credentialEnv.MatchString(s.CredentialEnv) {
			return fmt.Errorf("toolRegistry.skillSources.%s.credentialEnv: %q is not an environment variable name", key, s.CredentialEnv)
		}
		in := toolregistry.SourceInput{Key: key, Name: nameOr(s.Name, key), Kind: s.Kind, Ref: s.Ref, URL: s.URL, Commit: s.Commit, Digest: s.Digest, HasCredential: s.CredentialEnv != ""}
		if e := policy.CheckSource(context.Background(), in); e != nil {
			return fmt.Errorf("toolRegistry.skillSources.%s: %s", key, e.Message)
		}
	}
	return nil
}

func nameOr(name, key string) string {
	if name == "" {
		return key
	}
	return name
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
