package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/harness"
)

// ForgeTokenIsolationProxy is the PLOEG_FORGE_TOKEN_ISOLATION value that keeps
// a writer's forge token inside the worker: the harness pushes and calls the
// forge API through a loopback proxy that only reaches the Run's repository,
// and there only the git and API endpoints its Role needs.
const ForgeTokenIsolationProxy = "proxy"

type forgeTokenProxy struct {
	server      *http.Server
	baseURL     string
	upstream    string
	placeholder string
}

func startForgeTokenProxy(repo harness.RepoRef, token string, access forgeAccess, runBranch string, leaks leakScope) (*forgeTokenProxy, error) {
	target, err := url.Parse(repo.ForgeURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, fmt.Errorf("forge URL %q is not absolute", repo.ForgeURL)
	}
	if token == "" {
		return nil, fmt.Errorf("no forge token to isolate")
	}
	if access == forgeWriter && runBranch == "" {
		return nil, fmt.Errorf("a writer's forge proxy needs the Run's branch to fence pushes to")
	}
	fence := newPushFence(runBranch)
	leaks.watch.guard(credentialForgeToken, token)
	placeholder, err := randomPlaceholder()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for the forge token proxy: %w", err)
	}
	scope := newForgeScope(repo, access)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = target.Scheme
			r.Out.URL.Host = target.Host
			r.Out.Host = target.Host
			attachForgeToken(r.Out.Header, scope.kind(r.In), repo.Dialect(), token)
		},
		FlushInterval: -1,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := scope.kind(r)
		if kind == forgeOutOfScope {
			http.Error(w, "this Run's forge access is limited to what its Role needs in "+repo.ProjectPath(), http.StatusForbidden)
			return
		}
		if kind == forgeGit && !carriesForgeCredential(r.Header) {
			challengeGitForPlaceholder(w)
			return
		}
		if !presentsForgePlaceholder(r.Header, placeholder) {
			http.Error(w, "this Run's forge proxy serves only requests that present the Run's placeholder", http.StatusForbidden)
			return
		}
		if _, leaked := leaks.watch.leak(); leaked && writesToTheForge(kind, r) {
			http.Error(w, "a credential leak stopped this Run's writes to the forge", http.StatusForbidden)
			return
		}
		if kind == forgeAPI {
			if err := inspectAPIRequestForLeaks(r, leaks); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
		if isReceivePack(r) {
			if err := fence.admit(r); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})
	p := &forgeTokenProxy{
		server:      &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second},
		baseURL:     "http://" + ln.Addr().String(),
		upstream:    target.Scheme + "://" + target.Host,
		placeholder: placeholder,
	}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

func (p *forgeTokenProxy) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
}

func (p *forgeTokenProxy) gitEnvironment() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url." + p.placeholderBaseURL() + "/.insteadOf",
		"GIT_CONFIG_VALUE_0=" + p.upstream + "/",
	}
}

func (p *forgeTokenProxy) placeholderBaseURL() string {
	u, _ := url.Parse(p.baseURL)
	u.User = url.UserPassword(forgeGitUser, p.placeholder)
	return u.String()
}

const forgeGitUser = "agent-builder"

const maxInspectedAPIBody = 8 << 20

func writesToTheForge(kind forgeRequestKind, r *http.Request) bool {
	return isReceivePack(r) || (kind == forgeAPI && !slices.Contains(readMethods, r.Method))
}

func inspectAPIRequestForLeaks(r *http.Request, leaks leakScope) error {
	route := r.Method + " " + r.URL.Path
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("the forge proxy cannot inspect an API body with Content-Encoding %q", encoding)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInspectedAPIBody+1))
	if err != nil {
		return fmt.Errorf("read the API request body: %w", err)
	}
	if len(body) > maxInspectedAPIBody {
		return fmt.Errorf("the API request body exceeds the %d bytes the forge proxy inspects", maxInspectedAPIBody)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	query, _ := url.QueryUnescape(r.URL.RawQuery)
	for _, part := range [][]byte{body, []byte(r.URL.RawQuery), []byte(query)} {
		if kind, found := leaks.watch.find(part); found {
			leaks.report(credentialLeak{kind: kind, where: "the forge API request " + route})
			return fmt.Errorf("refused %s: the request carries one of this Run's credentials", route)
		}
	}
	return nil
}

func carriesForgeCredential(h http.Header) bool {
	return h.Get("Authorization") != "" || h.Get("Private-Token") != ""
}

func challengeGitForPlaceholder(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="ploeg"`)
	http.Error(w, "present the Run's placeholder as the Basic password", http.StatusUnauthorized)
}

func presentsForgePlaceholder(h http.Header, placeholder string) bool {
	if equalSecret(h.Get("Private-Token"), placeholder) {
		return true
	}
	for _, scheme := range []string{"token", "Bearer"} {
		if credential, ok := authorizationCredential(h, scheme); ok {
			return equalSecret(credential, placeholder)
		}
	}
	encoded, ok := authorizationCredential(h, "Basic")
	if !ok {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	_, password, _ := strings.Cut(string(decoded), ":")
	return equalSecret(password, placeholder)
}

type forgeRequestKind int

const (
	forgeOutOfScope forgeRequestKind = iota
	forgeGit
	forgeAPI
)

type forgeAccess int

const (
	forgeReader forgeAccess = iota
	forgeWriter
)

type forgeEndpoint struct {
	methods []string
	path    []string
}

const (
	anyNumber  = "{number}"
	anySegment = "{segment}"
)

var (
	readMethods = []string{http.MethodGet, http.MethodHead}
	postMethod  = []string{http.MethodPost}
)

var forgejoReaderEndpoints = []forgeEndpoint{
	{readMethods, nil},
	{readMethods, []string{"pulls"}},
	{readMethods, []string{"pulls", anyNumber}},
	{readMethods, []string{"pulls", anyNumber, "files"}},
	{readMethods, []string{"pulls", anyNumber, "commits"}},
	{readMethods, []string{"pulls", anyNumber, "reviews"}},
	{readMethods, []string{"issues", anyNumber, "comments"}},
	{postMethod, []string{"issues", anyNumber, "comments"}},
	{readMethods, []string{"commits", anySegment, "status"}},
	{readMethods, []string{"commits", anySegment, "statuses"}},
	{readMethods, []string{"statuses", anySegment}},
}

var forgejoWriterEndpoints = append([]forgeEndpoint{
	{postMethod, []string{"pulls"}},
	{[]string{http.MethodPatch}, []string{"pulls", anyNumber}},
}, forgejoReaderEndpoints...)

var gitlabReaderEndpoints = []forgeEndpoint{
	{readMethods, nil},
	{readMethods, []string{"merge_requests"}},
	{readMethods, []string{"merge_requests", anyNumber}},
	{readMethods, []string{"merge_requests", anyNumber, "changes"}},
	{readMethods, []string{"merge_requests", anyNumber, "diffs"}},
	{readMethods, []string{"merge_requests", anyNumber, "commits"}},
	{readMethods, []string{"merge_requests", anyNumber, "notes"}},
	{postMethod, []string{"merge_requests", anyNumber, "notes"}},
	{readMethods, []string{"issues", anyNumber, "notes"}},
	{postMethod, []string{"issues", anyNumber, "notes"}},
	{readMethods, []string{"repository", "commits", anySegment, "statuses"}},
}

var gitlabWriterEndpoints = append([]forgeEndpoint{
	{postMethod, []string{"merge_requests"}},
	{[]string{http.MethodPut}, []string{"merge_requests", anyNumber}},
}, gitlabReaderEndpoints...)

type forgeScope struct {
	gitlab    bool
	project   []string
	apiPrefix []string
	git       []forgeEndpoint
	api       []forgeEndpoint
}

func newForgeScope(repo harness.RepoRef, access forgeAccess) forgeScope {
	s := forgeScope{
		project: strings.Split(strings.ToLower(repo.ProjectPath()), "/"),
		git: []forgeEndpoint{
			{readMethods, []string{"info", "refs?service=git-upload-pack"}},
			{postMethod, []string{"git-upload-pack"}},
		},
	}
	if access == forgeWriter {
		s.git = append(s.git,
			forgeEndpoint{readMethods, []string{"info", "refs?service=git-receive-pack"}},
			forgeEndpoint{postMethod, []string{"git-receive-pack"}})
	}
	if repo.Dialect() == harness.ForgeGitLab {
		s.gitlab = true
		s.apiPrefix = []string{"api", "v4", "projects"}
		s.api = gitlabReaderEndpoints
		if access == forgeWriter {
			s.api = gitlabWriterEndpoints
		}
	} else {
		s.apiPrefix = append([]string{"api", "v1", "repos"}, s.project...)
		s.api = forgejoReaderEndpoints
		if access == forgeWriter {
			s.api = forgejoWriterEndpoints
		}
	}
	return s
}

func (s forgeScope) kind(r *http.Request) forgeRequestKind {
	segments, ok := s.decodedSegments(r.URL.EscapedPath())
	if !ok {
		return forgeOutOfScope
	}
	if rest, ok := s.gitRemainder(segments); ok {
		if len(rest) == 2 && rest[0] == "info" && rest[1] == "refs" {
			rest = []string{"info", "refs?service=" + r.URL.Query().Get("service")}
		}
		if allowed(s.git, r.Method, rest) {
			return forgeGit
		}
		return forgeOutOfScope
	}
	if rest, ok := hasPrefix(segments, s.apiPrefix); ok {
		if s.gitlab {
			if len(rest) == 0 || rest[0] != strings.Join(s.project, "/") {
				return forgeOutOfScope
			}
			rest = rest[1:]
		}
		if allowed(s.api, r.Method, rest) {
			return forgeAPI
		}
	}
	return forgeOutOfScope
}

func (s forgeScope) decodedSegments(escaped string) ([]string, bool) {
	if !strings.HasPrefix(escaped, "/") {
		return nil, false
	}
	raw := strings.Split(escaped[1:], "/")
	segments := make([]string, 0, len(raw))
	for i, part := range raw {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." {
			return nil, false
		}
		decoded = strings.ToLower(decoded)
		if strings.ContainsAny(decoded, "%\\") || strings.IndexFunc(decoded, isControl) >= 0 {
			return nil, false
		}
		if strings.Contains(decoded, "/") && !s.isGitLabProjectID(segments, i, decoded) {
			return nil, false
		}
		segments = append(segments, decoded)
	}
	return segments, true
}

func (s forgeScope) isGitLabProjectID(preceding []string, index int, decoded string) bool {
	_, atProject := hasPrefix(preceding, s.apiPrefix)
	return s.gitlab && atProject && index == len(s.apiPrefix) && decoded == strings.Join(s.project, "/")
}

func (s forgeScope) gitRemainder(segments []string) ([]string, bool) {
	if len(segments) <= len(s.project) {
		return nil, false
	}
	for i, part := range s.project {
		last := i == len(s.project)-1
		if (!last && segments[i] != part) || (last && segments[i] != part+".git") {
			return nil, false
		}
	}
	return segments[len(s.project):], true
}

func hasPrefix(segments, prefix []string) ([]string, bool) {
	if len(segments) < len(prefix) {
		return nil, false
	}
	for i, part := range prefix {
		if segments[i] != part {
			return nil, false
		}
	}
	return segments[len(prefix):], true
}

func allowed(endpoints []forgeEndpoint, method string, rest []string) bool {
	for _, e := range endpoints {
		if slices.Contains(e.methods, method) && matches(e.path, rest) {
			return true
		}
	}
	return false
}

func matches(pattern, rest []string) bool {
	if len(pattern) != len(rest) {
		return false
	}
	for i, want := range pattern {
		switch want {
		case anyNumber:
			if _, err := strconv.ParseUint(rest[i], 10, 64); err != nil {
				return false
			}
		case anySegment:
		default:
			if rest[i] != want {
				return false
			}
		}
	}
	return true
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func attachForgeToken(h http.Header, kind forgeRequestKind, dialect, token string) {
	h.Del("Authorization")
	h.Del("Private-Token")
	switch {
	case kind == forgeGit:
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(forgeGitUser+":"+token)))
	case kind == forgeAPI && dialect == harness.ForgeGitLab:
		h.Set("Private-Token", token)
	case kind == forgeAPI:
		h.Set("Authorization", "token "+token)
	}
}
