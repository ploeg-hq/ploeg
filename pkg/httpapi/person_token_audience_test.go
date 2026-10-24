package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	workbenchAudience = "ploeg-workbench"
	otherAudience     = "ploeg-other"
)

type audienceRig struct {
	issuer        *personIssuer
	globalIssuer  string
	server        *Server
	tokens        map[string]string
	caller        string
	seen          *store.Attribution
	reachedHandle bool
}

func audienceConsumers(t *testing.T) ([]OperatorConsumer, map[string]string) {
	t.Helper()
	tokens := map[string]string{"workbench": strings.Repeat("w", 32), "other": strings.Repeat("o", 32), "legacy": strings.Repeat("l", 32)}
	raw, _ := json.Marshal([]map[string]any{
		{"name": "workbench", "tokenEnv": "WORKBENCH", "teams": []string{"silver"}, "execute": true, "audiences": []string{workbenchAudience}},
		{"name": "other", "tokenEnv": "OTHER", "teams": []string{"silver"}, "execute": true, "audiences": []string{otherAudience}},
		{"name": "legacy", "tokenEnv": "LEGACY", "teams": []string{"silver"}, "execute": true, "clientIds": []string{personClient}},
	})
	consumers, err := ParseOperatorConsumers(string(raw), func(name string) (string, bool) {
		token, ok := tokens[strings.ToLower(name)]
		return token, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return consumers, tokens
}

func newAudienceRig(t *testing.T) *audienceRig {
	t.Helper()
	consumers, tokens := audienceConsumers(t)
	is := newPersonIssuer(t)
	global := strings.TrimSuffix(is.url, "application/o/ploeg/")
	personTokens, err := NewPersonTokens(PersonTokenConfig{Issuer: global, Audience: personAudience, JWKSURL: global + "jwks", Now: func() time.Time { return personNow }}, consumers)
	if err != nil {
		t.Fatal(err)
	}
	return &audienceRig{issuer: is, globalIssuer: global, tokens: tokens, server: &Server{OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: personTokens}}}
}

func (rig *audienceRig) exchanged(audience string) jwt.MapClaims {
	return jwt.MapClaims{"iss": rig.globalIssuer, "sub": "person-sub-1", "aud": audience, "azp": audience, "scope": "",
		"iat": personNow.Unix(), "exp": personNow.Add(5 * time.Minute).Unix(), "uid": "x"}
}

func (rig *audienceRig) do(t *testing.T, bearer string, personToken string) *httptest.ResponseRecorder {
	t.Helper()
	rig.caller, rig.seen, rig.reachedHandle = "", nil, false
	h := rig.server.operatorAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.reachedHandle = true
		p, _ := OperatorPrincipalFromContext(r.Context())
		rig.caller = p.Name
		if a, ok := store.AttributionFrom(r.Context()); ok {
			rig.seen = &a
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest("POST", "/api/v1/operator/work-items/1/notes", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	if personToken != "" {
		r.Header.Set(PersonTokenHeader, personToken)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAnExchangedTokenNamingItsAudienceAsAzpBelongsToThatAudiencesConsumer(t *testing.T) {
	rig := newAudienceRig(t)
	c := rig.exchanged(workbenchAudience)
	c["act"] = map[string]any{"sub": "agent-1"}
	token := rig.issuer.sign(c)
	want := store.Attribution{Consumer: "workbench", Actor: operatorclient.PersonID(rig.globalIssuer, "person-sub-1"), Proof: ProofProven, Act: "agent-1"}
	if w := rig.do(t, token, ""); w.Code != http.StatusNoContent || rig.caller != "workbench" || rig.seen == nil || *rig.seen != want {
		t.Fatalf("as the bearer: %d %s caller=%q attribution=%+v", w.Code, w.Body, rig.caller, rig.seen)
	}
	if w := rig.do(t, rig.tokens["workbench"], token); w.Code != http.StatusNoContent || rig.caller != "workbench" || rig.seen == nil || *rig.seen != want {
		t.Fatalf("next to the consumer's credential: %d %s caller=%q attribution=%+v", w.Code, w.Body, rig.caller, rig.seen)
	}
	if w := rig.do(t, rig.issuer.sign(rig.exchanged(otherAudience)), ""); w.Code != http.StatusNoContent || rig.caller != "other" {
		t.Fatalf("the other consumer's audience: %d %s caller=%q", w.Code, w.Body, rig.caller)
	}
}

func TestAnExchangedTokenIsRefusedNextToAnotherConsumersCredential(t *testing.T) {
	rig := newAudienceRig(t)
	token := rig.issuer.sign(rig.exchanged(workbenchAudience))
	for _, consumer := range []string{"other", "legacy"} {
		if w := rig.do(t, rig.tokens[consumer], token); w.Code != http.StatusUnauthorized || rig.reachedHandle || !strings.Contains(w.Body.String(), "another consumer") {
			t.Fatalf("workbench's token with %s's credential: %d %s", consumer, w.Code, w.Body)
		}
	}
}

func TestPersonTokensThatNameNoSingleConsumerAreRefused(t *testing.T) {
	rig := newAudienceRig(t)
	for name, edit := range map[string]func(jwt.MapClaims){
		"two consumers' audiences":            func(c jwt.MapClaims) { c["aud"] = []string{workbenchAudience, otherAudience} },
		"an ID token: no azp, no scope":       func(c jwt.MapClaims) { delete(c, "azp"); delete(c, "scope") },
		"no scope claim":                      func(c jwt.MapClaims) { delete(c, "scope") },
		"another consumer's audience as azp":  func(c jwt.MapClaims) { c["azp"] = otherAudience },
		"another consumer's client id as azp": func(c jwt.MapClaims) { c["azp"] = personClient },
		"a consumer audience as a shared azp": func(c jwt.MapClaims) { c["aud"] = personAudience },
		"an audience nobody names":            func(c jwt.MapClaims) { c["aud"], c["azp"] = "ploeg-stranger", "ploeg-stranger" },
		"the target provider's own issuer":    func(c jwt.MapClaims) { c["iss"] = rig.issuer.url },
	} {
		c := rig.exchanged(workbenchAudience)
		edit(c)
		if w := rig.do(t, rig.issuer.sign(c), ""); w.Code != http.StatusUnauthorized || rig.reachedHandle {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestClientIDTokensStillWorkNextToConsumerAudiences(t *testing.T) {
	rig := newAudienceRig(t)
	c := rig.exchanged(personAudience)
	c["azp"], c["act"] = personClient, map[string]any{"sub": "agent-2"}
	token := rig.issuer.sign(c)
	if w := rig.do(t, token, ""); w.Code != http.StatusNoContent || rig.caller != "legacy" || rig.seen.Act != "agent-2" || rig.seen.Proof != ProofProven {
		t.Fatalf("azp path as the bearer: %d %s caller=%q", w.Code, w.Body, rig.caller)
	}
	if w := rig.do(t, rig.tokens["legacy"], token); w.Code != http.StatusNoContent || rig.caller != "legacy" {
		t.Fatalf("azp path next to the credential: %d %s", w.Code, w.Body)
	}
	if w := rig.do(t, rig.tokens["workbench"], token); w.Code != http.StatusUnauthorized {
		t.Fatalf("azp path next to another consumer's credential: %d %s", w.Code, w.Body)
	}
}

func TestConsumerAudiencesAreValidatedAtStart(t *testing.T) {
	consumers, _ := audienceConsumers(t)
	cfg := PersonTokenConfig{Issuer: "https://id.example/", Audience: personAudience}
	if _, err := NewPersonTokens(cfg, consumers); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func([]OperatorConsumer){
		"an audience listed by two consumers":    func(c []OperatorConsumer) { c[1].audiences = []string{workbenchAudience} },
		"an audience equal to a client id":       func(c []OperatorConsumer) { c[2].clientIDs = []string{workbenchAudience} },
		"an audience equal to Ploeg's audience":  func(c []OperatorConsumer) { c[0].audiences = []string{personAudience} },
		"a consumer's own client id as audience": func(c []OperatorConsumer) { c[0].clientIDs = []string{workbenchAudience} },
	} {
		consumers, _ := audienceConsumers(t)
		edit(consumers)
		if _, err := NewPersonTokens(cfg, consumers); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	consumers[2].clientIDs = nil
	if _, err := NewPersonTokens(PersonTokenConfig{Issuer: cfg.Issuer}, consumers); err != nil {
		t.Fatalf("audiences alone need no shared audience: %v", err)
	}
	consumers[2].clientIDs = []string{personClient}
	if _, err := NewPersonTokens(PersonTokenConfig{Issuer: cfg.Issuer}, consumers); err == nil {
		t.Fatal("clientIds without Ploeg's audience accepted")
	}
	lookup := func(string) (string, bool) { return strings.Repeat("s", 32), true }
	if _, err := ParseOperatorConsumers(`[{"name":"a","tokenEnv":"T","audiences":["x","x"]}]`, lookup); err == nil {
		t.Fatal("duplicated audiences accepted")
	}
	parsed, err := ParseOperatorConsumers(`[{"name":"a","tokenEnv":"T","audiences":["x"]}]`, lookup)
	if err != nil || !OperatorConsumersNameClientIDs(parsed) {
		t.Fatalf("audiences: %v", err)
	}
}
