package team

import (
	"strings"
	"testing"

	"github.com/ploeg-hq/ploeg/pkg/spendcap"
)

const validBody = `{"key":"docs-review","charter":"Review docs.",
 "plan":{"rounds":[{"roles":[{"name":"reviewer","cap":2,"harness":"openhands","model":"m1"}]}]},
 "spend":{"cap":{"usd":50,"period":"month"}},"maxRunning":2,
 "may":[{"action":"comment"},{"action":"createWork","effect":"proposed"}],
 "intake":{"admissions":true,"targets":["app"]}}`

func policy() Policy {
	return Policy{
		Pools:        Pools{{Tenant: "default", Harness: "openhands", MaxReplicaCount: 4, Isolation: IsolationProxy}, {Tenant: "default", Harness: "raw", MaxReplicaCount: 2}},
		Tenant:       "default",
		Models:       []string{"m1"},
		MaxRoleCap:   5,
		MayComment:   true,
		TargetExists: func(k string) bool { return k == "app" },
	}
}

func TestAValidManifestGetsTheDefaultNameHint(t *testing.T) {
	m, r := ParseManifest([]byte(validBody))
	if r != nil {
		t.Fatal(r)
	}
	if r := m.Validate(policy()); r != nil {
		t.Fatal(r)
	}
	if m.Name != "reviewer · openhands" {
		t.Fatalf("name %q", m.Name)
	}
}

func TestFieldsThatTouchAPodOrACredentialAreRefusedByName(t *testing.T) {
	for field, body := range map[string]string{
		"image":                           `{"key":"a","image":"x"}`,
		"dind":                            `{"key":"a","dind":true}`,
		"assignees":                       `{"key":"a","assignees":["x"]}`,
		"tools":                           `{"key":"a","tools":["x"]}`,
		"skills":                          `{"key":"a","skills":["x"]}`,
		"plan.rounds[0].roles[0].sandbox": `{"key":"a","plan":{"rounds":[{"roles":[{"name":"b","sandbox":"gvisor"}]}]}}`,
		"spend.cap.currency":              `{"key":"a","spend":{"cap":{"usd":1,"period":"day","currency":"EUR"}}}`,
		"unknownField":                    `{"key":"a","unknownField":1}`,
	} {
		_, r := ParseManifest([]byte(body))
		if r == nil || r.Code != RefuseFieldNotAllowed || r.Field != field {
			t.Errorf("%s: %+v", field, r)
		}
	}
}

func TestEachPolicyCheckRefusesWithItsCode(t *testing.T) {
	cases := map[string]struct {
		edit func(*Manifest)
		code string
	}{
		"key":              {func(m *Manifest) { m.Key = "Docs" }, RefuseInvalidKey},
		"name":             {func(m *Manifest) { m.Name = "a\nb" }, RefuseNameInvalid},
		"charter":          {func(m *Manifest) { m.Charter = strings.Repeat("a", 4001) }, RefuseInvalidRequest},
		"no plan":          {func(m *Manifest) { m.Plan.Rounds = nil }, RefusePlanInvalid},
		"no pool":          {func(m *Manifest) { m.Plan.Rounds[0].Roles[0].Harness = "claude" }, RefuseHarnessNotOffered},
		"unqualified pool": {func(m *Manifest) { m.Plan.Rounds[0].Roles[0].Harness = "raw" }, RefuseHarnessQualified},
		"model":            {func(m *Manifest) { m.Plan.Rounds[0].Roles[0].Model = "m2" }, RefuseModelNotAllowed},
		"role cap":         {func(m *Manifest) { m.Plan.Rounds[0].Roles[0].Cap = 6 }, RefusePlanInvalid},
		"no role cap":      {func(m *Manifest) { m.Plan.Rounds[0].Roles[0].Cap = 0 }, RefusePlanInvalid},
		"no spend cap":     {func(m *Manifest) { m.Spend.Cap = nil }, RefuseSpendRequired},
		"zero spend cap":   {func(m *Manifest) { m.Spend.Cap = &spendcap.Cap{} }, RefuseSpendRequired},
		"maxRunning":       {func(m *Manifest) { m.MaxRunning = 5 }, RefuseInvalidRequest},
		"write":            {func(m *Manifest) { m.May = []Grant{{Action: "write"}} }, RefuseFieldNotAllowed},
		"queued work":      {func(m *Manifest) { m.May = []Grant{{Action: "createWork", Effect: "queued"}} }, RefuseFieldNotAllowed},
		"egress":           {func(m *Manifest) { m.May = []Grant{{Action: "egress", Resource: "pypi"}} }, RefuseFieldNotAllowed},
		"target":           {func(m *Manifest) { m.Intake.Targets = []string{"other"} }, RefuseUnknownTarget},
		"nine roles": {func(m *Manifest) {
			roles := []RuntimeRole{}
			for range 9 {
				roles = append(roles, RuntimeRole{Name: "r", Cap: 1, Harness: "openhands", Model: "m1"})
			}
			m.Plan.Rounds = []RuntimeRound{{Roles: roles}}
		}, RefusePlanInvalid},
	}
	for name, tc := range cases {
		m, r := ParseManifest([]byte(validBody))
		if r != nil {
			t.Fatal(r)
		}
		tc.edit(&m)
		if r := m.Validate(policy()); r == nil || r.Code != tc.code {
			t.Errorf("%s: %+v, want %s", name, r, tc.code)
		}
	}
}

func TestOnlyAProxyPoolIsQualifiedUntilTheGatewayShips(t *testing.T) {
	pools, err := ParsePools(`[{"tenant":"default","harness":"openhands","maxReplicaCount":4,"isolation":"gateway"},
		{"tenant":"default","harness":"acp/opencode","maxReplicaCount":2,"isolation":"proxy"},
		{"tenant":"default","harness":"raw","maxReplicaCount":1,"isolation":""}]`)
	if err != nil {
		t.Fatal(err)
	}
	for harness, want := range map[string]bool{"openhands": false, "acp/opencode": true, "raw": false} {
		p, ok := pools.For("default", harness)
		if !ok || p.Qualified() != want {
			t.Errorf("%s: qualified %v", harness, p.Qualified())
		}
	}
	if p, _ := pools.For("default", "acp/opencode"); p.BootstrapKey() != "pool--default--acp-opencode" {
		t.Errorf("bootstrap key %q", p.BootstrapKey())
	}
	for _, bad := range []string{`[{"tenant":"default","harness":"x","maxReplicaCount":0}]`, `[{"tenant":"a","harness":"x","maxReplicaCount":1},{"tenant":"a","harness":"x","maxReplicaCount":1}]`, `{}`, `[{"tenant":"a","harness":"x","maxReplicaCount":1,"image":"y"}]`} {
		if _, err := ParsePools(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestTheStateDecisionLetsStaticWinThenNamesTheFirstCause(t *testing.T) {
	m, _ := ParseManifest([]byte(validBody))
	in := Installation{
		StaticKeys: map[string]string{}, Accepting: true, Pools: policy().Pools, Models: []string{"m1"},
		Allowances: map[string]spendcap.Cap{"default": {USD: 100, Period: "month"}}, TargetExists: func(string) bool { return true },
	}
	if d := in.Decide("default", "docs-review", m); d.State != StateActive {
		t.Fatalf("%+v", d)
	}
	cases := map[string]struct {
		edit func(*Installation)
		want Decision
	}{
		"static":        {func(i *Installation) { i.StaticKeys = map[string]string{"docs-review": "team_s"}; i.Accepting = false }, Decision{State: StateShadowed, ShadowedBy: "team_s"}},
		"disabled":      {func(i *Installation) { i.Accepting = false }, Decision{State: StateInvalid, InvalidReason: InvalidCreationDisabled}},
		"pool gone":     {func(i *Installation) { i.Pools = nil }, Decision{State: StateInvalid, InvalidReason: InvalidHarnessNotOffered}},
		"unqualified":   {func(i *Installation) { i.Pools = Pools{{Tenant: "default", Harness: "openhands", MaxReplicaCount: 1}} }, Decision{State: StateInvalid, InvalidReason: InvalidHarnessNotQualified}},
		"model":         {func(i *Installation) { i.Models = nil }, Decision{State: StateInvalid, InvalidReason: InvalidModelNotAllowed}},
		"target":        {func(i *Installation) { i.TargetExists = func(string) bool { return false } }, Decision{State: StateInvalid, InvalidReason: InvalidTargetUnavailable}},
		"allowance cut": {func(i *Installation) { i.Allowances = map[string]spendcap.Cap{"default": {USD: 10, Period: "month"}} }, Decision{State: StateInvalid, InvalidReason: InvalidSpendExceedsTenant}},
	}
	for name, tc := range cases {
		copy := in
		tc.edit(&copy)
		if got := copy.Decide("default", "docs-review", m); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}
