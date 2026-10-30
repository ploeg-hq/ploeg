package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ploeg-hq/ploeg/pkg/mcpauth"
	"github.com/ploeg-hq/ploeg/pkg/operatorclient"
)

const (
	defaultListen = ":8090"
	maxBody       = 1 << 20
	sessionIdle   = 30 * time.Minute
)

type groupConsumer struct {
	Group    string `json:"group"`
	TokenEnv string `json:"tokenEnv"`
	Execute  bool   `json:"execute"`
}

type httpConfig struct {
	listen          string
	resource        *url.URL
	issuer          string
	personNamespace string
	clientIDs       []string
	jwksURL         string
	requiredScope   string
	challengeScopes []string
	maxLifetime     time.Duration
	groups          []groupConsumer
	servers         []*mcp.Server
	verifier        *mcpauth.Verifier
	exchanger       *mcpauth.Exchanger
	confirmations   *confirmations
	confirmMode     string
	loopbackHTTP    bool
	warnings        []string
}

const (
	allowLoopbackHTTPEnv = "PLOEG_MCP_ALLOW_LOOPBACK_HTTP"
	tokenAudienceEnv     = "PLOEG_MCP_TOKEN_AUDIENCE"
	audienceResource     = "resource"
	audienceClientID     = "client-id"
	clientIDAudienceNote = "PLOEG_MCP_TOKEN_AUDIENCE=client-id: tokens are accepted when aud names a client id in PLOEG_MCP_CLIENT_IDS instead of PLOEG_MCP_RESOURCE, so a token issued to that client for another resource is accepted too (threat model T-OC-1); give ploeg-mcp a client no other resource uses"
	loopbackHTTPNote     = "PLOEG_MCP_ALLOW_LOOPBACK_HTTP=true: an http issuer, key set or token endpoint on a loopback host is accepted; this is for local development and is outside profile P1"
)

func loadTokenAudience(getenv func(string) string, resource *url.URL) (string, error) {
	switch strings.TrimSpace(getenv(tokenAudienceEnv)) {
	case "", audienceResource:
		return resource.String(), nil
	case audienceClientID:
		return "", nil
	}
	return "", fmt.Errorf("%s must be %s or %s", tokenAudienceEnv, audienceResource, audienceClientID)
}

func loadLoopbackHTTP(getenv func(string) string) (bool, error) {
	switch strings.TrimSpace(getenv(allowLoopbackHTTPEnv)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, fmt.Errorf("%s must be true or false", allowLoopbackHTTPEnv)
}

var tokenEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func loadHTTPConfig(getenv func(string) string, lookup func(string) (string, bool)) (*httpConfig, error) {
	cfg := &httpConfig{listen: getenv("PLOEG_MCP_LISTEN"), issuer: strings.TrimSpace(getenv("PLOEG_MCP_ISSUER")), jwksURL: strings.TrimSpace(getenv("PLOEG_MCP_JWKS_URL"))}
	if cfg.listen == "" {
		cfg.listen = defaultListen
	}
	resource, err := url.Parse(strings.TrimSpace(getenv("PLOEG_MCP_RESOURCE")))
	if err != nil || (resource.Scheme != "https" && resource.Scheme != "http") || resource.Host == "" || resource.User != nil || resource.RawQuery != "" || resource.Fragment != "" {
		return nil, errors.New("PLOEG_MCP_RESOURCE must be the exact public http(s) URL clients connect to, without query or fragment")
	}
	cfg.resource = resource
	for _, id := range strings.Split(getenv("PLOEG_MCP_CLIENT_IDS"), ",") {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(cfg.clientIDs, id) {
			if err := mcpauth.CheckClientID(id); err != nil {
				return nil, fmt.Errorf("PLOEG_MCP_CLIENT_IDS: %w", err)
			}
			cfg.clientIDs = append(cfg.clientIDs, id)
		}
	}
	cfg.requiredScope = strings.TrimSpace(getenv("PLOEG_MCP_SCOPE"))
	if cfg.requiredScope == "" {
		cfg.requiredScope = "ploeg:read"
	}
	if strings.ContainsAny(cfg.requiredScope, " \t\"\\") {
		return nil, errors.New("PLOEG_MCP_SCOPE must be one scope")
	}
	cfg.challengeScopes = strings.Fields(getenv("PLOEG_MCP_REQUEST_SCOPES"))
	if !slices.Contains(cfg.challengeScopes, cfg.requiredScope) {
		cfg.challengeScopes = append(cfg.challengeScopes, cfg.requiredScope)
	}
	for _, s := range cfg.challengeScopes {
		if strings.ContainsAny(s, "\"\\") {
			return nil, errors.New("PLOEG_MCP_REQUEST_SCOPES holds an invalid scope")
		}
	}
	if raw := strings.TrimSpace(getenv("PLOEG_MCP_MAX_TOKEN_LIFETIME")); raw != "" {
		if cfg.maxLifetime, err = time.ParseDuration(raw); err != nil || cfg.maxLifetime <= 0 || cfg.maxLifetime > 24*time.Hour {
			return nil, errors.New("PLOEG_MCP_MAX_TOKEN_LIFETIME must be a positive duration of at most 24h")
		}
	}
	cfg.personNamespace = cfg.issuer
	if ns, set := lookup("PLOEG_MCP_SUBJECT_NAMESPACE"); set {
		ns = strings.TrimSpace(ns)
		if ns == "" || len(ns) > 2048 || strings.ContainsFunc(ns, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, errors.New("PLOEG_MCP_SUBJECT_NAMESPACE, when set, must be 1 to 2048 printable characters, such as the issuer person ids were first recorded under")
		}
		cfg.personNamespace = ns
	}
	if cfg.loopbackHTTP, err = loadLoopbackHTTP(getenv); err != nil {
		return nil, err
	}
	if cfg.loopbackHTTP {
		cfg.warnings = append(cfg.warnings, loopbackHTTPNote)
	}
	tokenResource, err := loadTokenAudience(getenv, cfg.resource)
	if err != nil {
		return nil, err
	}
	if tokenResource == "" {
		if len(cfg.clientIDs) == 0 {
			return nil, fmt.Errorf("%s=%s needs PLOEG_MCP_CLIENT_IDS, the client ids aud must name", tokenAudienceEnv, audienceClientID)
		}
		cfg.warnings = append(cfg.warnings, clientIDAudienceNote)
	}
	var refused []string
	if id := strings.TrimSpace(getenv("PLOEG_MCP_CONFIRMATION_CLIENT_ID")); id != "" {
		refused = append(refused, id)
	}
	cfg.verifier, err = mcpauth.NewVerifier(mcpauth.Config{Issuer: cfg.issuer, Audiences: cfg.clientIDs, RefusedClients: refused, Resource: tokenResource, AllowLoopbackHTTP: cfg.loopbackHTTP,
		JWKSURL: cfg.jwksURL, Leeway: time.Minute, MaxLifetime: cfg.maxLifetime})
	if err != nil {
		return nil, fmt.Errorf("PLOEG_MCP_ISSUER, PLOEG_MCP_CLIENT_IDS or PLOEG_MCP_JWKS_URL: %w", err)
	}
	if cfg.groups, err = parseGroups(getenv("PLOEG_MCP_GROUPS")); err != nil {
		return nil, err
	}
	if cfg.exchanger, err = loadExchanger(getenv, cfg.loopbackHTTP); err != nil {
		return nil, err
	}
	if cfg.confirmations, err = loadConfirmations(getenv); err != nil {
		return nil, err
	}
	executes := slices.ContainsFunc(cfg.groups, func(g groupConsumer) bool { return g.Execute })
	if cfg.confirmations == nil && executes {
		return nil, fmt.Errorf("a group in PLOEG_MCP_GROUPS is marked execute, so %s must hold the key that signs each person's confirmation; set it from a Secret", confirmationKeyEnv)
	}
	mode, deprecated, err := loadConfirmationMode(getenv, executes)
	if err != nil {
		return nil, err
	}
	cfg.confirmMode = mode
	if deprecated {
		cfg.warnings = append(cfg.warnings, deprecatedDefaultMode)
	}
	if mode == modeLogin && cfg.confirmations != nil {
		if cfg.confirmations.pages, err = loadConfirmationPages(getenv, cfg.issuer, cfg.resource, cfg.personNamespace, cfg.clientIDs, cfg.loopbackHTTP); err != nil {
			return nil, err
		}
	}
	base := getenv("PLOEG_URL")
	for _, g := range cfg.groups {
		token, _ := lookup(g.TokenEnv)
		client, err := operatorclient.New(base, token)
		if err != nil {
			return nil, fmt.Errorf("group %q: PLOEG_URL or %s: %w", g.Group, g.TokenEnv, err)
		}
		var confirm *confirmations
		if g.Execute {
			confirm = cfg.confirmations
		}
		cfg.servers = append(cfg.servers, newExchangingServer(client, confirm, cfg.exchanger))
	}
	return cfg, nil
}

func loadExchanger(getenv func(string) string, loopbackHTTP bool) (*mcpauth.Exchanger, error) {
	tokenURL := strings.TrimSpace(getenv("PLOEG_MCP_EXCHANGE_TOKEN_URL"))
	if tokenURL == "" {
		return nil, nil
	}
	ex, err := mcpauth.NewExchanger(mcpauth.ExchangeConfig{
		TokenURL:          tokenURL,
		AllowLoopbackHTTP: loopbackHTTP,
		ClientID:          strings.TrimSpace(getenv("PLOEG_MCP_EXCHANGE_CLIENT_ID")),
		ClientSecret:      strings.TrimSpace(getenv("PLOEG_MCP_EXCHANGE_CLIENT_SECRET")),
		Audience:          strings.TrimSpace(getenv("PLOEG_MCP_EXCHANGE_AUDIENCE")),
		Scope:             strings.TrimSpace(getenv("PLOEG_MCP_EXCHANGE_SCOPE")),
	})
	if err != nil {
		return nil, fmt.Errorf("PLOEG_MCP_EXCHANGE_TOKEN_URL, _CLIENT_ID, _CLIENT_SECRET or _AUDIENCE: %w", err)
	}
	return ex, nil
}

func parseGroups(raw string) ([]groupConsumer, error) {
	const shape = "PLOEG_MCP_GROUPS must be a JSON array of {group, tokenEnv, execute}, first match wins"
	if len(raw) > 65536 {
		return nil, errors.New("PLOEG_MCP_GROUPS exceeds 64 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var groups []groupConsumer
	if err := dec.Decode(&groups); err != nil {
		return nil, errors.New(shape)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New(shape)
	}
	if len(groups) == 0 || len(groups) > 100 {
		return nil, errors.New("PLOEG_MCP_GROUPS needs between 1 and 100 entries")
	}
	seen := map[string]bool{}
	for _, g := range groups {
		if g.Group == "" || len(g.Group) > 256 || strings.ContainsFunc(g.Group, func(r rune) bool { return r < 0x20 || r == 0x7f }) || seen[g.Group] || !tokenEnvName.MatchString(g.TokenEnv) {
			return nil, errors.New("PLOEG_MCP_GROUPS has an empty, duplicated or invalid group, or an invalid tokenEnv")
		}
		seen[g.Group] = true
	}
	return groups, nil
}

type consumerKey struct{}

func (cfg *httpConfig) handler(logger *slog.Logger) http.Handler {
	resourceURL := cfg.resource.String()
	metadataURL := *cfg.resource
	metadataURL.Path = mcpauth.MetadataPath(cfg.resource.Path)
	metadata := mcpauth.ResourceMetadata(resourceURL, []string{cfg.issuer}, cfg.challengeScopes, "Ploeg")

	serverFor := func(r *http.Request) *mcp.Server {
		i, ok := r.Context().Value(consumerKey{}).(int)
		if !ok {
			return nil
		}
		return cfg.servers[i]
	}
	stateless := mcp.NewStreamableHTTPHandler(serverFor, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: logger, MaxRequestBodyBytes: maxBody, PropagateRequestCancellation: true})
	sessions := mcp.NewStreamableHTTPHandler(serverFor, &mcp.StreamableHTTPOptions{Logger: logger, MaxRequestBodyBytes: maxBody, SessionTimeout: sessionIdle})
	var sessionWrites time.Duration
	if cfg.confirmations != nil {
		sessionWrites = cfg.confirmations.window + time.Minute
	}
	stream := byProtocol(stateless, sessions, sessionWrites)

	guard := mcpauth.Guard{Verify: cfg.verifier.Verify, ResourceMetadataURL: metadataURL.String(), RequiredScopes: []string{cfg.requiredScope}, ChallengeScopes: cfg.challengeScopes}

	mux := http.NewServeMux()
	mux.Handle("/.well-known/oauth-protected-resource", metadata)
	if p := mcpauth.MetadataPath(cfg.resource.Path); p != "/.well-known/oauth-protected-resource" {
		mux.Handle(p, metadata)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.verifier.Ready(r.Context()); err != nil {
			mcpauth.Problem(w, http.StatusServiceUnavailable, "The identity provider's signing keys are not loaded yet.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mcpPath := cfg.resource.Path
	if mcpPath == "" {
		mcpPath = "/"
	}
	mux.Handle(mcpPath, guard.Wrap(cfg.admit(stream, logger)))
	if cfg.confirmations != nil && cfg.confirmations.pages != nil {
		cfg.confirmations.pages.log = logger
		cfg.confirmations.pages.register(mux)
	}
	return mux
}

func (cfg *httpConfig) admit(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := mcpauth.TokenInfoFrom(r.Context())
		groups, _ := info.Extra["groups"].([]string)
		username, _ := info.Extra["username"].(string)
		for i, g := range cfg.groups {
			if slices.Contains(groups, g.Group) {
				id := operatorclient.PersonID(cfg.personNamespace, info.UserID)
				logger.Info("mcp request", "sub", info.UserID, "person", id, "username", username, "group", g.Group, "method", r.Method)
				p := person{ID: id, Username: username}
				if cfg.exchanger != nil && g.Execute {
					if fields := strings.Fields(r.Header.Get("Authorization")); len(fields) == 2 {
						p.token = fields[1]
					}
				}
				admitted := &auth.TokenInfo{Scopes: info.Scopes, Expiration: info.Expiration, UserID: id + " " + g.Group, Extra: map[string]any{personExtra: p}}
				ctx := context.WithValue(context.WithValue(r.Context(), consumerKey{}, i), admittedKey{}, admitted)
				carryAdmitted(next).ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		logger.Warn("mcp request refused: no group maps to a consumer", "sub", info.UserID, "username", username)
		who := username
		if who == "" {
			who = info.UserID
		}
		mcpauth.Problem(w, http.StatusForbidden, fmt.Sprintf("Signed in as %s, but none of your groups gives access to Ploeg. Ask an administrator to add you to one of: %s.", who, strings.Join(cfg.groupNames(), ", ")))
	})
}

type admittedKey struct{}

// carryAdmitted hands the SDK the person admit chose, so that each request's
// RequestExtra names its own person and token, and a kept session answers only
// the person and group that initialized it.
func carryAdmitted(next http.Handler) http.Handler {
	return auth.RequireBearerToken(func(_ context.Context, _ string, r *http.Request) (*auth.TokenInfo, error) {
		if info, ok := r.Context().Value(admittedKey{}).(*auth.TokenInfo); ok {
			return info, nil
		}
		return nil, auth.ErrInvalidToken
	}, nil)(next)
}

// byProtocol serves 2026-07-28 requests statelessly. A client on an earlier
// revision gets a session when it initializes, so that its declared
// capabilities last and ploeg-mcp can ask it to confirm; its requests in that
// session may stay open for a confirmation's window. A request on an earlier
// revision outside any session is served statelessly, as before (ADR-0157).
func byProtocol(stateless, sessions http.Handler, sessionWrites time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Protocol-Version") >= modernProtocol || (r.Header.Get("Mcp-Session-Id") == "" && !initializes(r)) {
			stateless.ServeHTTP(w, r)
			return
		}
		if sessionWrites > 0 {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sessionWrites))
		}
		sessions.ServeHTTP(w, r)
	})
}

// initializes reports whether r is a POST whose body is one initialize
// request. It leaves r.Body readable from the start.
func initializes(r *http.Request) bool {
	if r.Method != http.MethodPost || r.Body == nil {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if err != nil || int64(len(head)) > maxBody {
		return false
	}
	var msg struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(head, &msg) == nil && msg.Method == "initialize"
}

func (cfg *httpConfig) clientsLabel() string {
	if len(cfg.clientIDs) == 0 {
		return "any client issued a token for the resource"
	}
	return strings.Join(cfg.clientIDs, ",")
}

func (cfg *httpConfig) groupNames() []string {
	names := make([]string, 0, len(cfg.groups))
	for _, g := range cfg.groups {
		names = append(names, g.Group)
	}
	return names
}

func serveHTTP(ctx context.Context, cfg *httpConfig, logger *slog.Logger) error {
	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           cfg.handler(logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	for _, warning := range cfg.warnings {
		logger.Warn(warning)
	}
	logger.Info("ploeg-mcp serving streamable HTTP", "listen", cfg.listen, "resource", cfg.resource.String(), "issuer", cfg.issuer, "groups", len(cfg.groups), "clients", cfg.clientsLabel(), "exchangesForActions", cfg.exchanger != nil, "confirmation", cfg.confirmMode)
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
