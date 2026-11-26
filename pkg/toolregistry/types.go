// Package toolregistry is the registry of MCP servers and skill sources of
// ADR-0199: what an installation registers per Tenant, how a Team's Roles
// are given servers and skills by id, and the grant ploegd builds for a
// Run. It holds no credential value; a credential is a secret store
// reference.
package toolregistry

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// Access is what a tool may do: read or write.
type Access string

const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

// Auth kinds of an MCP server.
const (
	AuthNone                   = "none"
	AuthBearer                 = "bearer"
	AuthHeader                 = "header"
	AuthOAuthClientCredentials = "oauth-client-credentials"
)

// Transports of an MCP server.
const (
	TransportStreamableHTTP = "streamable-http"
	TransportStdio          = "stdio"
)

// AdapterGatewayAccessGroups marks a server that is a model gateway's MCP
// endpoint reached with the Run's model key (ADR-0199 section 8).
const AdapterGatewayAccessGroups = "gateway-access-groups"

// Skill source kinds.
const (
	SourceOCI   = "oci"
	SourceHTTPS = "https"
	SourceGit   = "git"
)

// Where a record comes from.
const (
	FromStatic  = "static"
	FromRuntime = "runtime"
)

// AllRoles is the attachment key that applies to every Role of a Team.
const AllRoles = "*"

// Tool is one entry of a server's allowed tool list.
type Tool struct {
	Name   string `json:"name" yaml:"name"`
	Access Access `json:"access" yaml:"access"`
}

// Auth is how the gateway authenticates to a server.
type Auth struct {
	Kind     string   `json:"kind" yaml:"kind"`
	Header   string   `json:"header,omitempty" yaml:"header"`
	TokenURL string   `json:"tokenUrl,omitempty" yaml:"tokenUrl"`
	ClientID string   `json:"clientId,omitempty" yaml:"clientId"`
	Scopes   []string `json:"scopes,omitempty" yaml:"scopes"`
}

// Server is one registered MCP server.
type Server struct {
	ID         string
	Tenant     string
	Key        string
	Name       string
	URL        string
	Transport  string
	Auth       Auth
	Credential secretstore.Ref
	// CredentialEnv names the environment variable that holds a static
	// server's credential in ploegd.
	CredentialEnv string
	Tools         []Tool
	Adapter       string
	Source        string
	State         string
	Revision      int64
	// GatewayAccess is set on the server a static Team's ADR-0078 grant
	// is reported as, and nil on every registered server.
	GatewayAccess *GatewayAccess
}

// HasCredential reports whether a credential is stored or configured.
func (s Server) HasCredential() bool { return !s.Credential.IsZero() || s.CredentialEnv != "" }

// Tool returns the allowed tool named name.
func (s Server) Tool(name string) (Tool, bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// SkillSource is one registered, digest-pinned skill bundle.
type SkillSource struct {
	ID     string
	Tenant string
	Key    string
	Name   string
	Kind   string
	Ref    string
	URL    string
	Commit string
	Digest string
	// BundleDigest is the canonical digest of the unpacked bundle, the key
	// a Run fetches it by.
	BundleDigest string
	Credential   secretstore.Ref
	Skills       []string
	Source       string
	State        string
	Revision     int64
}

// ServerUse is the servers and tools one Role is given.
type ServerUse struct {
	Server string   `json:"server" yaml:"server"`
	Tools  []string `json:"tools,omitempty" yaml:"tools"`
}

// RoleTools is what one Role of a Team is given.
type RoleTools struct {
	MCP    []ServerUse `json:"mcp,omitempty" yaml:"mcp"`
	Skills []string    `json:"skills,omitempty" yaml:"skills"`
}

// Attachment gives the Roles of one Team servers and skills.
type Attachment struct {
	Tenant   string
	Team     string
	TeamID   string
	Roles    map[string]RoleTools
	Source   string
	Revision int64
}

// TeamRole is one Role of a Team as the registry needs it.
type TeamRole struct {
	Name   string
	Writes bool
}

// Team is a Team as the registry resolves it.
type Team struct {
	ID    string
	Key   string
	Roles []TeamRole
	// ToolsInFile reports whether the configuration file declares this Team's
	// tools, which makes the file the authority over its attachment.
	ToolsInFile bool
}

// Role returns the Team's Role named name.
func (t Team) Role(name string) (TeamRole, bool) {
	for _, r := range t.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return TeamRole{}, false
}

// Ref is how an attachment row names a Team: its id when ADR-0198 gave it
// one, else its key.
func (t Team) Ref() string {
	if t.ID != "" {
		return t.ID
	}
	return t.Key
}

// Error is a refusal with the operator API's status and stable code.
type Error struct {
	Status  int
	Code    string
	Message string
	Field   string
	Checks  []Check
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func refuse(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func refuseField(status int, code, field, message string) *Error {
	return &Error{Status: status, Code: code, Message: message, Field: field}
}

// Check is one result of a dry run.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewID returns prefix, an underscore and a lowercase ULID.
func NewID(prefix string, now time.Time, entropy io.Reader) (string, error) {
	var raw [16]byte
	ms := uint64(now.UnixMilli())
	binary.BigEndian.PutUint16(raw[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(raw[2:6], uint32(ms))
	if _, err := io.ReadFull(entropy, raw[6:]); err != nil {
		return "", fmt.Errorf("toolregistry: id entropy: %w", err)
	}
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteByte('_')
	var acc uint64
	var bits uint
	b.WriteByte(crockford[raw[0]>>5])
	acc, bits = uint64(raw[0]&0x1f), 5
	for _, c := range raw[1:] {
		acc = acc<<8 | uint64(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			b.WriteByte(crockford[(acc>>bits)&0x1f])
		}
	}
	return b.String(), nil
}

func newID(prefix string, now time.Time) (string, error) { return NewID(prefix, now, rand.Reader) }
