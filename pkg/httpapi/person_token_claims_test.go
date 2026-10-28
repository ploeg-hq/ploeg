package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const operatorsGroup = "ploeg-operators"

func operatorsOnly() map[string][]string {
	return map[string][]string{"groups": {operatorsGroup}}
}

func notesAddedFor(t *testing.T, workItemID int64) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = $2`,
		workItemID, store.AuditOperatorNoteAdded).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAPersonTokenWithoutTheRequiredGroupIsRefusedOnAMutatingRouteAndChangesNothing(t *testing.T) {
	f := newRequeueFixture(t)
	f.server.OperatorConfig.Consumers[0].clientIDs = []string{personClient}
	f.server.OperatorConfig.Consumers[0].requiredClaims = operatorsOnly()
	is := newPersonIssuer(t)
	tokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, Audience: personAudience, Now: func() time.Time { return personNow }}, f.server.OperatorConfig.Consumers)
	if err != nil {
		t.Fatal(err)
	}
	f.server.OperatorConfig.PersonTokens = tokens
	live := f.item(t, "5791")
	for name, groups := range map[string]any{"another group": []string{"staff"}, "no groups claim": nil, "a group the required one prefixes": []string{"ploeg-operators-readonly"}} {
		c := is.claims()
		if groups != nil {
			c["groups"] = groups
		}
		w := attributedRequest(f.server, "POST", notesPath(live), is.sign(c), nil, map[string]any{"commandId": "note-" + strings.ReplaceAll(name, " ", "-"), "text": "Refused."})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "person_claim_required") || !strings.Contains(w.Body.String(), `\"groups\"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if n := notesAddedFor(t, live); n != 0 {
		t.Fatalf("refused person tokens recorded %d notes", n)
	}
	c := is.claims()
	c["groups"] = []string{"staff", operatorsGroup}
	w := attributedRequest(f.server, "POST", notesPath(live), is.sign(c), nil, map[string]any{"commandId": "note-member", "text": "Allowed."})
	if w.Code != http.StatusCreated {
		t.Fatalf("a member of the required group: %d %s", w.Code, w.Body)
	}
	want := store.Attribution{Consumer: "workbench", Actor: is.personID(), Proof: ProofProven}
	if got := auditAttribution(t, live, store.AuditOperatorNoteAdded); got != want {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
}

func TestRequiredClaimsHoldForEveryWayAPersonTokenArrives(t *testing.T) {
	rig := newPersonRig(t)
	rig.server.OperatorConfig.Consumers[0].requiredClaims = map[string][]string{"groups": {operatorsGroup}, "scope": {"ploeg:act"}, "roles": {"operator"}}
	with := func(edit func(jwt.MapClaims)) string {
		c := rig.issuer.claims()
		c["groups"] = []string{operatorsGroup}
		c["roles"] = []string{"viewer", "operator"}
		edit(c)
		return rig.issuer.sign(c)
	}
	member := with(func(jwt.MapClaims) {})
	if w := rig.do(t, "POST", member, nil); w.Code != http.StatusNoContent || rig.seen == nil || rig.seen.Proof != ProofProven {
		t.Fatalf("member as the bearer: %d %s", w.Code, w.Body)
	}
	if w := rig.do(t, "POST", rig.token, map[string]string{PersonTokenHeader: member}); w.Code != http.StatusNoContent || rig.seen == nil || rig.seen.Proof != ProofProven {
		t.Fatalf("member next to the credential: %d %s", w.Code, w.Body)
	}
	if w := rig.do(t, "POST", with(func(c jwt.MapClaims) { c["scope"] = "openid ploeg:act profile" }), nil); w.Code != http.StatusNoContent {
		t.Fatalf("scope is space-delimited: %d %s", w.Code, w.Body)
	}
	for name, tc := range map[string]struct {
		bearer  string
		headers map[string]string
		claim   string
	}{
		"bearer without the group":         {with(func(c jwt.MapClaims) { c["groups"] = []string{"staff"} }), nil, "groups"},
		"header without the group":         {rig.token, map[string]string{PersonTokenHeader: with(func(c jwt.MapClaims) { delete(c, "groups") })}, "groups"},
		"the group in another case":        {with(func(c jwt.MapClaims) { c["groups"] = []string{"Ploeg-Operators"} }), nil, "groups"},
		"a custom claim of objects":        {with(func(c jwt.MapClaims) { c["roles"] = []any{map[string]string{"name": "operator"}} }), nil, "roles"},
		"a custom claim of another value":  {with(func(c jwt.MapClaims) { c["roles"] = "viewer" }), nil, "roles"},
		"the scope missing":                {with(func(c jwt.MapClaims) { c["scope"] = "ploeg:read" }), nil, "scope"},
		"the scope as one joined value":    {with(func(c jwt.MapClaims) { c["scope"] = "ploeg:act,ploeg:read" }), nil, "scope"},
		"the group and the scope both off": {with(func(c jwt.MapClaims) { c["groups"] = []string{}; c["scope"] = "" }), nil, "groups"},
	} {
		t.Run(name, func(t *testing.T) {
			w := rig.do(t, "POST", tc.bearer, tc.headers)
			if w.Code != http.StatusForbidden || rig.reached {
				t.Fatalf("accepted: %d %s", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), "person_claim_required") || !strings.Contains(w.Body.String(), `\"`+tc.claim+`\"`) || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("refusal: %s %v", w.Body, w.Header())
			}
		})
	}
	rig.server.OperatorConfig.Consumers[0].requiredClaims = nil
	if w := rig.do(t, "POST", with(func(c jwt.MapClaims) { delete(c, "groups"); delete(c, "roles") }), nil); w.Code != http.StatusNoContent {
		t.Fatalf("a consumer that requires no claim: %d %s", w.Code, w.Body)
	}
}

func TestAnAudienceConsumerRequiresItsClaimsFromConfiguration(t *testing.T) {
	tokens := map[string]string{"WORKBENCH": strings.Repeat("w", 32)}
	consumers, err := ParseOperatorConsumers(`[{"name":"workbench","tokenEnv":"WORKBENCH","teams":["silver"],"execute":true,"audiences":["`+workbenchAudience+`"],"requireClaims":{"groups":["`+operatorsGroup+`"]}}]`,
		func(name string) (string, bool) { token, ok := tokens[name]; return token, ok })
	if err != nil {
		t.Fatal(err)
	}
	is := newPersonIssuer(t)
	global := strings.TrimSuffix(is.url, "application/o/ploeg/")
	personTokens, err := NewPersonTokens(PersonTokenConfig{Issuer: global, JWKSURL: global + "jwks", Now: func() time.Time { return personNow }}, consumers)
	if err != nil {
		t.Fatal(err)
	}
	rig := &audienceRig{issuer: is, globalIssuer: global, server: &Server{OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: personTokens}}}
	c := rig.exchanged(workbenchAudience)
	if w := rig.do(t, is.sign(c), ""); w.Code != http.StatusForbidden || rig.reachedHandle {
		t.Fatalf("an exchanged token without the group: %d %s", w.Code, w.Body)
	}
	c["groups"] = []string{operatorsGroup}
	if w := rig.do(t, is.sign(c), ""); w.Code != http.StatusNoContent || rig.caller != "workbench" || rig.seen == nil || rig.seen.Proof != ProofProven {
		t.Fatalf("an exchanged token with the group: %d %s", w.Code, w.Body)
	}
}

func TestRequireClaimsConfigurationIsValidated(t *testing.T) {
	lookup := func(string) (string, bool) { return strings.Repeat("s", 32), true }
	for name, raw := range map[string]string{
		"without clientIds or audiences": `[{"name":"a","tokenEnv":"T","execute":true,"requireClaims":{"groups":["x"]}}]`,
		"no values":                      `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"groups":[]}}]`,
		"an empty value":                 `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"groups":[""]}}]`,
		"a duplicated value":             `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"groups":["x","x"]}}]`,
		"an empty claim name":            `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"":["x"]}}]`,
		"a claim name with a space":      `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"my groups":["x"]}}]`,
		"a value that is not a string":   `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"groups":[1]}}]`,
		"a single value, not a list":     `[{"name":"a","tokenEnv":"T","clientIds":["c"],"requireClaims":{"groups":"x"}}]`,
	} {
		if _, err := ParseOperatorConsumers(raw, lookup); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	parsed, err := ParseOperatorConsumers(`[{"name":"a","tokenEnv":"T","execute":true,"clientIds":["c"],"requireClaims":{"groups":["x","y"],"department":["platform"]}}]`, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed[0].requiredClaims; len(got) != 2 || !slices.Equal(got["groups"], []string{"x", "y"}) {
		t.Fatalf("requiredClaims = %v", got)
	}
}

func TestConsumersThatLetAnyPersonExecuteAreNamed(t *testing.T) {
	lookup := func(name string) (string, bool) { return strings.Repeat(strings.ToLower(name[:1]), 32), true }
	consumers, err := ParseOperatorConsumers(`[
		{"name":"open","tokenEnv":"OPEN","teams":["*"],"execute":true,"audiences":["ploeg-open"]},
		{"name":"client","tokenEnv":"CLIENT","teams":["*"],"execute":true,"clientIds":["app"]},
		{"name":"guarded","tokenEnv":"GUARDED","teams":["*"],"execute":true,"audiences":["ploeg-guarded"],"requireClaims":{"groups":["ploeg-operators"]}},
		{"name":"reader","tokenEnv":"READER","teams":["*"],"audiences":["ploeg-reader"]},
		{"name":"static","tokenEnv":"STATIC","teams":["*"],"execute":true}
	]`, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got := OperatorConsumersActingForAnyPerson(consumers); !slices.Equal(got, []string{"open", "client"}) {
		t.Fatalf("named %v, want open and client", got)
	}
}
