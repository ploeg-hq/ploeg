package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
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
	personAudience = "ploeg"
	personClient   = "acting-app"
)

var personNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

type personIssuer struct {
	t   *testing.T
	url string
	key *ecdsa.PrivateKey
}

func newPersonIssuer(t *testing.T) *personIssuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	is := &personIssuer{t: t, key: key}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	is.url = srv.URL + "/application/o/ploeg/"
	mux.HandleFunc("/application/o/ploeg/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": is.url, "jwks_uri": srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := key.PublicKey.Bytes()
		b := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "EC", "kid": "k1", "use": "sig", "crv": "P-256", "x": b(raw[1:33]), "y": b(raw[33:])}}})
	})
	return is
}

func (is *personIssuer) claims() jwt.MapClaims {
	return jwt.MapClaims{"iss": is.url, "sub": "person-sub-1", "aud": personAudience, "azp": personClient,
		"iat": personNow.Unix(), "exp": personNow.Add(5 * time.Minute).Unix(), "scope": "ploeg:act"}
}

func (is *personIssuer) sign(c jwt.MapClaims) string {
	is.t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(is.key)
	if err != nil {
		is.t.Fatal(err)
	}
	return s
}

func (is *personIssuer) personID() string {
	return operatorclient.PersonID(is.url, "person-sub-1")
}

type personRig struct {
	issuer  *personIssuer
	server  *Server
	token   string
	other   string
	seen    *store.Attribution
	proven  *ProvenPerson
	reached bool
}

func newPersonRig(t *testing.T) *personRig {
	t.Helper()
	consumers, token := operatorTestConsumers(t, []string{"silver"}, true)
	consumers[0].clientIDs = []string{personClient}
	others, other := operatorTestConsumers(t, []string{"silver"}, true)
	others[0].Principal.Name = "other"
	others[0].clientIDs = []string{"other-app"}
	consumers = append(consumers, others...)
	is := newPersonIssuer(t)
	tokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience, Now: func() time.Time { return personNow }}, consumers)
	if err != nil {
		t.Fatal(err)
	}
	return &personRig{issuer: is, token: token, other: other, server: &Server{OperatorConfig: OperatorConfig{Consumers: consumers, PersonTokens: tokens}}}
}

func (rig *personRig) do(t *testing.T, method, bearer string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rig.seen, rig.proven, rig.reached = nil, nil, false
	h := rig.server.operatorAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.reached = true
		if a, ok := store.AttributionFrom(r.Context()); ok {
			rig.seen = &a
		}
		if p, ok := ProvenPersonFromContext(r.Context()); ok {
			rig.proven = &p
		}
		if p, _ := OperatorPrincipalFromContext(r.Context()); p.Name != "workbench" {
			t.Errorf("caller = %q, want workbench", p.Name)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(method, "/api/v1/operator/work-items/1/notes", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAPersonTokenAsBearerProvesThePersonAndNamesTheConsumer(t *testing.T) {
	rig := newPersonRig(t)
	w := rig.do(t, "POST", rig.issuer.sign(rig.issuer.claims()), map[string]string{"X-Ploeg-Via": "browser"})
	if w.Code != http.StatusNoContent || rig.seen == nil {
		t.Fatalf("valid person token: %d %s", w.Code, w.Body)
	}
	want := store.Attribution{Consumer: "workbench", Actor: rig.issuer.personID(), Via: "browser", Proof: ProofProven}
	if *rig.seen != want {
		t.Fatalf("attribution = %+v, want %+v", *rig.seen, want)
	}
	if w := rig.do(t, "GET", rig.issuer.sign(rig.issuer.claims()), nil); w.Code != http.StatusNoContent || rig.proven == nil {
		t.Fatalf("a read with a person token: %d %s", w.Code, w.Body)
	}
}

func TestAPersonTokenNextToTheConsumersCredentialProvesThePerson(t *testing.T) {
	rig := newPersonRig(t)
	w := rig.do(t, "POST", rig.token, map[string]string{PersonTokenHeader: rig.issuer.sign(rig.issuer.claims()), "X-Ploeg-Actor": rig.issuer.personID()})
	if w.Code != http.StatusNoContent || rig.seen == nil || rig.seen.Proof != ProofProven || rig.seen.Actor != rig.issuer.personID() {
		t.Fatalf("person token in its header: %d %s %+v", w.Code, w.Body, rig.seen)
	}
}

func TestTheActClaimIsRecorded(t *testing.T) {
	rig := newPersonRig(t)
	c := rig.issuer.claims()
	c["act"] = map[string]any{"sub": "team-bronze", "act": map[string]any{"sub": "earlier"}}
	if w := rig.do(t, "POST", rig.issuer.sign(c), nil); w.Code != http.StatusNoContent || rig.seen == nil || rig.seen.Act != "team-bronze" {
		t.Fatalf("act: %d %s %+v", w.Code, w.Body, rig.seen)
	}
}

func TestHeadersAloneAreAssertedForOneMoreRelease(t *testing.T) {
	rig := newPersonRig(t)
	if w := rig.do(t, "POST", rig.token, map[string]string{"X-Ploeg-Actor": "alice"}); w.Code != http.StatusNoContent || rig.seen == nil {
		t.Fatalf("header attribution: %d %s", w.Code, w.Body)
	}
	if want := (store.Attribution{Consumer: "workbench", Actor: "alice", Proof: ProofAsserted}); *rig.seen != want {
		t.Fatalf("attribution = %+v, want %+v", *rig.seen, want)
	}
}

func TestARefusedPersonTokenChangesNothing(t *testing.T) {
	rig := newPersonRig(t)
	is := rig.issuer
	other := newPersonIssuer(t)
	with := func(edit func(jwt.MapClaims)) string {
		c := is.claims()
		edit(c)
		return is.sign(c)
	}
	for name, tc := range map[string]struct {
		bearer  string
		headers map[string]string
		reason  string
	}{
		"wrong audience": {with(func(c jwt.MapClaims) { c["aud"] = "someone-else" }), nil, "another audience"},
		"expired": {with(func(c jwt.MapClaims) {
			c["iat"], c["exp"] = personNow.Add(-10*time.Minute).Unix(), personNow.Add(-6*time.Minute).Unix()
		}), nil, "expired"},
		"too long a life":   {with(func(c jwt.MapClaims) { c["exp"] = personNow.Add(6 * time.Minute).Unix() }), nil, "lives longer"},
		"unknown azp":       {with(func(c jwt.MapClaims) { c["azp"] = "stranger" }), nil, "client"},
		"bad signature":     {other.sign(is.claims()), nil, "signature"},
		"wrong issuer":      {with(func(c jwt.MapClaims) { c["iss"] = other.url }), nil, "issuer"},
		"no sub":            {with(func(c jwt.MapClaims) { delete(c, "sub") }), nil, "sub"},
		"act without sub":   {with(func(c jwt.MapClaims) { c["act"] = map[string]any{"iss": "x"} }), nil, "act"},
		"another consumer":  {rig.other, map[string]string{PersonTokenHeader: is.sign(is.claims())}, "does not name"},
		"not a jwt":         {rig.token, map[string]string{PersonTokenHeader: "opaque-token-value-that-is-long-enough"}, "not a JWT"},
		"azp as client_id":  {with(func(c jwt.MapClaims) { delete(c, "azp"); c["client_id"] = "stranger" }), nil, "client"},
		"other app's token": {with(func(c jwt.MapClaims) { c["azp"] = "other-app" }), map[string]string{"X-Ploeg-Actor": "x"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := rig.do(t, "POST", tc.bearer, tc.headers)
			if name == "other app's token" {
				if w.Code != http.StatusForbidden || rig.reached {
					t.Fatalf("a header naming someone else beside a proven person: %d %s", w.Code, w.Body)
				}
				return
			}
			if w.Code != http.StatusUnauthorized || rig.reached {
				t.Fatalf("accepted: %d %s", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), tc.reason) || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
				t.Fatalf("refusal does not say %q: %s %v", tc.reason, w.Body, w.Header())
			}
		})
	}
}

func TestAProvenPersonAndAConflictingActorHeaderAreRefused(t *testing.T) {
	rig := newPersonRig(t)
	token := rig.issuer.sign(rig.issuer.claims())
	for _, header := range []string{"X-Ploeg-Actor", "X-Ploeg-Acting-User"} {
		w := rig.do(t, "POST", token, map[string]string{header: "mallory"})
		if w.Code != http.StatusForbidden || rig.reached || !strings.Contains(w.Body.String(), "actor_conflict") {
			t.Fatalf("%s conflict: %d %s", header, w.Code, w.Body)
		}
	}
	if w := rig.do(t, "POST", token, map[string]string{"X-Ploeg-Actor": rig.issuer.personID()}); w.Code != http.StatusNoContent {
		t.Fatalf("a header naming the proven person: %d %s", w.Code, w.Body)
	}
}

func TestAClientNamedByTwoConsumersNeedsTheConsumersCredential(t *testing.T) {
	rig := newPersonRig(t)
	rig.server.OperatorConfig.Consumers[1].clientIDs = []string{personClient}
	token := rig.issuer.sign(rig.issuer.claims())
	if w := rig.do(t, "POST", token, nil); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "more than one consumer") {
		t.Fatalf("ambiguous bearer: %d %s", w.Code, w.Body)
	}
	if w := rig.do(t, "POST", rig.token, map[string]string{PersonTokenHeader: token}); w.Code != http.StatusNoContent || rig.seen.Proof != ProofProven {
		t.Fatalf("credential plus person token: %d %s", w.Code, w.Body)
	}
}

func TestADeploymentWithoutPersonTokensRefusesOne(t *testing.T) {
	rig := newPersonRig(t)
	rig.server.OperatorConfig.PersonTokens = nil
	token := rig.issuer.sign(rig.issuer.claims())
	if w := rig.do(t, "POST", token, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("person token as bearer without configuration: %d", w.Code)
	}
	if w := rig.do(t, "POST", rig.token, map[string]string{PersonTokenHeader: token}); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "accepts no person tokens") {
		t.Fatalf("person token header without configuration: %d %s", w.Code, w.Body)
	}
}

func TestPersonTokenConfigurationIsValidated(t *testing.T) {
	consumers, _ := operatorTestConsumers(t, []string{AllTeams}, true)
	cfg := PersonTokenConfig{Issuer: "https://id.example/application/o/ploeg/", Audience: personAudience}
	if _, err := NewPersonTokens(cfg, consumers); err == nil {
		t.Fatal("person tokens without any consumer client id must be refused")
	}
	consumers[0].clientIDs = []string{personAudience}
	if _, err := NewPersonTokens(cfg, consumers); err == nil {
		t.Fatal("a consumer client id equal to Ploeg's audience must be refused")
	}
	consumers[0].clientIDs = []string{personClient}
	for _, bad := range []PersonTokenConfig{
		{Issuer: cfg.Issuer},
		{Issuer: cfg.Issuer, Audience: personAudience, MaxLifetime: 2 * time.Hour},
		{Issuer: "not a url", Audience: personAudience},
	} {
		if _, err := NewPersonTokens(bad, consumers); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if _, err := NewPersonTokens(cfg, consumers); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseOperatorConsumers(`[{"name":"a","tokenEnv":"T","clientIds":["x","x"]}]`, func(string) (string, bool) { return strings.Repeat("s", 32), true }); err == nil {
		t.Fatal("duplicated client ids accepted")
	}
	parsed, err := ParseOperatorConsumers(`[{"name":"a","tokenEnv":"T","clientIds":["x"]}]`, func(string) (string, bool) { return strings.Repeat("s", 32), true })
	if err != nil || !OperatorConsumersNameClientIDs(parsed) {
		t.Fatalf("clientIds: %v", err)
	}
}

func TestAProvenNoteRecordsTheProvenPersonInTheAudit(t *testing.T) {
	f := newRequeueFixture(t)
	f.server.OperatorConfig.Consumers[0].clientIDs = []string{personClient}
	is := newPersonIssuer(t)
	tokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience, Now: func() time.Time { return personNow }}, f.server.OperatorConfig.Consumers)
	if err != nil {
		t.Fatal(err)
	}
	f.server.OperatorConfig.PersonTokens = tokens
	c := is.claims()
	c["act"] = map[string]any{"sub": "team-bronze"}
	live := f.item(t, "5790")
	w := attributedRequest(f.server, "POST", notesPath(live), is.sign(c), map[string][]string{"X-Ploeg-Via": {"browser"}}, map[string]any{"commandId": "note-proven", "text": "Proven."})
	if w.Code != http.StatusCreated {
		t.Fatalf("note: %d %s", w.Code, w.Body)
	}
	want := store.Attribution{Consumer: "workbench", Actor: is.personID(), Via: "browser", Proof: ProofProven, Act: "team-bronze"}
	if got := auditAttribution(t, live, store.AuditOperatorNoteAdded); got != want {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
	events := eventsByActor(t, f.server, f.token, live, is.personID())
	if len(events) == 0 {
		t.Fatal("events?actor= does not find the proven person")
	}
	if a, _ := events[0].Detail["attribution"].(map[string]any); a["proof"] != ProofProven || a["act"] != "team-bronze" {
		t.Fatalf("event attribution = %v", events[0].Detail["attribution"])
	}
}
