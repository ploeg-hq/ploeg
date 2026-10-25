package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const (
	confirmClientID     = "ploeg-mcp-confirm"
	confirmClientSecret = "confirm-client-secret"
)

// fakeIssuer is an OpenID Connect provider that signs in whoever it is told
// to, the moment the browser reaches its authorization endpoint.
type fakeIssuer struct {
	t      *testing.T
	key    *rsa.PrivateKey
	url    string
	issuer string

	mu        sync.Mutex
	subject   string
	authAge   time.Duration
	acr       string
	amr       []string
	nonce     string
	onLogin   func()
	authorize []url.Values
	codes     map[string]issuedCode
}

type issuedCode struct {
	query  url.Values
	claims jwt.MapClaims
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{t: t, key: key, subject: "subject-1", acr: "goauthentik.io/providers/oauth2/default", amr: []string{"pwd", "mfa"}, codes: map[string]issuedCode{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	f.issuer = srv.URL + "/application/o/ploeg-confirm/"
	return f
}

func (f *fakeIssuer) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/application/o/ploeg-confirm/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.issuer, "authorization_endpoint": f.url + "/authorize", "token_endpoint": f.url + "/token", "jwks_uri": f.url + "/jwks"})
	case "/jwks":
		b := func(i *big.Int) string { return base64.RawURLEncoding.EncodeToString(i.Bytes()) }
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "c1", "use": "sig", "n": b(f.key.N), "e": b(big.NewInt(int64(f.key.E)))}}})
	case "/authorize":
		f.login(w, r)
	case "/token":
		f.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeIssuer) login(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.authorize = append(f.authorize, q)
	now := time.Now()
	nonce := q.Get("nonce")
	if f.nonce != "" {
		nonce = f.nonce
	}
	claims := jwt.MapClaims{"iss": f.issuer, "sub": f.subject, "aud": q.Get("client_id"), "azp": q.Get("client_id"), "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "auth_time": now.Add(-f.authAge).Unix(), "acr": f.acr, "amr": f.amr, "preferred_username": "ryan"}
	code := randomText(16)
	f.codes[code] = issuedCode{query: q, claims: claims}
	onLogin := f.onLogin
	f.mu.Unlock()
	if onLogin != nil {
		onLogin()
	}
	back, _ := url.Parse(q.Get("redirect_uri"))
	back.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	_ = r.ParseForm()
	f.mu.Lock()
	issued, known := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	challenge := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || id != confirmClientID || secret != confirmClientSecret || !known || r.PostForm.Get("grant_type") != "authorization_code" ||
		r.PostForm.Get("redirect_uri") != issued.query.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(challenge[:]) != issued.query.Get("code_challenge") {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, issued.claims)
	tok.Header["kid"] = "c1"
	signed, err := tok.SignedString(f.key)
	if err != nil {
		f.t.Error(err)
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "opaque", "token_type": "Bearer", "id_token": signed})
}

func (f *fakeIssuer) set(change func(*fakeIssuer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeIssuer) lastAuthorize() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authorize) == 0 {
		return nil
	}
	return f.authorize[len(f.authorize)-1]
}

type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

type signing struct {
	acting
	idp   *fakeIssuer
	clock *clock
}

// newSigningRemote is newActingRemote with PLOEG_MCP_CONFIRMATION_MODE=login
// against a fake issuer. The page clock is the test's, and a re-call waits
// only briefly for the page.
func newSigningRemote(t *testing.T, env map[string]string) signing {
	t.Helper()
	idp := newFakeIssuer(t)
	c := &clock{}
	all := map[string]string{
		confirmationModeEnv:                    modeLogin,
		"PLOEG_MCP_CONFIRMATION_ISSUER":        idp.issuer,
		"PLOEG_MCP_CONFIRMATION_CLIENT_ID":     confirmClientID,
		"PLOEG_MCP_CONFIRMATION_CLIENT_SECRET": confirmClientSecret,
	}
	for k, v := range env {
		all[k] = v
	}
	a := newActingRemoteWith(t, actingOptions{env: all, configure: func(cfg *httpConfig) {
		cfg.confirmations.pages.now = c.now
		cfg.confirmations.pages.wait = 50 * time.Millisecond
	}})
	return signing{acting: a, idp: idp, clock: c}
}

// pageClient connects as subject with a client that declares URL
// elicitation and leaves every round trip to the test.
func (s signing) pageClient(t *testing.T, subject string) *mcp.ClientSession {
	t.Helper()
	caps := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}, URL: &mcp.URLElicitationCapabilities{}}}
	never := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}
	transport := &mcp.StreamableClientTransport{Endpoint: s.url, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{s.tokenFor(subject, []string{"ploeg-stewards"}, "ploeg:read")}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "Test Client", Version: "1.2.3"},
		&mcp.ClientOptions{Capabilities: caps, ElicitationHandler: never, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}}).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type pageRequest struct {
	id      string
	link    string
	message string
	sealed  string
}

func requestPage(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) pageRequest {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return pageRequestOf(t, res)
}

func pageRequestOf(t *testing.T, res *mcp.CallToolResult) pageRequest {
	t.Helper()
	if res.IsError || len(res.InputRequests) != 1 {
		t.Fatalf("want one input request, got %s %+v", textOf(res), res.InputRequests)
	}
	for id, r := range res.InputRequests {
		e, ok := r.(*mcp.ElicitParams)
		if !ok || e.Mode != "url" || e.URL == "" || e.ElicitationID != id || e.RequestedSchema != nil {
			t.Fatalf("want a URL elicitation, got %+v", r)
		}
		return pageRequest{id: id, link: e.URL, message: e.Message, sealed: res.RequestState}
	}
	return pageRequest{}
}

func (p pageRequest) effect() string {
	return strings.TrimSuffix(p.message, " "+pageInstruction)
}

func recall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, p pageRequest, answer *mcp.ElicitResult) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args, RequestState: p.sealed, InputResponses: mcp.InputResponseMap{p.id: answer}})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func opened() *mcp.ElicitResult { return &mcp.ElicitResult{Action: "accept"} }

type browser struct{ *http.Client }

func newBrowser(t *testing.T) browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return browser{&http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (b browser) get(t *testing.T, link string) (int, string) {
	t.Helper()
	resp, err := b.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (b browser) submit(t *testing.T, link, decision string) (int, string) {
	t.Helper()
	resp, err := b.PostForm(link, url.Values{"decision": {decision}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// signIn is a person reading the page and signing in to confirm it.
func (b browser) signIn(t *testing.T, link string) (int, string) {
	t.Helper()
	if status, body := b.get(t, link); status != http.StatusOK || !strings.Contains(body, "Sign in to confirm") {
		t.Fatalf("the page answered %d: %s", status, body)
	}
	return b.submit(t, link, "sign-in")
}

func done(t *testing.T, res *mcp.CallToolResult) actionOut {
	t.Helper()
	var out actionOut
	if res.IsError || res.InputRequests != nil {
		return out
	}
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestAPersonWhoSignsInAfreshAtThePageConfirmsTheAction(t *testing.T) {
	s := newSigningRemote(t, map[string]string{"PLOEG_MCP_CONFIRMATION_ACR_VALUES": "goauthentik.io/providers/oauth2/default", "PLOEG_MCP_CONFIRMATION_AMR": "mfa"})
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-confirm-1")
	args := map[string]any{"ref": id, "note": "Try the smaller fix."}
	p := requestPage(t, cs, "ploeg_retry_work", args)
	if !strings.HasPrefix(p.link, strings.TrimSuffix(s.url, "/mcp")+pagesPath) || !strings.Contains(p.message, "Retry Work Item "+id) {
		t.Fatalf("the input request: %+v", p)
	}

	status, page := newBrowser(t).get(t, p.link)
	for _, want := range []string{"Retry Work Item " + id, "ploeg_retry_work", "Try the smaller fix.", "ryan", signedInPerson} {
		if status != http.StatusOK || !strings.Contains(page, want) {
			t.Fatalf("the page (%d) does not show %q: %s", status, want, page)
		}
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("opening the page acted")
	}

	b := newBrowser(t)
	if status, body := b.signIn(t, p.link); status != http.StatusOK || !strings.Contains(body, "Confirmed.") {
		t.Fatalf("signing in answered %d: %s", status, body)
	}
	q := s.idp.lastAuthorize()
	for k, v := range map[string]string{"prompt": "login", "max_age": "0", "response_type": "code", "code_challenge_method": "S256", "client_id": confirmClientID,
		"acr_values": "goauthentik.io/providers/oauth2/default", "redirect_uri": strings.TrimSuffix(s.url, "/mcp") + callbackPath} {
		if q.Get(k) != v {
			t.Fatalf("the authorization request has %s=%q, want %q", k, q.Get(k), v)
		}
	}
	if q.Get("nonce") == "" || q.Get("state") == "" || q.Get("code_challenge") == "" || !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("the authorization request lacks nonce, state, challenge or openid: %v", q)
	}

	out := done(t, recall(t, cs, "ploeg_retry_work", args, p, opened()))
	if !out.Done || itemState(t, id) != "queued" {
		t.Fatalf("a confirmed action did not act: %+v", out)
	}
	got, _ := lastAttribution(t, id, store.AuditWorkItemRequeued)
	if got.Actor != signedInPerson || got.Confirmation != p.id+":"+confirmed(p.effect()) {
		t.Fatalf("attribution = %+v, want the person and %s", got, p.id)
	}
}

func TestAnMCPClientThatOpensThePageActsAfterThePersonSignsIn(t *testing.T) {
	s := newSigningRemote(t, nil)
	id := stuckItem(t, "page-client-1")
	var asked []string
	b := newBrowser(t)
	openPage := func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.URL)
		if status, body := b.signIn(t, req.Params.URL); status != http.StatusOK {
			t.Errorf("signing in answered %d: %s", status, body)
		}
		return &mcp.ElicitResult{Action: "accept"}, nil
	}
	caps := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}
	transport := &mcp.StreamableClientTransport{Endpoint: s.url, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{s.token([]string{"ploeg-stewards"}, "ploeg:read")}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "Test Client", Version: "1.2.3"}, &mcp.ClientOptions{Capabilities: caps, ElicitationHandler: openPage}).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	out, res := act(t, cs, "ploeg_retry_work", map[string]any{"ref": id})
	if res.IsError || !out.Done || len(asked) != 1 || itemState(t, id) != "queued" {
		t.Fatalf("retry: %s %+v, pages opened %d", textOf(res), out, len(asked))
	}
}

func TestAClientThatSkipsThePageNeverActs(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-skip-1")
	args := map[string]any{"ref": id}
	p := requestPage(t, cs, "ploeg_retry_work", args)
	for name, answer := range map[string]*mcp.ElicitResult{
		"an opened page":     opened(),
		"a ticked form":      {Action: "accept", Content: map[string]any{confirmFieldName: true}},
		"an opened page too": opened(),
	} {
		again := pageRequestOf(t, recall(t, cs, "ploeg_retry_work", args, p, answer))
		if again.link != p.link || again.id != p.id {
			t.Fatalf("%s: the re-call asked for another page: %+v", name, again)
		}
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("a re-call without the page acted")
	}

	selfAnswering := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{confirmFieldName: true}}, nil
	}
	caps := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}
	transport := &mcp.StreamableClientTransport{Endpoint: s.url, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{s.token([]string{"ploeg-stewards"}, "ploeg:read")}}}
	auto, err := mcp.NewClient(&mcp.Implementation{Name: "Agent", Version: "1"}, &mcp.ClientOptions{Capabilities: caps, ElicitationHandler: selfAnswering}).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auto.Close() })
	if res, err := auto.CallTool(context.Background(), &mcp.CallToolParams{Name: "ploeg_retry_work", Arguments: args}); err == nil && !res.IsError {
		t.Fatalf("a client answering its own elicitations acted: %+v", done(t, res))
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("a client answering its own elicitations acted")
	}
}

func TestAClientWithoutURLElicitationIsRefusedInLoginMode(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.client(t, []string{"ploeg-stewards"}, accept())
	id := stuckItem(t, "page-form-only-1")
	if _, res := act(t, cs, "ploeg_stop_work", map[string]any{"ref": id}); !res.IsError || !strings.Contains(textOf(res), "cannot open a confirmation page") {
		t.Fatalf("a form-only client: %s", textOf(res))
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("a form-only client acted")
	}
}

func TestASignInAsAnotherPersonConfirmsNothing(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-sub-1")
	args := map[string]any{"ref": id}
	p := requestPage(t, cs, "ploeg_stop_work", args)
	s.idp.set(func(f *fakeIssuer) { f.subject = "subject-2" })
	if status, body := newBrowser(t).signIn(t, p.link); status != http.StatusForbidden || !strings.Contains(body, "someone other than the person") {
		t.Fatalf("another person's sign-in answered %d: %s", status, body)
	}
	pageRequestOf(t, recall(t, cs, "ploeg_stop_work", args, p, opened()))
	if itemState(t, id) != "needs_human" {
		t.Fatal("another person's sign-in acted")
	}
}

func TestAStaleSignInConfirmsNothing(t *testing.T) {
	for name, c := range map[string]struct {
		env   map[string]string
		stale func(signing)
	}{
		"a sign-in from before the page asked": {map[string]string{"PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE": "10m"}, func(s signing) {
			s.idp.set(func(f *fakeIssuer) { f.authAge = 5 * time.Minute })
		}},
		"a sign-in older than the maximum age": {nil, func(s signing) {
			s.idp.set(func(f *fakeIssuer) { f.onLogin = func() { s.clock.advance(3 * time.Minute) } })
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSigningRemote(t, c.env)
			cs := s.pageClient(t, "subject-1")
			id := stuckItem(t, "page-stale-"+strings.ReplaceAll(name, " ", "-"))
			args := map[string]any{"ref": id}
			p := requestPage(t, cs, "ploeg_stop_work", args)
			c.stale(s)
			if status, body := newBrowser(t).signIn(t, p.link); status != http.StatusForbidden || !strings.Contains(body, "did not sign you in afresh") {
				t.Fatalf("a stale sign-in answered %d: %s", status, body)
			}
			pageRequestOf(t, recall(t, cs, "ploeg_stop_work", args, p, opened()))
			if itemState(t, id) != "needs_human" {
				t.Fatal("a stale sign-in acted")
			}
		})
	}
}

func TestASignInWithoutTheRequiredMethodConfirmsNothing(t *testing.T) {
	for name, c := range map[string]struct {
		env    map[string]string
		issuer func(*fakeIssuer)
	}{
		"a password where mfa is required":          {map[string]string{"PLOEG_MCP_CONFIRMATION_AMR": "mfa"}, func(f *fakeIssuer) { f.amr = []string{"pwd"} }},
		"hwk alone where hwk and user are required": {map[string]string{"PLOEG_MCP_CONFIRMATION_AMR": "hwk user"}, func(f *fakeIssuer) { f.amr = []string{"hwk"} }},
		"another acr than the required one":         {map[string]string{"PLOEG_MCP_CONFIRMATION_ACR_VALUES": "phr phrh"}, func(f *fakeIssuer) { f.acr = "goauthentik.io/providers/oauth2/default" }},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSigningRemote(t, c.env)
			cs := s.pageClient(t, "subject-1")
			id := stuckItem(t, "page-method-"+strings.ReplaceAll(name, " ", "-"))
			args := map[string]any{"ref": id}
			p := requestPage(t, cs, "ploeg_stop_work", args)
			s.idp.set(c.issuer)
			if status, body := newBrowser(t).signIn(t, p.link); status != http.StatusForbidden || !strings.Contains(body, "did not use the method") {
				t.Fatalf("a weak sign-in answered %d: %s", status, body)
			}
			pageRequestOf(t, recall(t, cs, "ploeg_stop_work", args, p, opened()))
			if itemState(t, id) != "needs_human" {
				t.Fatal("a weak sign-in acted")
			}
		})
	}
}

func TestASignInStartedInAnotherBrowserConfirmsNothing(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-browser-1")
	args := map[string]any{"ref": id}
	p := requestPage(t, cs, "ploeg_stop_work", args)
	agent := newBrowser(t)
	agent.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := agent.PostForm(p.link, url.Values{"decision": {"sign-in"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	signInURL := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(signInURL, s.idp.url+"/authorize") {
		t.Fatalf("starting the sign-in answered %d %q", resp.StatusCode, signInURL)
	}
	if status, body := newBrowser(t).get(t, signInURL); status != http.StatusForbidden || !strings.Contains(body, "not started in this browser") {
		t.Fatalf("a sign-in finished in another browser answered %d: %s", status, body)
	}
	pageRequestOf(t, recall(t, cs, "ploeg_stop_work", args, p, opened()))
	if itemState(t, id) != "needs_human" {
		t.Fatal("a sign-in finished in another browser acted")
	}
}

func TestAnExpiredPageConfirmsAndActsNever(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-expired-1")
	args := map[string]any{"ref": id}
	p := requestPage(t, cs, "ploeg_stop_work", args)
	if status, _ := newBrowser(t).signIn(t, p.link); status != http.StatusOK {
		t.Fatalf("signing in answered %d", status)
	}
	s.clock.advance(confirmWindow + time.Second)
	if res := recall(t, cs, "ploeg_stop_work", args, p, opened()); !res.IsError || !strings.Contains(textOf(res), "no longer open") {
		t.Fatalf("an expired page: %s", textOf(res))
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("an expired page acted")
	}

	late := requestPage(t, cs, "ploeg_stop_work", args)
	s.clock.advance(confirmWindow + time.Second)
	if status, body := newBrowser(t).get(t, late.link); status != http.StatusNotFound || strings.Contains(body, "Sign in to confirm") {
		t.Fatalf("an expired page still offers a sign-in (%d): %s", status, body)
	}
}

func TestAPageConfirmationActsOnce(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-replay-1")
	args := map[string]any{"ref": id}
	p := requestPage(t, cs, "ploeg_retry_work", args)
	if status, _ := newBrowser(t).signIn(t, p.link); status != http.StatusOK {
		t.Fatalf("signing in answered %d", status)
	}
	if out := done(t, recall(t, cs, "ploeg_retry_work", args, p, opened())); !out.Done {
		t.Fatalf("the confirmed call did not act: %+v", out)
	}
	res := recall(t, cs, "ploeg_retry_work", args, p, opened())
	if !res.IsError || (!strings.Contains(textOf(res), "already used") && !strings.Contains(textOf(res), "no longer open")) {
		t.Fatalf("a replayed page confirmation: %s", textOf(res))
	}
	if status, _ := newBrowser(t).get(t, p.link); status != http.StatusNotFound {
		t.Fatalf("a used page still answers %d", status)
	}
	if _, n := lastAttribution(t, id, store.AuditWorkItemRequeued); n != 1 {
		t.Fatalf("requeued %d times", n)
	}
}

func TestADeclinedPageChangesNothing(t *testing.T) {
	s := newSigningRemote(t, nil)
	cs := s.pageClient(t, "subject-1")
	id := stuckItem(t, "page-decline-1")
	args := map[string]any{"ref": id}
	p := requestPage(t, cs, "ploeg_stop_work", args)
	if status, body := newBrowser(t).submit(t, p.link, "decline"); status != http.StatusOK || !strings.Contains(body, "Declined") {
		t.Fatalf("declining answered %d: %s", status, body)
	}
	if out := done(t, recall(t, cs, "ploeg_stop_work", args, p, opened())); out.Done || out.Summary != notConfirmed {
		t.Fatalf("a declined page: %+v", out)
	}
	if itemState(t, id) != "needs_human" {
		t.Fatal("a declined page acted")
	}
}

func loginEnv() map[string]string {
	return map[string]string{
		"PLOEG_URL":                            "http://ploeg:8080",
		"PLOEG_MCP_RESOURCE":                   "https://mcp.example/mcp",
		"PLOEG_MCP_ISSUER":                     "https://id.example/application/o/ploeg-mcp/",
		"PLOEG_MCP_CLIENT_IDS":                 "ploeg-mcp",
		"PLOEG_MCP_GROUPS":                     `[{"group":"g","tokenEnv":"READERS_TOKEN","execute":true}]`,
		"READERS_TOKEN":                        strings.Repeat("a", 64),
		confirmationKeyEnv:                     strings.Repeat("k", minConfirmationKeyBytes),
		confirmationModeEnv:                    modeLogin,
		"PLOEG_MCP_CONFIRMATION_CLIENT_ID":     confirmClientID,
		"PLOEG_MCP_CONFIRMATION_CLIENT_SECRET": confirmClientSecret,
	}
}

func loadEnv(env map[string]string) (*httpConfig, error) {
	return loadHTTPConfig(func(k string) string { return env[k] }, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
}

func TestLoginModeLoadsWithAConfidentialClient(t *testing.T) {
	cfg, err := loadEnv(loginEnv())
	if err != nil || cfg.confirmations.pages == nil || cfg.confirmMode != modeLogin || len(cfg.warnings) != 0 {
		t.Fatalf("login mode: %v", err)
	}
	if cfg.confirmations.pages.origin.String() != "https://mcp.example" || cfg.confirmations.pages.maxAge != defaultLoginMaxAge || cfg.confirmations.window != confirmWindow {
		t.Fatalf("login mode defaults: %+v", cfg.confirmations.pages)
	}
}

func TestLoginModeConfigurationIsRefusedWhenIncomplete(t *testing.T) {
	for name, change := range map[string][2]string{
		"an unknown mode":                    {confirmationModeEnv, "yes"},
		"no confirmation client id":          {"PLOEG_MCP_CONFIRMATION_CLIENT_ID", ""},
		"no confirmation client secret":      {"PLOEG_MCP_CONFIRMATION_CLIENT_SECRET", ""},
		"the MCP client as the confirmer":    {"PLOEG_MCP_CONFIRMATION_CLIENT_ID", "ploeg-mcp"},
		"a confirmation issuer with a query": {"PLOEG_MCP_CONFIRMATION_ISSUER", "https://id.example/?a=b"},
		"a page over plain http":             {"PLOEG_MCP_RESOURCE", "http://mcp.example/mcp"},
		"a login max age too long":           {"PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE", "1h"},
		"a login max age too short":          {"PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE", "1s"},
		"a confirmation TTL too long":        {"PLOEG_MCP_CONFIRMATION_TTL", "2h"},
		"a confirmation TTL too short":       {"PLOEG_MCP_CONFIRMATION_TTL", "10s"},
	} {
		env := loginEnv()
		env[change[0]] = change[1]
		if _, err := loadEnv(env); err == nil {
			t.Errorf("%s: want an error", name)
		} else if strings.Contains(err.Error(), confirmClientSecret) {
			t.Errorf("%s: the error leaks the client secret: %v", name, err)
		}
	}
}

func TestAnUnsetModeKeepsClientConfirmationsAndWarnsOnlyForExecuteGroups(t *testing.T) {
	env := loginEnv()
	delete(env, confirmationModeEnv)
	cfg, err := loadEnv(env)
	if err != nil || cfg.confirmMode != modeElicitation || cfg.confirmations.pages != nil || len(cfg.warnings) != 1 || !strings.HasPrefix(cfg.warnings[0], "DEPRECATED") {
		t.Fatalf("unset mode with an execute group: %v %+v", err, cfg)
	}
	env[confirmationModeEnv] = modeElicitation
	if cfg, err = loadEnv(env); err != nil || cfg.confirmMode != modeElicitation || len(cfg.warnings) != 0 {
		t.Fatalf("explicit elicitation: %v", err)
	}
	delete(env, confirmationModeEnv)
	env["PLOEG_MCP_GROUPS"] = `[{"group":"g","tokenEnv":"READERS_TOKEN"}]`
	if cfg, err = loadEnv(env); err != nil || len(cfg.warnings) != 0 {
		t.Fatalf("read-only groups: %v %v", err, cfg.warnings)
	}
}
