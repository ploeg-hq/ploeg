package toolregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SafeClient is an HTTP client that never follows a redirect and refuses
// to dial a private, loopback or link-local address unless policy lists
// the host, checked on the address it actually connects to.
func SafeClient(p Policy, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			allowed := p.lists(p.PrivateHosts, host) || p.lists(p.PlainHTTPHosts, host)
			addrs, err := p.addresses(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, a := range addrs {
				if PrivateAddress(a) && !allowed {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
			}
			return nil, fmt.Errorf("toolregistry: %s resolves only to addresses this installation does not allow", host)
		},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  20 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// ErrFetch is a skill source that could not be fetched.
var ErrFetch = errors.New("toolregistry: skill source not fetched")

// Fetcher fetches a skill source and verifies it against its pin.
type Fetcher struct {
	Client *http.Client
	// Git is the git binary for git sources; empty refuses them.
	Git string
	// GitAllowFile lets a test fetch from a file:// repository.
	GitAllowFile bool
}

// Fetch returns the verified bundle of src. credential holds the pull
// credential's values (username and password, or token) when one is set.
func (f *Fetcher) Fetch(ctx context.Context, src SkillSource, credential map[string]string) (Bundle, *Error) {
	var data []byte
	var e *Error
	switch src.Kind {
	case SourceHTTPS:
		data, e = f.fetchHTTPS(ctx, src, credential)
		if e == nil && DigestOf(data) != src.Digest {
			return Bundle{}, refuse(422, "digest_mismatch", "The archive does not match its pinned digest; nothing was installed.")
		}
	case SourceOCI:
		data, e = f.fetchOCI(ctx, src.Ref, credential)
	case SourceGit:
		data, e = f.fetchGit(ctx, src, credential)
	default:
		return Bundle{}, refuse(400, "invalid_request", "Unknown source kind.")
	}
	if e != nil {
		return Bundle{}, e
	}
	b, e := ReadBundle(data)
	if e != nil {
		return Bundle{}, e
	}
	if src.Kind == SourceGit && b.Digest != src.Digest {
		return Bundle{}, refuse(422, "digest_mismatch", "The commit's tree does not match its pinned digest; nothing was installed.")
	}
	return b, nil
}

func unreachable(what string, err error) *Error {
	msg := what + " could not be fetched."
	if err != nil {
		msg = fmt.Sprintf("%s could not be fetched: %s", what, scrub(err.Error()))
	}
	return refuse(422, "source_unreachable", msg)
}

func scrub(s string) string {
	if u, err := url.Parse(s); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func (f *Fetcher) get(ctx context.Context, rawURL string, header http.Header, limit int64) (*http.Response, []byte, error) {
	if !outboundURL.MatchString(rawURL) {
		return nil, nil, errOutboundURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp, nil, err
	}
	if int64(len(body)) > limit {
		return resp, nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return resp, body, nil
}

func basicOrBearer(credential map[string]string) string {
	if t := credential["token"]; t != "" {
		return "Bearer " + t
	}
	if u, p := credential["username"], credential["password"]; u != "" || p != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))
	}
	return ""
}

func (f *Fetcher) fetchHTTPS(ctx context.Context, src SkillSource, credential map[string]string) ([]byte, *Error) {
	h := http.Header{}
	if a := basicOrBearer(credential); a != "" {
		h.Set("Authorization", a)
	}
	resp, body, err := f.get(ctx, src.URL, h, MaxBundleCompressed)
	if err != nil {
		return nil, unreachable("The archive", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unreachable(fmt.Sprintf("The archive (HTTP %d)", resp.StatusCode), nil)
	}
	return body, nil
}

const (
	ociManifest     = "application/vnd.oci.image.manifest.v1+json"
	ociLayerGzip    = "application/vnd.oci.image.layer.v1.tar+gzip"
	ociLayerTar     = "application/vnd.oci.image.layer.v1.tar"
	dockerLayerGzip = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func (f *Fetcher) fetchOCI(ctx context.Context, ref string, credential map[string]string) ([]byte, *Error) {
	m := ociRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return nil, refuse(422, "digest_required", "An oci ref is registry/repository@sha256:<64 hex>.")
	}
	registry, repo, digest := m[1], m[2], m[3]
	base := "https://" + registry + "/v2/" + repo
	auth := ""
	manifestURL := base + "/manifests/" + digest
	body, e := f.ociGet(ctx, manifestURL, ociManifest, &auth, credential, 4<<20)
	if e != nil {
		return nil, e
	}
	if DigestOf(body) != digest {
		return nil, refuse(422, "digest_mismatch", "The manifest does not match its pinned digest; nothing was installed.")
	}
	var manifest struct {
		MediaType string          `json:"mediaType"`
		Layers    []ociDescriptor `json:"layers"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil || len(manifest.Layers) != 1 {
		return nil, refuse(422, "bundle_refused", "The OCI manifest must have exactly one layer holding the skill bundle.")
	}
	layer := manifest.Layers[0]
	switch layer.MediaType {
	case ociLayerGzip, ociLayerTar, dockerLayerGzip:
	default:
		return nil, refuse(422, "bundle_refused", "The OCI layer is not a tar or tar+gzip layer.")
	}
	if !digestPattern.MatchString(layer.Digest) || layer.Size > MaxBundleCompressed {
		return nil, refuse(422, "bundle_refused", "The OCI layer has no sha256 digest or is larger than 10 MiB.")
	}
	blob, e := f.ociGet(ctx, base+"/blobs/"+layer.Digest, "*/*", &auth, credential, MaxBundleCompressed)
	if e != nil {
		return nil, e
	}
	if DigestOf(blob) != layer.Digest {
		return nil, refuse(422, "digest_mismatch", "The layer does not match the digest its pinned manifest names; nothing was installed.")
	}
	return blob, nil
}

func (f *Fetcher) ociGet(ctx context.Context, rawURL, accept string, auth *string, credential map[string]string, limit int64) ([]byte, *Error) {
	for attempt := 0; attempt < 2; attempt++ {
		h := http.Header{"Accept": {accept}}
		if *auth != "" {
			h.Set("Authorization", *auth)
		}
		resp, body, err := f.get(ctx, rawURL, h, limit)
		if err != nil {
			return nil, unreachable("The OCI artifact", err)
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			token, e := f.ociToken(ctx, resp.Header.Get("WWW-Authenticate"), credential)
			if e != nil {
				return nil, e
			}
			*auth = token
		default:
			return nil, unreachable(fmt.Sprintf("The OCI artifact (HTTP %d)", resp.StatusCode), nil)
		}
	}
	return nil, unreachable("The OCI artifact (authorization refused)", nil)
}

func (f *Fetcher) ociToken(ctx context.Context, challenge string, credential map[string]string) (string, *Error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if strings.EqualFold(scheme, "Basic") {
		if a := basicOrBearer(credential); a != "" {
			return a, nil
		}
		return "", unreachable("The OCI artifact (the registry needs a credential)", nil)
	}
	if !strings.EqualFold(scheme, "Bearer") {
		return "", unreachable("The OCI artifact (unknown authorization challenge)", nil)
	}
	values := map[string]string{}
	for _, part := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			values[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	realm, err := url.Parse(values["realm"])
	if err != nil || realm.Scheme != "https" || realm.User != nil {
		return "", unreachable("The OCI artifact (the token realm is not https)", nil)
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if values[k] != "" {
			q.Set(k, values[k])
		}
	}
	realm.RawQuery = q.Encode()
	h := http.Header{}
	if u, p := credential["username"], credential["password"]; u != "" || p != "" {
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u+":"+p)))
	}
	resp, body, err := f.get(ctx, realm.String(), h, 64<<10)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", unreachable("The OCI artifact (no registry token)", err)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.Token == "" && tok.AccessToken == "" {
		return "", unreachable("The OCI artifact (no registry token)", nil)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	return "Bearer " + tok.Token, nil
}

func (f *Fetcher) fetchGit(ctx context.Context, src SkillSource, credential map[string]string) ([]byte, *Error) {
	if f.Git == "" {
		return nil, refuse(422, "source_unreachable", "This installation has no git binary to fetch git sources with.")
	}
	u, err := url.Parse(src.URL)
	if err != nil || !(u.Scheme == "https" || u.Scheme == "http" || f.GitAllowFile && u.Scheme == "file") || u.User != nil {
		return nil, refuse(422, "invalid_url", "A git source is an https URL without user info.")
	}
	dir, err := os.MkdirTemp("", "ploeg-skill-git-*")
	if err != nil {
		return nil, unreachable("The git source", err)
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + dir, "GIT_ALLOW_PROTOCOL=https:http:file",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.followRedirects", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null"}
	if a := basicOrBearer(credential); a != "" {
		env[4] = "GIT_CONFIG_COUNT=3"
		env = append(env, "GIT_CONFIG_KEY_2=http.extraHeader", "GIT_CONFIG_VALUE_2=Authorization: "+a)
	}
	run := func(limit int64, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, f.Git, append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.Output()
		if int64(len(out)) > limit {
			return nil, errors.New("output too large")
		}
		return out, err
	}
	if _, err := run(1<<20, "init", "-q", "--bare"); err != nil {
		return nil, unreachable("The git source", nil)
	}
	if _, err := run(1<<20, "fetch", "-q", "--depth=1", "--no-tags", src.URL, src.Commit); err != nil {
		return nil, unreachable("The git source (fetch of the pinned commit failed)", nil)
	}
	got, err := run(1<<10, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(got)) != src.Commit {
		return nil, refuse(422, "digest_mismatch", "The fetched commit is not the pinned commit.")
	}
	archive, err := run(MaxBundleCompressed, "archive", "--format=tar", src.Commit)
	if err != nil {
		return nil, refuse(422, "bundle_refused", "The commit's tree is larger than 10 MiB or could not be archived.")
	}
	return archive, nil
}
