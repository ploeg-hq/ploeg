package toolregistry

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Limits of ADR-0199.
const (
	MaxToolsPerServer   = 200
	MaxServersPerRole   = 20
	MaxSkillsPerRole    = 50
	MaxToolsPerRole     = 100
	MaxNameLength       = 80
	MaxURLLength        = 2048
	MaxCredentialLength = 16 * 1024
)

var (
	keyPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
	toolPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	headerPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	ociRefPattern  = regexp.MustCompile(`^([A-Za-z0-9.-]+(?::[0-9]{1,5})?)/([a-z0-9]+(?:[._/-][a-z0-9]+)*)@(sha256:[0-9a-f]{64})$`)
	rolePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	idPattern      = regexp.MustCompile(`^(mcp|skl)_[0-9a-hjkmnp-tv-z]{26}$`)
	scopePattern   = regexp.MustCompile(`^[\x21\x23-\x5b\x5d-\x7e]{1,128}$`)
	clientIDFormat = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
	outboundURL    = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[\x21-\x7e]*)?$`)
)

var forbiddenHeaders = map[string]bool{
	"host": true, "cookie": true, "set-cookie": true, "connection": true, "keep-alive": true,
	"proxy-authorization": true, "proxy-authenticate": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true, "content-length": true, "content-type": true,
	"accept": true, "last-event-id": true, "forwarded": true,
}

// ValidKey reports whether key is a registry key.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// IsID reports whether s is a server or skill source id.
func IsID(s string) bool { return idPattern.MatchString(s) }

// Policy is what the installation allows (the configuration file's
// toolRegistry).
type Policy struct {
	AllowWriteTools bool
	PlainHTTPHosts  []string
	PrivateHosts    []string
	// SkipResolve checks a URL's shape only; the address is still checked
	// when a connection is made.
	SkipResolve bool
	// Resolve looks a host up. Nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (p Policy) lists(list []string, host string) bool {
	return slices.ContainsFunc(list, func(h string) bool { return strings.EqualFold(h, host) })
}

func (p Policy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if p.Resolve != nil {
		return p.Resolve(ctx, host)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	return ips, err
}

// ValidName checks a display name.
func ValidName(name string) *Error {
	if name == "" || len([]rune(name)) > MaxNameLength || strings.ContainsFunc(name, unicode.IsControl) {
		return refuseField(400, "invalid_request", "name", "name is 1 to 80 characters without control characters.")
	}
	return nil
}

// PrivateAddress reports whether addr is loopback, link-local, private,
// unspecified, multicast or otherwise not a public unicast address.
func PrivateAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() || addr.IsInterfaceLocalMulticast() ||
		cgnat.Contains(addr) || addr.Is4() && addr.As4()[0] == 0
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// CheckURL refuses a URL that is not https (or http for a listed host),
// carries user info or a fragment, or names a host that resolves to a
// private address the installation did not list.
func (p Policy) CheckURL(ctx context.Context, field, raw string) (*url.URL, *Error) {
	if raw == "" || len(raw) > MaxURLLength {
		return nil, refuseField(422, "invalid_url", field, field+" is an absolute URL of at most 2048 characters.")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return nil, refuseField(422, "invalid_url", field, field+" is not an absolute URL.")
	}
	if u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, refuseField(422, "invalid_url", field, field+" carries no user info and no fragment; a credential goes in credential.")
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
	case "http":
		if !p.lists(p.PlainHTTPHosts, host) {
			return nil, refuseField(422, "host_not_allowed", field, "http is allowed only for a host listed in toolRegistry.plainHTTPHosts; use https.")
		}
	default:
		return nil, refuseField(422, "invalid_url", field, field+" is an https URL.")
	}
	if p.SkipResolve || p.lists(p.PrivateHosts, host) || p.lists(p.PlainHTTPHosts, host) {
		return u, nil
	}
	addrs, err := p.addresses(ctx, host)
	if err != nil {
		return nil, refuseField(422, "host_not_allowed", field, fmt.Sprintf("%s's host does not resolve.", field))
	}
	for _, a := range addrs {
		if PrivateAddress(a) {
			return nil, refuseField(422, "host_not_allowed", field, field+"'s host resolves to a private, loopback or link-local address; list it in toolRegistry.privateHosts to allow it.")
		}
	}
	return u, nil
}

func (p Policy) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	addrs, err := p.resolve(ctx, host)
	if err == nil && len(addrs) == 0 {
		err = fmt.Errorf("no address for %s", host)
	}
	return addrs, err
}

// ServerInput is a server as a request or the file states it.
type ServerInput struct {
	Key           string
	Name          string
	URL           string
	Transport     string
	Auth          Auth
	Tools         []Tool
	Adapter       string
	HasCredential bool
	CredentialLen int
}

// CheckServer validates a server against the installation's policy.
func (p Policy) CheckServer(ctx context.Context, in ServerInput) *Error {
	if !ValidKey(in.Key) {
		return refuseField(400, "invalid_request", "key", "key is a lowercase slug of 1 to 40 characters.")
	}
	if e := ValidName(in.Name); e != nil {
		return e
	}
	switch in.Transport {
	case TransportStreamableHTTP:
	case TransportStdio:
		return refuseField(422, "transport_needs_adapter", "transport", "A stdio server runs in a sandboxed MCP adapter outside the Run pod; register the adapter's streamable-http URL (ADR-0199 section 7).")
	default:
		return refuseField(400, "invalid_request", "transport", "transport is streamable-http.")
	}
	if _, e := p.CheckURL(ctx, "url", in.URL); e != nil {
		return e
	}
	if e := checkAuth(in.Auth, in.Adapter); e != nil {
		return e
	}
	if in.Adapter != "" && in.Adapter != AdapterGatewayAccessGroups {
		return refuseField(400, "invalid_request", "adapter", "adapter is gateway-access-groups or omitted.")
	}
	needs := in.Auth.Kind != AuthNone && in.Adapter == ""
	switch {
	case in.HasCredential && !needs:
		return refuseField(422, "credential_not_allowed", "credential", "This server's auth kind takes no credential.")
	case !in.HasCredential && needs:
		return refuseField(422, "credential_required", "credential", "This server's auth kind needs a credential.")
	case in.CredentialLen > MaxCredentialLength:
		return refuseField(400, "invalid_request", "credential", "A credential is at most 16 KiB.")
	}
	return p.checkTools(in.Tools)
}

func checkAuth(a Auth, adapter string) *Error {
	switch a.Kind {
	case AuthNone, AuthBearer:
		if a.Header != "" || a.TokenURL != "" || a.ClientID != "" || len(a.Scopes) > 0 {
			return refuseField(400, "invalid_request", "auth", "auth of kind none or bearer takes no other field.")
		}
	case AuthHeader:
		if !headerPattern.MatchString(a.Header) || forbiddenHeaders[strings.ToLower(a.Header)] || strings.HasPrefix(strings.ToLower(a.Header), "mcp-") {
			return refuseField(400, "invalid_request", "auth.header", "auth.header names one request header, never Host, Cookie, a hop-by-hop or an Mcp- header.")
		}
		if a.TokenURL != "" || a.ClientID != "" || len(a.Scopes) > 0 {
			return refuseField(400, "invalid_request", "auth", "auth of kind header takes only header.")
		}
	case AuthOAuthClientCredentials:
		if a.Header != "" || !clientIDFormat.MatchString(a.ClientID) || len(a.Scopes) > 20 {
			return refuseField(400, "invalid_request", "auth", "auth of kind oauth-client-credentials takes tokenUrl, clientId and up to 20 scopes.")
		}
		for _, s := range a.Scopes {
			if !scopePattern.MatchString(s) {
				return refuseField(400, "invalid_request", "auth.scopes", "A scope is a printable token.")
			}
		}
		if u, err := url.Parse(a.TokenURL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return refuseField(422, "invalid_url", "auth.tokenUrl", "auth.tokenUrl is an https URL without user info.")
		}
	default:
		return refuseField(400, "invalid_request", "auth.kind", "auth.kind is none, bearer, header or oauth-client-credentials.")
	}
	if adapter == AdapterGatewayAccessGroups && a.Kind != AuthNone {
		return refuseField(400, "invalid_request", "auth.kind", "A gateway-access-groups server is reached with the Run's model key; its auth.kind is none.")
	}
	return nil
}

func (p Policy) checkTools(tools []Tool) *Error {
	if len(tools) == 0 || len(tools) > MaxToolsPerServer {
		return refuseField(400, "invalid_request", "tools", "tools lists 1 to 200 allowed tools.")
	}
	seen := map[string]bool{}
	for _, t := range tools {
		if !toolPattern.MatchString(t.Name) || seen[t.Name] {
			return refuseField(400, "invalid_request", "tools", fmt.Sprintf("tool %q is not a tool name or is listed twice.", t.Name))
		}
		seen[t.Name] = true
		switch t.Access {
		case AccessRead:
		case AccessWrite:
			if !p.AllowWriteTools {
				return refuseField(422, "write_tools_not_allowed", "tools", fmt.Sprintf("tool %q is write; this installation allows only read tools (toolRegistry.allowWriteTools).", t.Name))
			}
		default:
			return refuseField(400, "invalid_request", "tools", fmt.Sprintf("tool %q: access is read or write.", t.Name))
		}
	}
	return nil
}

// SourceInput is a skill source as a request or the file states it.
type SourceInput struct {
	Key           string
	Name          string
	Kind          string
	Ref           string
	URL           string
	Commit        string
	Digest        string
	HasCredential bool
	CredentialLen int
}

// CheckSource validates a skill source.
func (p Policy) CheckSource(ctx context.Context, in SourceInput) *Error {
	if !ValidKey(in.Key) {
		return refuseField(400, "invalid_request", "key", "key is a lowercase slug of 1 to 40 characters.")
	}
	if e := ValidName(in.Name); e != nil {
		return e
	}
	if in.CredentialLen > MaxCredentialLength {
		return refuseField(400, "invalid_request", "credential", "A credential is at most 16 KiB.")
	}
	switch in.Kind {
	case SourceOCI:
		if in.URL != "" || in.Commit != "" || in.Digest != "" {
			return refuseField(400, "invalid_request", "ref", "An oci source names only ref, as registry/repository@sha256:<digest>.")
		}
		m := ociRefPattern.FindStringSubmatch(in.Ref)
		if m == nil {
			return refuseField(422, "digest_required", "ref", "An oci ref is registry/repository@sha256:<64 hex>; a tag is never accepted.")
		}
		_, e := p.CheckURL(ctx, "ref", "https://"+m[1]+"/")
		return e
	case SourceHTTPS:
		if in.Ref != "" || in.Commit != "" {
			return refuseField(400, "invalid_request", "url", "An https source names url and digest.")
		}
		if !digestPattern.MatchString(in.Digest) {
			return refuseField(422, "digest_required", "digest", "digest is sha256:<64 hex> of the archive.")
		}
		_, e := p.CheckURL(ctx, "url", in.URL)
		return e
	case SourceGit:
		if in.Ref != "" {
			return refuseField(400, "invalid_request", "ref", "A git source names url, commit and digest.")
		}
		if !commitPattern.MatchString(in.Commit) {
			return refuseField(422, "digest_required", "commit", "commit is the 40-hex commit id; a branch or tag is never accepted.")
		}
		if !digestPattern.MatchString(in.Digest) {
			return refuseField(422, "digest_required", "digest", "digest is sha256:<64 hex> of the tree's canonical bundle.")
		}
		_, e := p.CheckURL(ctx, "url", in.URL)
		return e
	default:
		return refuseField(400, "invalid_request", "kind", "kind is oci, https or git.")
	}
}

// CheckAttachment validates an attachment against the Team, the servers and
// the sources it names, and returns it with every reference resolved to an
// id.
func CheckAttachment(team Team, roles map[string]RoleTools, servers []Server, sources []SkillSource) (map[string]RoleTools, *Error) {
	if len(roles) == 0 {
		return nil, refuseField(400, "invalid_request", "roles", "roles names at least one Role or \"*\".")
	}
	out := make(map[string]RoleTools, len(roles))
	for name, rt := range roles {
		if name != AllRoles && !rolePattern.MatchString(name) {
			return nil, refuseField(400, "invalid_request", "roles", fmt.Sprintf("%q is not a Role name.", name))
		}
		writes := true
		if name != AllRoles {
			role, ok := team.Role(name)
			if !ok {
				return nil, refuseField(422, "unknown_role", "roles", fmt.Sprintf("Team %s has no Role %q.", team.Key, name))
			}
			writes = role.Writes
		} else {
			writes = everyRoleWrites(team)
		}
		resolved, e := checkRole(name, writes, rt, servers, sources)
		if e != nil {
			return nil, e
		}
		out[name] = resolved
	}
	return out, nil
}

func everyRoleWrites(team Team) bool {
	if len(team.Roles) == 0 {
		return false
	}
	for _, r := range team.Roles {
		if !r.Writes {
			return false
		}
	}
	return true
}

func checkRole(role string, writes bool, rt RoleTools, servers []Server, sources []SkillSource) (RoleTools, *Error) {
	if len(rt.MCP) > MaxServersPerRole || len(rt.Skills) > MaxSkillsPerRole {
		return RoleTools{}, refuseField(429, "limit_reached", "roles", fmt.Sprintf("Role %q: at most 20 servers and 50 skills.", role))
	}
	var out RoleTools
	seenServer, total := map[string]bool{}, 0
	for _, use := range rt.MCP {
		srv, ok := findServer(servers, use.Server)
		if !ok {
			return RoleTools{}, refuseField(404, "unknown_server", "roles", fmt.Sprintf("Role %q names server %q, which is not registered.", role, use.Server))
		}
		if seenServer[srv.ID] {
			return RoleTools{}, refuseField(400, "invalid_request", "roles", fmt.Sprintf("Role %q names server %s twice.", role, srv.Key))
		}
		seenServer[srv.ID] = true
		names := use.Tools
		if len(names) == 0 {
			for _, t := range srv.Tools {
				if t.Access == AccessRead || writes {
					names = append(names, t.Name)
				}
			}
		}
		seenTool := map[string]bool{}
		for _, n := range names {
			t, ok := srv.Tool(n)
			if !ok || seenTool[n] {
				return RoleTools{}, refuseField(422, "unknown_tool", "roles", fmt.Sprintf("Role %q: server %s does not allow tool %q, or it is named twice.", role, srv.Key, n))
			}
			seenTool[n] = true
			if t.Access == AccessWrite && !writes {
				return RoleTools{}, refuseField(422, "write_tool_on_reader", "roles", fmt.Sprintf("Role %q does not write; tool %q of server %s writes.", role, n, srv.Key))
			}
		}
		total += len(names)
		out.MCP = append(out.MCP, ServerUse{Server: srv.ID, Tools: slices.Clone(names)})
	}
	if total > MaxToolsPerRole {
		return RoleTools{}, refuseField(429, "limit_reached", "roles", fmt.Sprintf("Role %q: at most 100 tools in total.", role))
	}
	seenSkill := map[string]bool{}
	for _, ref := range rt.Skills {
		src, ok := findSource(sources, ref)
		if !ok {
			return RoleTools{}, refuseField(404, "unknown_source", "roles", fmt.Sprintf("Role %q names skill source %q, which is not registered.", role, ref))
		}
		if seenSkill[src.ID] {
			return RoleTools{}, refuseField(400, "invalid_request", "roles", fmt.Sprintf("Role %q names skill source %s twice.", role, src.Key))
		}
		seenSkill[src.ID] = true
		out.Skills = append(out.Skills, src.ID)
	}
	return out, nil
}

func findServer(servers []Server, ref string) (Server, bool) {
	for _, s := range servers {
		if s.ID == ref || strings.EqualFold(s.Key, ref) {
			return s, s.State == "" || s.State == "active"
		}
	}
	return Server{}, false
}

func findSource(sources []SkillSource, ref string) (SkillSource, bool) {
	for _, s := range sources {
		if s.ID == ref || strings.EqualFold(s.Key, ref) {
			return s, s.State == "" || s.State == "active"
		}
	}
	return SkillSource{}, false
}

// errOutboundURL is a URL ploegd refuses to call: not http or https, user
// info, or a host that is not a plain name or address.
var errOutboundURL = fmt.Errorf("toolregistry: refused outbound URL")
