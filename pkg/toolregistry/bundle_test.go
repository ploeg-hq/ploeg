package toolregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name    string
	body    string
	kind    byte
	mode    int64
	link    string
	size    int64
	rawSize bool
}

func archive(t *testing.T, gz bool, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		size := int64(len(e.body))
		if kind != tar.TypeReg {
			size = 0
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: kind, Mode: mode, Size: size, Linkname: e.link}); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if !gz {
		return buf.Bytes()
	}
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, _ = w.Write(buf.Bytes())
	_ = w.Close()
	return z.Bytes()
}

const skillMD = "---\nname: review\ndescription: Review a change\n---\nRead the diff.\n"

func goodBundle(t *testing.T) []byte {
	return archive(t, true, entry{name: "review/", kind: tar.TypeDir, mode: 0o755}, entry{name: "review/SKILL.md", body: skillMD},
		entry{name: "review/scripts/check.sh", body: "#!/bin/sh\necho ok\n", mode: 0o755})
}

func TestReadBundleAcceptsAWellFormedBundle(t *testing.T) {
	b, e := ReadBundle(goodBundle(t))
	if e != nil {
		t.Fatal(e)
	}
	if len(b.Skills) != 1 || b.Skills[0] != "review" || len(b.Files) != 2 {
		t.Fatalf("%+v", b)
	}
	again, e := ReadBundle(archive(t, false, entry{name: "review/scripts/check.sh", body: "#!/bin/sh\necho ok\n", mode: 0o700}, entry{name: "review/SKILL.md", body: skillMD, mode: 0o600}))
	if e != nil || again.Digest != b.Digest {
		t.Fatalf("the canonical digest depends on packing: %s vs %s (%v)", again.Digest, b.Digest, e)
	}
	repacked, e := ReadBundle(b.Archive())
	if e != nil || repacked.Digest != b.Digest {
		t.Fatalf("a served archive does not unpack to the same digest: %v", e)
	}
}

func TestReadBundleRefusesHostileArchives(t *testing.T) {
	ok := entry{name: "review/SKILL.md", body: skillMD}
	cases := map[string][]byte{
		"symlink":        archive(t, true, ok, entry{name: "review/creds", kind: tar.TypeSymlink, link: "/var/run/secrets/kubernetes.io/serviceaccount/token"}),
		"hard link":      archive(t, true, ok, entry{name: "review/x", kind: tar.TypeLink, link: "review/SKILL.md"}),
		"dot dot":        archive(t, true, ok, entry{name: "review/../../etc/cron.d/x", body: "x"}),
		"absolute":       archive(t, true, ok, entry{name: "/etc/passwd", body: "x"}),
		"device":         archive(t, true, ok, entry{name: "review/dev", kind: tar.TypeChar}),
		"fifo":           archive(t, true, ok, entry{name: "review/fifo", kind: tar.TypeFifo}),
		"no SKILL.md":    archive(t, true, entry{name: "review/README.md", body: "x"}),
		"top-level":      archive(t, true, ok, entry{name: "loose.txt", body: "x"}),
		"private key":    archive(t, true, ok, entry{name: "review/key.pem", body: "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n"}),
		"github token":   archive(t, true, ok, entry{name: "review/notes.md", body: "use ghp_" + strings.Repeat("A", 36)}),
		"api key line":   archive(t, true, ok, entry{name: "review/.env", body: "API_KEY=" + strings.Repeat("Zx9", 10)}),
		"not an archive": []byte("plain text"),
		"oversized":      bytes.Repeat([]byte{0}, MaxBundleCompressed+1),
	}
	for name, data := range cases {
		if _, e := ReadBundle(data); e == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, e := ReadBundle(cases["private key"])
	if e == nil || e.Code != "bundle_carries_credential" {
		t.Fatalf("a private key in a bundle: got %v, want bundle_carries_credential", e)
	}
}

func TestReadBundleRefusesAnArchiveThatUnpacksPastTheLimit(t *testing.T) {
	big := strings.Repeat("a", 26<<20)
	data := archive(t, true, entry{name: "review/SKILL.md", body: skillMD}, entry{name: "review/a.txt", body: big}, entry{name: "review/b.txt", body: big})
	if len(data) > MaxBundleCompressed {
		t.Fatalf("test archive is %d bytes compressed", len(data))
	}
	if _, e := ReadBundle(data); e == nil {
		t.Fatal("a 52 MiB bundle that compresses small is accepted")
	}
}

func loopbackPolicy() Policy {
	return Policy{PrivateHosts: []string{"127.0.0.1"}, Resolve: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
}

func TestFetchHTTPSVerifiesTheArchiveDigest(t *testing.T) {
	data := goodBundle(t)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	f := &Fetcher{Client: SafeClient(loopbackPolicy(), 0)}
	src := SkillSource{Kind: SourceHTTPS, URL: srv.URL + "/b.tgz", Digest: DigestOf(data)}
	b, e := f.Fetch(context.Background(), src, map[string]string{"token": "pull-token"})
	if e != nil || b.Skills[0] != "review" || auth != "Bearer pull-token" {
		t.Fatalf("%v %+v %q", e, b, auth)
	}
	src.Digest = DigestOf([]byte("other"))
	if _, e := f.Fetch(context.Background(), src, nil); e == nil || e.Code != "digest_mismatch" {
		t.Fatalf("got %v, want digest_mismatch", e)
	}
}

func TestSafeClientRefusesAPrivateAddressAndNeverFollowsARedirect(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	f := &Fetcher{Client: SafeClient(Policy{}, 0)}
	if _, e := f.Fetch(context.Background(), SkillSource{Kind: SourceHTTPS, URL: target.URL, Digest: DigestOf(nil)}, nil); e == nil || hits != 0 {
		t.Fatalf("a loopback address not listed was dialled: %v hits=%d", e, hits)
	}
	f = &Fetcher{Client: SafeClient(loopbackPolicy(), 0)}
	if _, e := f.Fetch(context.Background(), SkillSource{Kind: SourceHTTPS, URL: redirect.URL, Digest: DigestOf(nil)}, nil); e == nil || hits != 0 {
		t.Fatalf("a redirect was followed: %v hits=%d", e, hits)
	}
}

func ociRegistry(t *testing.T, layer []byte, mediaType string, needToken bool) (*httptest.Server, string) {
	t.Helper()
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": ociManifest,
		"config": map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": DigestOf([]byte("{}")), "size": 2},
		"layers": []map[string]any{{"mediaType": mediaType, "digest": DigestOf(layer), "size": len(layer)}}})
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:skills/review:pull" {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "registry-token"})
			return
		}
		if needToken && r.Header.Get("Authorization") != "Bearer registry-token" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry",scope="repository:skills/review:pull"`, srv.URL))
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/v2/skills/review/manifests/"+DigestOf(manifest):
			w.Header().Set("Content-Type", ociManifest)
			_, _ = w.Write(manifest)
		case r.URL.Path == "/v2/skills/review/blobs/"+DigestOf(layer):
			_, _ = w.Write(layer)
		default:
			w.WriteHeader(404)
		}
	}))
	return srv, DigestOf(manifest)
}

func TestFetchOCIFollowsTheTokenChallengeAndVerifiesManifestAndLayer(t *testing.T) {
	layer := goodBundle(t)
	srv, digest := ociRegistry(t, layer, ociLayerGzip, true)
	defer srv.Close()
	client := SafeClient(loopbackPolicy(), 0)
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	f := &Fetcher{Client: client}
	host := strings.TrimPrefix(srv.URL, "https://")
	b, e := f.Fetch(context.Background(), SkillSource{Kind: SourceOCI, Ref: host + "/skills/review@" + digest}, nil)
	if e != nil || b.Skills[0] != "review" {
		t.Fatalf("%v %+v", e, b)
	}
	if _, e := f.Fetch(context.Background(), SkillSource{Kind: SourceOCI, Ref: host + "/skills/review@sha256:" + strings.Repeat("0", 64)}, nil); e == nil {
		t.Fatal("a manifest digest the registry does not hold is accepted")
	}
}

func TestFetchOCIRefusesALayerThatIsNotABundle(t *testing.T) {
	srv, digest := ociRegistry(t, []byte("x"), "application/vnd.oci.image.config.v1+json", false)
	defer srv.Close()
	client := SafeClient(loopbackPolicy(), 0)
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	f := &Fetcher{Client: client}
	if _, e := f.Fetch(context.Background(), SkillSource{Kind: SourceOCI, Ref: strings.TrimPrefix(srv.URL, "https://") + "/skills/review@" + digest}, nil); e == nil || e.Code != "bundle_refused" {
		t.Fatalf("got %v, want bundle_refused", e)
	}
}

func TestFetchGitPinsTheCommitAndTheTreeDigest(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git binary")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command(git, append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test", "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "uploadpack.allowReachableSHA1InWant", "true")
	_ = os.MkdirAll(filepath.Join(repo, "review"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "review", "SKILL.md"), []byte(skillMD), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "skill")
	commit := run("rev-parse", "HEAD")
	want, e := ReadBundle(archive(t, false, entry{name: "review/SKILL.md", body: skillMD}))
	if e != nil {
		t.Fatal(e)
	}
	f := &Fetcher{Git: git, GitAllowFile: true}
	u := (&url.URL{Scheme: "file", Path: repo}).String()
	b, e := f.Fetch(context.Background(), SkillSource{Kind: SourceGit, URL: u, Commit: commit, Digest: want.Digest}, nil)
	if e != nil || b.Digest != want.Digest {
		t.Fatalf("%v %s", e, b.Digest)
	}
	if _, e := f.Fetch(context.Background(), SkillSource{Kind: SourceGit, URL: u, Commit: commit, Digest: DigestOf([]byte("x"))}, nil); e == nil || e.Code != "digest_mismatch" {
		t.Fatalf("got %v, want digest_mismatch", e)
	}
}

func TestOutboundCallsRefuseURLsWithUserInfoOrOtherSchemes(t *testing.T) {
	f := &Fetcher{Client: SafeClient(loopbackPolicy(), 0)}
	for _, u := range []string{"https://user:pw@127.0.0.1/x", "file:///etc/passwd", "gopher://127.0.0.1/x"} {
		if _, _, err := f.get(context.Background(), u, nil, 10); err != errOutboundURL {
			t.Errorf("%s: got %v, want errOutboundURL", u, err)
		}
	}
}
