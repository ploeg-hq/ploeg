package worker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// FencedForgeRun is what the credential gateway knows about one Run's forge
// access (ADR-0197): the same facts the worker's forge proxy is started with.
type FencedForgeRun struct {
	Repo     harness.RepoRef
	Writes   bool
	Branch   string
	Prepares bool
	// LoadsRepositoryHooks is true for an OpenHands harness, whose push is
	// fenced off the repository's OpenHands hooks file (ADR-0148).
	LoadsRepositoryHooks bool
	ScratchDir           string
	Trace                string
	WorkItem             string
	Token                func(context.Context) string
	ModelKey             string
	Capability           string
	Log                  *slog.Logger
}

// ErrFenceUnavailable is returned when a Run's grant cannot be fenced, so the
// gateway refuses its forge requests instead of forwarding them unfenced.
var ErrFenceUnavailable = errors.New("the Run's forge access cannot be fenced")

// NewFencedForge is the worker's forge proxy fence for the credential
// gateway: one implementation of the repository, route, push, prepare and
// leak fences, serving requests the gateway has already tied to the Run.
func NewFencedForge(run FencedForgeRun) (http.Handler, error) {
	target, err := url.Parse(run.Repo.ForgeURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, fmt.Errorf("%w: forge URL %q is not absolute", ErrFenceUnavailable, run.Repo.ForgeURL)
	}
	if run.Token == nil {
		return nil, fmt.Errorf("%w: no forge credential", ErrFenceUnavailable)
	}
	if run.Writes && run.Branch == "" {
		return nil, fmt.Errorf("%w: a writer needs the Run's branch to fence pushes to", ErrFenceUnavailable)
	}
	access := forgeReader
	if run.Writes {
		access = forgeWriter
	}
	watch := newLeakWatch()
	watch.guard(credentialModelKey, run.ModelKey)
	watch.guard(credentialCapability, run.Capability)
	leaks := leakScope{run: run.Trace, workItem: run.WorkItem, log: run.Log, watch: watch}
	if leaks.log == nil {
		leaks.log = slog.New(slog.DiscardHandler)
	}
	tokens := guardedTokens{source: run.Token, watch: watch}
	fence := newPushFence(run.Branch)
	fence.fromPack = true
	if run.Writes && (run.Prepares || run.LoadsRepositoryHooks) {
		cloneURL, err := plainURL(run.Repo.ForgeURL, run.Repo.Owner, run.Repo.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrFenceUnavailable, err)
		}
		if run.Prepares {
			fence.content = append(fence.content, &deliverableFence{dir: run.ScratchDir, cloneURL: cloneURL, base: run.Repo.BaseBranch, token: tokens.current})
		}
		if run.LoadsRepositoryHooks {
			fence.content = append(fence.content, &repositoryHooksFence{dir: run.ScratchDir, cloneURL: cloneURL, base: run.Repo.BaseBranch, token: tokens.current})
		}
	}
	refusals := refusalLog{proxy: forgeProxyName, run: run.Trace, log: leaks.log, secrets: watch.values}
	presented := func(http.ResponseWriter, *http.Request, forgeRequestKind) bool { return true }
	return fencedForgeHandler(run.Repo, target, tokens, newForgeScope(run.Repo, access), fence, leaks, refusals, presented), nil
}

type guardedTokens struct {
	source func(context.Context) string
	watch  *leakWatch
}

func (g guardedTokens) current(ctx context.Context) string {
	token := g.source(ctx)
	g.watch.guard(credentialForgeToken, token)
	return token
}

// AttachModelKey replaces whatever credential a model request presents with
// the Run's key, in the header the caller used.
func AttachModelKey(h http.Header, key string) { swapModelKey(h, key) }

// PresentedCredential is the bearer value a request presents in any of the
// forms the worker's proxies accept: a Basic password, a token or Bearer
// authorization, PRIVATE-TOKEN, X-Api-Key or LiteLLM's key header.
func PresentedCredential(h http.Header) string {
	if v := h.Get("Private-Token"); v != "" {
		return v
	}
	if v := h.Get("X-Api-Key"); v != "" {
		return v
	}
	if v, ok := headerCredential(h, liteLLMKeyHeader, "Bearer"); ok {
		return v
	}
	for _, scheme := range []string{"token", "Bearer"} {
		if v, ok := authorizationCredential(h, scheme); ok {
			return v
		}
	}
	encoded, ok := authorizationCredential(h, "Basic")
	if !ok {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	_, password, _ := strings.Cut(string(decoded), ":")
	return password
}

// GitCredentialChallenge answers a git request that presented no credential,
// because git sends URL credentials only after the server asks.
func GitCredentialChallenge(w http.ResponseWriter) { challengeGitForPlaceholder(w) }
