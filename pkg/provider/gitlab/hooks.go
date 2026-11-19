package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/provider"
)

// SigningTokenPrefix starts a GitLab webhook signing token: whsec_ and the
// base64 of a 32-byte key.
const SigningTokenPrefix = "whsec_"

// signingMajor is the first GitLab major release that signs deliveries with
// a hook's signing token in the webhook-signature header.
const signingMajor = 19

// signatureTolerance is how far a signed delivery's webhook-timestamp may be
// from ploegd's clock.
const signatureTolerance = 5 * time.Minute

// HookConnectionParam is the query parameter of a runtime hook's URL that
// names the connection whose secret proves its deliveries.
const HookConnectionParam = "connection"

type projectHook struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func projectHooksURL(inst provider.ConnectionInstance, repositoryID string) (string, error) {
	if _, err := strconv.ParseInt(repositoryID, 10, 64); err != nil {
		return "", fmt.Errorf("gitlab: a hook needs the project's numeric id, not %q: %w", repositoryID, provider.ErrRepositoryNotFound)
	}
	return apiRoot(inst) + "/projects/" + repositoryID + "/hooks", nil
}

func hooksAt(ctx context.Context, hooks, token, hookURL string) ([]projectHook, error) {
	var out []projectHook
	for page := 1; page <= 20; page++ {
		var batch []projectHook
		if err := rest(ctx, http.MethodGet, hooks+"?per_page=100&page="+strconv.Itoa(page), token, nil, &batch); err != nil {
			return nil, hookError(err)
		}
		for _, h := range batch {
			if h.URL == hookURL {
				out = append(out, h)
			}
		}
		if len(batch) < 100 {
			return out, nil
		}
	}
	return out, nil
}

func hookError(err error) error {
	switch statusOf(err) {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", provider.ErrRepositoryNotFound, err)
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("%w: %w", provider.ErrCredentialRefused, err)
	}
	return err
}

func signs(ctx context.Context, inst provider.ConnectionInstance, token string) bool {
	var v struct {
		Version string `json:"version"`
	}
	if err := rest(ctx, http.MethodGet, apiRoot(inst)+"/version", token, nil, &v); err != nil {
		return false
	}
	head, _, _ := strings.Cut(v.Version, ".")
	major, err := strconv.Atoi(head)
	return err == nil && major >= signingMajor
}

// EnsureRepositoryHook registers, or updates in place, the project webhook at
// hook.URL for the merge request, note, pipeline and push events the
// provider consumes. On GitLab 19 or later the secret is the hook's signing
// token, so every delivery carries an HMAC over its body; on an older GitLab
// it is the X-Gitlab-Token the hook echoes.
func (Connector) EnsureRepositoryHook(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, repositoryID string, hook provider.RepositoryHook) error {
	hooks, err := projectHooksURL(inst, repositoryID)
	if err != nil {
		return err
	}
	existing, err := hooksAt(ctx, hooks, cred.Token, hook.URL)
	if err != nil {
		return err
	}
	body := map[string]any{"url": hook.URL, "name": "Ploeg", "push_events": true, "merge_requests_events": true, "note_events": true,
		"pipeline_events": true, "issues_events": false, "tag_push_events": false, "enable_ssl_verification": true}
	if signs(ctx, inst, cred.Token) && strings.HasPrefix(hook.Secret, SigningTokenPrefix) {
		body["signing_token"] = hook.Secret
		body["token"] = ""
	} else {
		body["token"] = hook.Secret
	}
	if len(existing) == 0 {
		return hookError(rest(ctx, http.MethodPost, hooks, cred.Token, body, nil))
	}
	for i, h := range existing {
		path := hooks + "/" + strconv.FormatInt(h.ID, 10)
		if i == 0 {
			err = rest(ctx, http.MethodPut, path, cred.Token, body, nil)
		} else {
			err = rest(ctx, http.MethodDelete, path, cred.Token, nil, nil)
		}
		if err != nil && statusOf(err) != http.StatusNotFound {
			return hookError(err)
		}
	}
	return nil
}

// RemoveRepositoryHook deletes every project webhook at hookURL. A project
// or hook GitLab no longer shows is removed already.
func (Connector) RemoveRepositoryHook(ctx context.Context, inst provider.ConnectionInstance, cred provider.ConnectionCredential, repositoryID, hookURL string) error {
	hooks, err := projectHooksURL(inst, repositoryID)
	if err != nil {
		return err
	}
	existing, err := hooksAt(ctx, hooks, cred.Token, hookURL)
	if errors.Is(err, provider.ErrRepositoryNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range existing {
		if err := rest(ctx, http.MethodDelete, hooks+"/"+strconv.FormatInt(h.ID, 10), cred.Token, nil, nil); err != nil && statusOf(err) != http.StatusNotFound {
			errs = append(errs, hookError(err))
		}
	}
	return errors.Join(errs...)
}

// verifyHookSecret proves a delivery with a runtime connection's secret: a
// webhook-signature header must be an HMAC-SHA256 of
// "<webhook-id>.<webhook-timestamp>.<body>" under the signing token's key
// within signatureTolerance; without one, X-Gitlab-Token must equal the
// secret.
func verifyHookSecret(r *http.Request, body []byte, secret string, now time.Time) error {
	signature := strings.TrimSpace(r.Header.Get("webhook-signature"))
	if signature == "" {
		got := r.Header.Get("X-Gitlab-Token")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			return errors.New("invalid webhook token")
		}
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, SigningTokenPrefix))
	if err != nil || !strings.HasPrefix(secret, SigningTokenPrefix) {
		return errors.New("the connection's webhook secret is not a signing token")
	}
	id, stamp := r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp")
	at, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || id == "" {
		return errors.New("signed webhook without webhook-id or webhook-timestamp")
	}
	if d := now.Sub(time.Unix(at, 0)); d > signatureTolerance || d < -signatureTolerance {
		return errors.New("signed webhook outside the timestamp tolerance")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + stamp + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, candidate := range strings.Fields(signature) {
		version, value, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(value)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return errors.New("invalid webhook signature")
}
