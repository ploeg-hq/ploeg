package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Guard admits a request only with a valid bearer access token that carries
// every required scope. It writes the RFC 6750 challenge itself so that a
// missing scope answers 403 with error="insufficient_scope", which
// auth.RequireBearerToken does not.
type Guard struct {
	// Verify checks a token; see Verifier.Verify.
	Verify auth.TokenVerifier
	// ResourceMetadataURL is the absolute URL of the protected resource
	// metadata, sent as resource_metadata in every challenge.
	ResourceMetadataURL string
	// RequiredScopes must all be granted.
	RequiredScopes []string
	// ChallengeScopes are what a client is told to request; empty means
	// RequiredScopes.
	ChallengeScopes []string
}

type tokenInfoKey struct{}

// TokenInfoFrom returns the verified token of a request a Guard admitted.
func TokenInfoFrom(ctx context.Context) *auth.TokenInfo {
	ti, _ := ctx.Value(tokenInfoKey{}).(*auth.TokenInfo)
	return ti
}

// WithTokenInfo returns ctx carrying ti, for handlers tested without a Guard.
func WithTokenInfo(ctx context.Context, ti *auth.TokenInfo) context.Context {
	return context.WithValue(ctx, tokenInfoKey{}, ti)
}

// Wrap returns next behind the guard.
func (g Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			g.challenge(w, http.StatusUnauthorized, "", "", "Sign in to use this MCP server.")
			return
		}
		info, err := g.Verify(r.Context(), fields[1], r)
		switch {
		case errors.Is(err, ErrKeysUnavailable):
			Problem(w, http.StatusServiceUnavailable, "The identity provider's signing keys are unavailable; try again shortly.")
			return
		case errors.Is(err, auth.ErrInvalidToken):
			reason := strings.TrimPrefix(err.Error(), auth.ErrInvalidToken.Error()+": ")
			g.challenge(w, http.StatusUnauthorized, "invalid_token", reason, "The access token was refused: "+reason+".")
			return
		case err != nil || info == nil:
			Problem(w, http.StatusInternalServerError, "The access token could not be checked.")
			return
		}
		for _, s := range g.RequiredScopes {
			if !slices.Contains(info.Scopes, s) {
				g.challenge(w, http.StatusForbidden, "insufficient_scope", "requires "+strings.Join(g.RequiredScopes, " "), "The access token lacks the scope "+s+"; sign in again and grant it.")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithTokenInfo(r.Context(), info)))
	})
}

func (g Guard) challenge(w http.ResponseWriter, status int, code, description, message string) {
	params := []string{}
	if code != "" {
		params = append(params, fmt.Sprintf("error=%q", code))
	}
	if description != "" {
		params = append(params, fmt.Sprintf("error_description=%q", strings.ReplaceAll(description, `"`, "'")))
	}
	scopes := g.ChallengeScopes
	if len(scopes) == 0 {
		scopes = g.RequiredScopes
	}
	if len(scopes) > 0 {
		params = append(params, fmt.Sprintf("scope=%q", strings.Join(scopes, " ")))
	}
	params = append(params, fmt.Sprintf("resource_metadata=%q", g.ResourceMetadataURL))
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	Problem(w, status, message)
}

// Problem writes a short JSON error body with status.
func Problem(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_")), "message": message}})
}

// ResourceMetadata returns the RFC 9728 document for resource, served with
// CORS so a browser-based client can read it.
func ResourceMetadata(resource string, authorizationServers, scopes []string, name string) http.Handler {
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               resource,
		AuthorizationServers:   authorizationServers,
		ScopesSupported:        scopes,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           name,
	})
}

// MetadataPath returns the well-known path for resource's metadata: the
// resource's path appended to /.well-known/oauth-protected-resource, as
// RFC 9728 section 3.1 inserts it.
func MetadataPath(resourcePath string) string {
	p := strings.TrimRight(resourcePath, "/")
	return "/.well-known/oauth-protected-resource" + p
}
