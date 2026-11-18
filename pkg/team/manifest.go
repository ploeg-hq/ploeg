package team

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/plan"
	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

// Bounds of a runtime Team's manifest (ADR-0198 sections 2 and 12).
const (
	MaxCharterLength = 4000
	MaxRuntimeRoles  = 8
	MaxRuntimeRounds = 5
)

// Manifest is the body of a runtime Team: ADR-0110's manifest restricted to
// fields that change no pod and no credential.
type Manifest struct {
	Key        string        `json:"key"`
	Name       string        `json:"name,omitempty"`
	Charter    string        `json:"charter,omitempty"`
	Plan       RuntimePlan   `json:"plan"`
	Spend      RuntimeSpend  `json:"spend"`
	MaxRunning int           `json:"maxRunning,omitempty"`
	May        []Grant       `json:"may,omitempty"`
	Intake     RuntimeIntake `json:"intake"`
	Sponsor    string        `json:"sponsor,omitempty"`
}

// RuntimePlan is a runtime Team's Rounds of Roles.
type RuntimePlan struct {
	Pool         float64        `json:"pool,omitempty"`
	Rounds       []RuntimeRound `json:"rounds"`
	MaxFixRounds int            `json:"maxFixRounds,omitempty"`
}

// RuntimeRound is one Round of a runtime plan.
type RuntimeRound struct {
	Roles []RuntimeRole `json:"roles"`
}

// RuntimeRole is one Role of a runtime Team, with the Harness and model it
// runs on.
type RuntimeRole struct {
	Name    string  `json:"name"`
	Writes  bool    `json:"writes,omitempty"`
	Cap     float64 `json:"cap,omitempty"`
	Planner bool    `json:"planner,omitempty"`
	Shadow  bool    `json:"shadow,omitempty"`
	Harness string  `json:"harness"`
	Model   string  `json:"model"`
}

// RuntimeSpend is a runtime Team's spend: its cap per period, required.
type RuntimeSpend struct {
	Cap *spendcap.Cap `json:"cap"`
}

// Grant is one entry of a Team's may list (ADR-0110).
type Grant struct {
	Action   string `json:"action"`
	Resource string `json:"resource,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

// RuntimeIntake is how work reaches a runtime Team.
type RuntimeIntake struct {
	Admissions bool     `json:"admissions,omitempty"`
	Targets    []string `json:"targets,omitempty"`
}

// Refusal is why a manifest is not accepted: an error code of ADR-0198
// section 3, the field it names and a message.
type Refusal struct {
	Code    string
	Field   string
	Message string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

// Refusal codes of a manifest.
const (
	RefuseFieldNotAllowed   = "field_not_allowed"
	RefuseInvalidKey        = "key_invalid"
	RefuseNameInvalid       = "name_invalid"
	RefusePlanInvalid       = "plan_invalid"
	RefuseHarnessNotOffered = "harness_not_offered"
	RefuseHarnessQualified  = "harness_not_qualified"
	RefuseModelNotAllowed   = "model_not_allowed"
	RefuseUnknownTarget     = "unknown_target"
	RefuseSpendRequired     = "spend_required"
	RefuseInvalidRequest    = "invalid_request"
)

var refusedFields = []string{"image", "toolchains", "dind", "sandbox", "runtimeClassName", "claudePlugins",
	"claudeEnvironment", "litellmTeamId", "gatewayTeam", "mcp", "mcpGroups", "tools", "skills", "assignees", "repository", "repo"}

var allowedFields = map[string][]string{
	"":       {"key", "name", "charter", "plan", "spend", "maxRunning", "may", "intake", "sponsor"},
	"plan":   {"pool", "rounds", "maxFixRounds"},
	"round":  {"roles"},
	"role":   {"name", "writes", "cap", "planner", "shadow", "harness", "model"},
	"spend":  {"cap"},
	"cap":    {"usd", "period"},
	"grant":  {"action", "resource", "effect"},
	"intake": {"admissions", "targets"},
}

// ParseManifest decodes a runtime Team's body. A field that changes a pod,
// a credential or a network path, and any unknown field, is refused by
// name, never ignored.
func ParseManifest(raw []byte) (Manifest, *Refusal) {
	var tree map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil || tree == nil {
		return Manifest{}, &Refusal{Code: RefuseInvalidRequest, Message: "A Team is one JSON object."}
	}
	if field := firstUnallowed(tree); field != "" {
		return Manifest{}, &Refusal{Code: RefuseFieldNotAllowed, Field: field,
			Message: fmt.Sprintf("A runtime Team may not set %s: it would change a pod, a credential or a network path, or it is not a field of a runtime Team.", field)}
	}
	var m Manifest
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&m); err != nil {
		return Manifest{}, &Refusal{Code: RefuseInvalidRequest, Message: "The Team does not match the runtime Team shape: " + err.Error()}
	}
	return m, nil
}

func firstUnallowed(tree map[string]any) string {
	var found []string
	check := func(path, kind string, obj map[string]any) {
		for k := range obj {
			if slices.Contains(refusedFields, k) || !slices.Contains(allowedFields[kind], k) {
				found = append(found, strings.TrimPrefix(path+"."+k, "."))
			}
		}
	}
	check("", "", tree)
	if p, ok := tree["plan"].(map[string]any); ok {
		check("plan", "plan", p)
		if rounds, ok := p["rounds"].([]any); ok {
			for i, r := range rounds {
				round, ok := r.(map[string]any)
				if !ok {
					continue
				}
				check(fmt.Sprintf("plan.rounds[%d]", i), "round", round)
				roles, _ := round["roles"].([]any)
				for j, ro := range roles {
					if role, ok := ro.(map[string]any); ok {
						check(fmt.Sprintf("plan.rounds[%d].roles[%d]", i, j), "role", role)
					}
				}
			}
		}
	}
	if sp, ok := tree["spend"].(map[string]any); ok {
		check("spend", "spend", sp)
		if c, ok := sp["cap"].(map[string]any); ok {
			check("spend.cap", "cap", c)
		}
	}
	if in, ok := tree["intake"].(map[string]any); ok {
		check("intake", "intake", in)
	}
	if may, ok := tree["may"].([]any); ok {
		for i, g := range may {
			if grant, ok := g.(map[string]any); ok {
				check(fmt.Sprintf("may[%d]", i), "grant", grant)
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[0]
}

// Policy is what the installation lets a runtime Team choose.
type Policy struct {
	Pools        Pools
	Tenant       string
	Models       []string
	MaxRoleCap   float64
	MayWrite     bool
	MayComment   bool
	Egress       []string
	TargetExists func(key string) bool
}

// Validate checks m against the installation's policy, in the order a
// person fixes things: identity, plan, harnesses and models, spend,
// permissions, intake. It fills a missing name with the default hint.
func (m *Manifest) Validate(p Policy) *Refusal {
	if !ValidRuntimeKey(m.Key) {
		return &Refusal{Code: RefuseInvalidKey, Field: "key", Message: "A runtime Team's key is 1 to 40 lowercase letters, digits and inner hyphens."}
	}
	if m.Name == "" {
		m.Name = DefaultName(m.Key, m.roleHarnesses())
	}
	if err := ValidateName(m.Name); err != nil {
		return &Refusal{Code: RefuseNameInvalid, Field: "name", Message: err.Error() + "."}
	}
	if utf8.RuneCountInString(m.Charter) > MaxCharterLength {
		return &Refusal{Code: RefuseInvalidRequest, Field: "charter", Message: fmt.Sprintf("A charter is at most %d characters.", MaxCharterLength)}
	}
	if r := m.validatePlan(p); r != nil {
		return r
	}
	if m.Spend.Cap == nil || !m.Spend.Cap.Capped() || m.Spend.Cap.Validate() != nil {
		return &Refusal{Code: RefuseSpendRequired, Field: "spend.cap", Message: "A runtime Team needs spend.cap: usd above 0 and a period of day, week or month."}
	}
	if m.MaxRunning < 0 {
		return &Refusal{Code: RefuseInvalidRequest, Field: "maxRunning", Message: "maxRunning is 0 (the pool's limit) or more."}
	}
	for _, role := range m.Roles() {
		if pool, ok := p.Pools.For(p.Tenant, role.Harness); ok && m.MaxRunning > pool.MaxReplicaCount {
			return &Refusal{Code: RefuseInvalidRequest, Field: "maxRunning", Message: fmt.Sprintf("maxRunning is at most the %s pool's %d replicas.", role.Harness, pool.MaxReplicaCount)}
		}
	}
	if r := m.validateMay(p); r != nil {
		return r
	}
	for i, key := range m.Intake.Targets {
		if p.TargetExists == nil || !p.TargetExists(key) {
			return &Refusal{Code: RefuseUnknownTarget, Field: fmt.Sprintf("intake.targets[%d]", i), Message: "No Work Target " + key + " is registered."}
		}
	}
	if m.Sponsor != "" && !sponsorID.MatchString(m.Sponsor) {
		return &Refusal{Code: RefuseInvalidRequest, Field: "sponsor", Message: "sponsor is a person id."}
	}
	return nil
}

func (m *Manifest) validatePlan(p Policy) *Refusal {
	roles := m.Roles()
	if len(m.Plan.Rounds) == 0 || len(roles) == 0 {
		return &Refusal{Code: RefusePlanInvalid, Field: "plan", Message: "A runtime Team needs a plan with at least one Round."}
	}
	if len(m.Plan.Rounds) > MaxRuntimeRounds || len(roles) > MaxRuntimeRoles {
		return &Refusal{Code: RefusePlanInvalid, Field: "plan", Message: fmt.Sprintf("A runtime Team has at most %d Roles in %d Rounds.", MaxRuntimeRoles, MaxRuntimeRounds)}
	}
	tp, err := m.TeamPlan()
	if err != nil {
		return &Refusal{Code: RefusePlanInvalid, Field: "plan", Message: err.Error()}
	}
	if err := plan.Validate(tp); err != nil {
		return &Refusal{Code: RefusePlanInvalid, Field: "plan", Message: err.Error()}
	}
	for i, role := range roles {
		field := fmt.Sprintf("plan.roles[%d]", i)
		pool, offered := p.Pools.For(p.Tenant, role.Harness)
		if !offered {
			return &Refusal{Code: RefuseHarnessNotOffered, Field: field + ".harness", Message: "No worker pool runs Harness " + role.Harness + " for runtime Teams of this Tenant."}
		}
		if !pool.Qualified() {
			return &Refusal{Code: RefuseHarnessQualified, Field: field + ".harness", Message: "Harness " + role.Harness + " is not qualified for credential isolation, so no runtime Team may use it."}
		}
		if !slices.Contains(p.Models, role.Model) {
			return &Refusal{Code: RefuseModelNotAllowed, Field: field + ".model", Message: "Model " + role.Model + " is not in teamCreation.models."}
		}
		if math.IsNaN(role.Cap) || math.IsInf(role.Cap, 0) || role.Cap <= 0 {
			return &Refusal{Code: RefusePlanInvalid, Field: field + ".cap", Message: "A runtime Role needs a per-Run cap above 0."}
		}
		if p.MaxRoleCap > 0 && role.Cap > p.MaxRoleCap {
			return &Refusal{Code: RefusePlanInvalid, Field: field + ".cap", Message: fmt.Sprintf("A Role's cap is at most teamCreation.maxRoleCap (%.2f).", p.MaxRoleCap)}
		}
	}
	return nil
}

func (m *Manifest) validateMay(p Policy) *Refusal {
	for i, g := range m.May {
		field := fmt.Sprintf("may[%d]", i)
		switch g.Action {
		case "write":
			if !p.MayWrite {
				return &Refusal{Code: RefuseFieldNotAllowed, Field: field, Message: "teamCreation.may does not let a runtime Team write."}
			}
		case "comment":
			if !p.MayComment {
				return &Refusal{Code: RefuseFieldNotAllowed, Field: field, Message: "teamCreation.may does not let a runtime Team comment."}
			}
		case "createWork":
			if g.Effect != "proposed" {
				return &Refusal{Code: RefuseFieldNotAllowed, Field: field, Message: "A runtime Team may create work only as proposed, never queued."}
			}
		case "egress":
			if !slices.Contains(p.Egress, g.Resource) {
				return &Refusal{Code: RefuseFieldNotAllowed, Field: field, Message: "Egress " + g.Resource + " is not an allowlist the operator declared in teamCreation.may.egress."}
			}
		default:
			return &Refusal{Code: RefuseFieldNotAllowed, Field: field, Message: "A runtime Team may grant only write, comment, createWork and egress."}
		}
	}
	return nil
}

// Roles returns every Role of the plan in Round order.
func (m *Manifest) Roles() []RuntimeRole {
	var out []RuntimeRole
	for _, r := range m.Plan.Rounds {
		out = append(out, r.Roles...)
	}
	return out
}

// Harnesses returns the distinct Harnesses the plan runs on.
func (m *Manifest) Harnesses() []string {
	var out []string
	for _, r := range m.Roles() {
		if !slices.Contains(out, r.Harness) {
			out = append(out, r.Harness)
		}
	}
	return out
}

func (m *Manifest) roleHarnesses() []RoleHarness {
	var out []RoleHarness
	for _, r := range m.Roles() {
		out = append(out, RoleHarness{Role: r.Name, Harness: r.Harness})
	}
	return out
}

// TeamPlan is the manifest's plan in the shape the Shift engine reads.
func (m *Manifest) TeamPlan() (plan.TeamPlan, error) {
	type role struct {
		Name    string  `json:"name"`
		Writes  bool    `json:"writes"`
		Cap     float64 `json:"cap"`
		Planner bool    `json:"planner,omitempty"`
		Shadow  bool    `json:"shadow,omitempty"`
	}
	type round struct {
		Roles []role `json:"roles"`
	}
	shape := struct {
		Pool         float64 `json:"pool"`
		Rounds       []round `json:"rounds"`
		MaxFixRounds int     `json:"maxFixRounds"`
	}{Pool: m.Plan.Pool, MaxFixRounds: m.Plan.MaxFixRounds}
	for _, r := range m.Plan.Rounds {
		var out round
		for _, ro := range r.Roles {
			out.Roles = append(out.Roles, role{Name: ro.Name, Writes: ro.Writes, Cap: ro.Cap, Planner: ro.Planner, Shadow: ro.Shadow})
		}
		shape.Rounds = append(shape.Rounds, out)
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		return plan.TeamPlan{}, err
	}
	var tp plan.TeamPlan
	if err := json.Unmarshal(raw, &tp); err != nil {
		return plan.TeamPlan{}, err
	}
	return tp, nil
}

// SameBody reports whether two manifests ask for the same Team.
func SameBody(a, b Manifest) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return bytes.Equal(ra, rb)
}
