package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
)

const (
	confirmationModeEnv   = "PLOEG_MCP_CONFIRMATION_MODE"
	modeLogin             = "login"
	modeElicitation       = "elicitation"
	pagesPath             = "/confirmations/"
	callbackPath          = pagesPath + "callback"
	cookiePrefix          = "ploeg_confirm_"
	maxOpenPages          = 10_000
	defaultLoginMaxAge    = 2 * time.Minute
	authTimeLeeway        = 30 * time.Second
	defaultPageWait       = 45 * time.Second
	pageInstruction       = "Open the link, check the action and sign in to confirm it; ploeg-mcp acts only after you sign in there."
	cannotOpenPage        = "This client cannot open a confirmation page (MCP URL elicitation), so ploeg-mcp did nothing. Use an MCP client that declares URL elicitation, or act through Ploeg's operator API or a consumer application."
	deprecatedDefaultMode = "DEPRECATED: a group in PLOEG_MCP_GROUPS is marked execute and PLOEG_MCP_CONFIRMATION_MODE is unset, so ploeg-mcp acts on a confirmation the client answers itself (ADR-0152). Set it to login to prove a person confirmed each action at a sign-in page (ADR-0153), or to elicitation to keep this behaviour; unset will refuse to start from the next minor release"
)

var (
	errPageClosed     = errors.New("this confirmation is no longer open on ploeg-mcp (it expired, ploeg-mcp restarted, or another replica issued it); nothing changed. Call the tool again")
	errPagesFull      = errors.New("ploeg-mcp holds too many open confirmation pages; nothing changed. Try again in a few minutes")
	errWrongPerson    = errors.New("you signed in as someone other than the person this action is for; nothing was confirmed")
	errStaleLogin     = errors.New("the identity provider did not sign you in afresh for this confirmation; nothing was confirmed. Sign in again")
	errWeakLogin      = errors.New("the sign-in did not use the method this Ploeg requires for actions; nothing was confirmed. Sign in with your security key or passkey")
	errLoginNotForYou = errors.New("this sign-in was not started in this browser for this confirmation; nothing was confirmed. Open the confirmation link again")
)

type pageState int

const (
	pageMissing pageState = iota
	pageWaiting
	pageApproved
	pageDeclined
)

type loginAttempt struct {
	state    string
	nonce    string
	verifier string
	browser  [sha256.Size]byte
	started  time.Time
}

type pendingPage struct {
	id       string
	page     string
	tool     string
	effect   string
	args     string
	person   string
	username string
	expires  time.Time
	login    *loginAttempt
	state    pageState
	settled  chan struct{}
}

type confirmationPages struct {
	login     *mcpauth.Login
	origin    *url.URL
	acr       []string
	amr       []string
	maxAge    time.Duration
	namespace string
	wait      time.Duration
	now       func() time.Time
	log       *slog.Logger

	mu      sync.Mutex
	open    map[string]*pendingPage
	byPage  map[string]string
	byState map[string]string
}

func newConfirmationPages(login *mcpauth.Login, origin *url.URL, namespace string, now func() time.Time) *confirmationPages {
	return &confirmationPages{login: login, origin: origin, namespace: namespace, maxAge: defaultLoginMaxAge, wait: defaultPageWait, now: now,
		log: slog.New(slog.DiscardHandler), open: map[string]*pendingPage{}, byPage: map[string]string{}, byState: map[string]string{}}
}

func loadConfirmationMode(getenv func(string) string, executes bool) (mode string, deprecated bool, err error) {
	switch mode = strings.TrimSpace(getenv(confirmationModeEnv)); mode {
	case modeLogin, modeElicitation:
		return mode, false, nil
	case "":
		return modeElicitation, executes, nil
	}
	return "", false, fmt.Errorf("%s must be login or elicitation", confirmationModeEnv)
}

func loadConfirmationPages(getenv func(string) string, issuer string, resource *url.URL, namespace string, mcpClients []string) (*confirmationPages, error) {
	if resource.Scheme != "https" && !loopback(resource.Hostname()) {
		return nil, errors.New("PLOEG_MCP_CONFIRMATION_MODE=login serves the confirmation page on PLOEG_MCP_RESOURCE's host, which must be https")
	}
	if i := strings.TrimSpace(getenv("PLOEG_MCP_CONFIRMATION_ISSUER")); i != "" {
		issuer = i
	}
	clientID := strings.TrimSpace(getenv("PLOEG_MCP_CONFIRMATION_CLIENT_ID"))
	if slices.Contains(mcpClients, clientID) {
		return nil, errors.New("PLOEG_MCP_CONFIRMATION_CLIENT_ID must be a confidential client of its own, not one of PLOEG_MCP_CLIENT_IDS")
	}
	origin := &url.URL{Scheme: resource.Scheme, Host: resource.Host}
	login, err := mcpauth.NewLogin(mcpauth.LoginConfig{Issuer: issuer, ClientID: clientID, ClientSecret: strings.TrimSpace(getenv("PLOEG_MCP_CONFIRMATION_CLIENT_SECRET")),
		RedirectURL: origin.String() + callbackPath, Leeway: time.Minute})
	if err != nil {
		return nil, fmt.Errorf("PLOEG_MCP_CONFIRMATION_ISSUER, _CLIENT_ID or _CLIENT_SECRET: %w", err)
	}
	p := newConfirmationPages(login, origin, namespace, time.Now)
	p.acr = strings.Fields(getenv("PLOEG_MCP_CONFIRMATION_ACR_VALUES"))
	p.amr = strings.Fields(getenv("PLOEG_MCP_CONFIRMATION_AMR"))
	if raw := strings.TrimSpace(getenv("PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE")); raw != "" {
		if p.maxAge, err = time.ParseDuration(raw); err != nil || p.maxAge < 10*time.Second || p.maxAge > 10*time.Minute {
			return nil, errors.New("PLOEG_MCP_CONFIRMATION_LOGIN_MAX_AGE must be a duration between 10s and 10m")
		}
	}
	return p, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func randomText(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *confirmationPages) link(page string) string {
	return p.origin.String() + pagesPath + page
}

func (p *confirmationPages) prune() {
	now := p.now()
	for id, pending := range p.open {
		if now.After(pending.expires) {
			p.drop(id)
		}
	}
}

func (p *confirmationPages) drop(id string) {
	pending, ok := p.open[id]
	if !ok {
		return
	}
	delete(p.open, id)
	delete(p.byPage, pending.page)
	if pending.login != nil {
		delete(p.byState, pending.login.state)
	}
}

func (p *confirmationPages) offer(s confirmState, effect, args string, who person) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.open) >= maxOpenPages {
		p.prune()
		if len(p.open) >= maxOpenPages {
			return "", errPagesFull
		}
	}
	pending := &pendingPage{id: s.ID, page: randomText(32), tool: s.Tool, effect: effect, args: args, person: who.ID, username: who.Username,
		expires: time.Unix(s.Expires, 0), state: pageWaiting, settled: make(chan struct{})}
	p.open[s.ID] = pending
	p.byPage[pending.page] = s.ID
	return p.link(pending.page), nil
}

func (p *confirmationPages) status(id string) (pageState, string, <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pending, ok := p.open[id]
	if !ok || p.now().After(pending.expires) {
		return pageMissing, "", nil
	}
	return pending.state, p.link(pending.page), pending.settled
}

func (p *confirmationPages) awaitDecision(ctx context.Context, id string, wait time.Duration) (pageState, string) {
	state, link, settled := p.status(id)
	if state != pageWaiting || wait <= 0 {
		return state, link
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-settled:
	case <-timer.C:
	case <-ctx.Done():
	}
	return p.statusOnly(id), link
}

func (p *confirmationPages) statusOnly(id string) pageState {
	state, _, _ := p.status(id)
	return state
}

func (p *confirmationPages) forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drop(id)
}

func (p *confirmationPages) request(id, link, effect, sealed string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{id: &mcp.ElicitParams{Mode: "url", URL: link, ElicitationID: id,
			Message: effect + " " + pageInstruction}},
		RequestState: sealed,
	}
}

func (p *confirmationPages) settle(pending *pendingPage, state pageState) {
	pending.state = state
	close(pending.settled)
}

func (p *confirmationPages) admit(pending *pendingPage, attempt *loginAttempt, tok mcpauth.IDToken) error {
	if operatorclient.PersonID(p.namespace, tok.Subject) != pending.person {
		return errWrongPerson
	}
	if tok.AuthTime.Before(attempt.started.Add(-authTimeLeeway)) || p.now().Sub(tok.AuthTime) > p.maxAge {
		return errStaleLogin
	}
	if len(p.acr) > 0 && !slices.Contains(p.acr, tok.ACR) {
		return errWeakLogin
	}
	for _, method := range p.amr {
		if !slices.Contains(tok.AMR, method) {
			return errWeakLogin
		}
	}
	return nil
}

func (p *confirmationPages) register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+callbackPath, p.callback)
	mux.HandleFunc("GET "+pagesPath+"{page}", p.show)
	mux.Handle("POST "+pagesPath+"{page}", http.NewCrossOriginProtection().Handler(http.HandlerFunc(p.decide)))
}

func pageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

type pageView struct {
	Tool     string
	Effect   string
	Args     string
	Username string
	Person   string
	Expires  string
	Message  string
	Done     bool
	Open     bool
}

func (p *confirmationPages) view(pending *pendingPage, message string) pageView {
	args := pending.args
	var pretty any
	if json.Unmarshal([]byte(args), &pretty) == nil {
		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			args = string(b)
		}
	}
	return pageView{Tool: pending.tool, Effect: pending.effect, Args: args, Username: pending.username, Person: pending.person,
		Expires: pending.expires.UTC().Format(time.RFC1123), Message: message, Open: pending.state == pageWaiting, Done: pending.state == pageApproved}
}

func (p *confirmationPages) render(w http.ResponseWriter, status int, v pageView) {
	pageHeaders(w)
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, v)
}

func (p *confirmationPages) closed(w http.ResponseWriter) {
	p.render(w, http.StatusNotFound, pageView{Message: "This confirmation is not open: it expired, was answered, or never existed. Ask your client to call the tool again."})
}

func (p *confirmationPages) show(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	pending := p.pageLocked(r.PathValue("page"))
	var v pageView
	if pending != nil {
		v = p.view(pending, "")
	}
	p.mu.Unlock()
	if pending == nil {
		p.closed(w)
		return
	}
	p.render(w, http.StatusOK, v)
}

func (p *confirmationPages) pageLocked(page string) *pendingPage {
	pending, ok := p.open[p.byPage[page]]
	if !ok || p.now().After(pending.expires) {
		return nil
	}
	return pending
}

func (p *confirmationPages) decide(w http.ResponseWriter, r *http.Request) {
	page := r.PathValue("page")
	p.mu.Lock()
	pending := p.pageLocked(page)
	if pending == nil || pending.state != pageWaiting {
		p.mu.Unlock()
		p.closed(w)
		return
	}
	if r.FormValue("decision") == "decline" {
		p.settle(pending, pageDeclined)
		v := p.view(pending, "Declined. Nothing changed, and this confirmation is closed.")
		p.mu.Unlock()
		p.render(w, http.StatusOK, v)
		return
	}
	if pending.login != nil {
		delete(p.byState, pending.login.state)
	}
	browser := randomText(32)
	attempt := &loginAttempt{state: randomText(32), nonce: randomText(32), verifier: randomText(48), browser: sha256.Sum256([]byte(browser)), started: p.now()}
	pending.login = attempt
	p.byState[attempt.state] = pending.id
	expires := pending.expires
	p.mu.Unlock()

	challenge := sha256.Sum256([]byte(attempt.verifier))
	to, err := p.login.AuthorizationURL(r.Context(), mcpauth.LoginRequest{State: attempt.state, Nonce: attempt.nonce, Challenge: base64.RawURLEncoding.EncodeToString(challenge[:]), ACRValues: p.acr})
	if err != nil {
		p.log.Warn("confirmation sign-in could not start", "error", err)
		p.render(w, http.StatusServiceUnavailable, pageView{Message: "The identity provider cannot be reached right now. Try again in a moment."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookiePrefix + page[:16], Value: browser, Path: pagesPath, Expires: expires, HttpOnly: true,
		Secure: p.origin.Scheme == "https", SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (p *confirmationPages) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	id, ok := p.byState[q.Get("state")]
	pending := p.open[id]
	if !ok || pending == nil || pending.login == nil || p.now().After(pending.expires) || pending.state != pageWaiting {
		p.mu.Unlock()
		p.closed(w)
		return
	}
	attempt := pending.login
	delete(p.byState, attempt.state)
	pending.login = nil
	page := pending.page
	p.mu.Unlock()

	cookie, err := r.Cookie(cookiePrefix + page[:16])
	if err != nil || sha256.Sum256([]byte(cookie.Value)) != attempt.browser {
		p.refuse(w, pending, errLoginNotForYou)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		p.refuse(w, pending, errors.New("the identity provider did not sign you in; nothing was confirmed"))
		return
	}
	tok, err := p.login.Redeem(r.Context(), q.Get("code"), attempt.verifier, attempt.nonce)
	if err != nil {
		p.log.Warn("confirmation sign-in refused", "error", err)
		p.refuse(w, pending, errors.New("the identity provider's answer did not verify; nothing was confirmed"))
		return
	}
	if err := p.admit(pending, attempt, tok); err != nil {
		p.log.Warn("confirmation sign-in refused", "error", err, "person", pending.person, "acr", tok.ACR, "amr", tok.AMR)
		p.refuse(w, pending, err)
		return
	}
	p.mu.Lock()
	if pending.state != pageWaiting || p.open[pending.id] != pending {
		p.mu.Unlock()
		p.closed(w)
		return
	}
	p.settle(pending, pageApproved)
	v := p.view(pending, "Confirmed. Ploeg makes this change when your client asks again; you can close this page.")
	p.mu.Unlock()
	p.log.Info("confirmation signed", "confirmation", pending.id, "tool", pending.tool, "person", pending.person, "authTime", tok.AuthTime.UTC(), "acr", tok.ACR, "amr", tok.AMR)
	p.render(w, http.StatusOK, v)
}

func (p *confirmationPages) refuse(w http.ResponseWriter, pending *pendingPage, reason error) {
	p.mu.Lock()
	v := p.view(pending, capitalize(reason.Error())+".")
	p.mu.Unlock()
	p.render(w, http.StatusForbidden, v)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var pageTemplate = template.Must(template.New("confirmation").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Confirm a Ploeg action</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:42rem;margin:2rem auto;padding:0 1rem;color:#1b1b1b;background:#fff}
@media (prefers-color-scheme:dark){body{color:#e8e8e8;background:#161616}pre,.effect{background:#222!important}}
.effect{background:#f3f3f3;padding:1rem;border-radius:.5rem;font-size:1.1rem}
pre{background:#f3f3f3;padding:1rem;border-radius:.5rem;overflow-x:auto;white-space:pre-wrap;word-break:break-word}
button{font:inherit;padding:.6rem 1.2rem;margin-right:.5rem;border-radius:.4rem;border:1px solid #888;cursor:pointer}
button[value=sign-in]{background:#1d5bd8;color:#fff;border-color:#1d5bd8}
dt{font-weight:600}
.message{font-weight:600}
</style>
</head>
<body>
<h1>Confirm a Ploeg action</h1>
{{with .Message}}<p class="message" role="status">{{.}}</p>{{end}}
{{if .Tool}}
<p>Your AI client asks Ploeg to do this for you:</p>
<p class="effect">{{.Effect}}</p>
<dl>
<dt>Person</dt><dd>{{.Username}} ({{.Person}})</dd>
<dt>Tool</dt><dd><code>{{.Tool}}</code></dd>
<dt>Open until</dt><dd>{{.Expires}}</dd>
</dl>
<p>Arguments, exactly as the client sent them:</p>
<pre>{{.Args}}</pre>
{{if .Open}}
<form method="post">
<p>Signing in confirms exactly this action, once. Sign in as {{.Username}}; you will be asked to sign in again even if you already are.</p>
<button type="submit" name="decision" value="sign-in">Sign in to confirm</button>
<button type="submit" name="decision" value="decline">Decline</button>
</form>
{{end}}
{{end}}
</body>
</html>
`))
