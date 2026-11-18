package toolregistry

import (
	"slices"
	"sort"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
)

// ServerGrant is one server a Run may reach through the credential gateway,
// with the tools its Role was given. It names the credential by reference;
// only the gateway ever holds the value.
type ServerGrant struct {
	ID            string            `json:"id"`
	Key           string            `json:"key"`
	URL           string            `json:"url"`
	Transport     string            `json:"transport"`
	Auth          Auth              `json:"auth"`
	Adapter       string            `json:"adapter,omitempty"`
	Credential    secretstore.Ref   `json:"credential"`
	CredentialEnv string            `json:"-"`
	Tools         map[string]Access `json:"tools"`
}

// SkillGrant is one skill bundle a Run installs read-only.
type SkillGrant struct {
	ID     string   `json:"id"`
	Key    string   `json:"key"`
	Digest string   `json:"digest"`
	Skills []string `json:"skills,omitempty"`
}

// Grant is the tools and skills of one Run's Role.
type Grant struct {
	Servers []ServerGrant `json:"servers"`
	Skills  []SkillGrant  `json:"skills"`
}

// ServerKeys are the keys a harness is configured with.
func (g Grant) ServerKeys() []string {
	keys := make([]string, 0, len(g.Servers))
	for _, s := range g.Servers {
		keys = append(keys, s.Key)
	}
	return keys
}

// BuildGrant is the grant of role in the Team the attachment belongs to:
// the "*" entry merged with the Role's own, every tool re-checked against
// the server's current allowed list, and a write tool left out of a Role
// that does not write.
func BuildGrant(att Attachment, role TeamRole, servers []Server, sources []SkillSource) Grant {
	byServer := map[string]map[string]Access{}
	var order []string
	var skillOrder []string
	skillSeen := map[string]bool{}
	for _, entry := range []string{AllRoles, role.Name} {
		rt, ok := att.Roles[entry]
		if !ok {
			continue
		}
		for _, use := range rt.MCP {
			srv, ok := findServer(servers, use.Server)
			if !ok {
				continue
			}
			tools := byServer[srv.ID]
			if tools == nil {
				tools = map[string]Access{}
				byServer[srv.ID] = tools
				order = append(order, srv.ID)
			}
			for _, name := range use.Tools {
				t, ok := srv.Tool(name)
				if !ok || t.Access == AccessWrite && !role.Writes {
					continue
				}
				tools[name] = t.Access
			}
		}
		for _, ref := range rt.Skills {
			if src, ok := findSource(sources, ref); ok && !skillSeen[src.ID] {
				skillSeen[src.ID] = true
				skillOrder = append(skillOrder, src.ID)
			}
		}
	}
	g := Grant{Servers: []ServerGrant{}, Skills: []SkillGrant{}}
	for _, id := range order {
		srv, _ := findServer(servers, id)
		if len(byServer[id]) == 0 {
			continue
		}
		g.Servers = append(g.Servers, ServerGrant{ID: srv.ID, Key: srv.Key, URL: srv.URL, Transport: srv.Transport, Auth: srv.Auth,
			Adapter: srv.Adapter, Credential: srv.Credential, CredentialEnv: srv.CredentialEnv, Tools: byServer[id]})
	}
	sort.SliceStable(g.Servers, func(i, j int) bool { return g.Servers[i].Key < g.Servers[j].Key })
	for _, id := range skillOrder {
		src, _ := findSource(sources, id)
		g.Skills = append(g.Skills, SkillGrant{ID: src.ID, Key: src.Key, Digest: src.BundleDigest, Skills: slices.Clone(src.Skills)})
	}
	sort.SliceStable(g.Skills, func(i, j int) bool { return g.Skills[i].Key < g.Skills[j].Key })
	return g
}
