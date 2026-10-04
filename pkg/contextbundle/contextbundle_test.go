package contextbundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	dir  bool
	link string
	// typeflag overrides the tar entry type.
	typeflag byte
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.link != "":
			h.SetMode(os.ModeSymlink | 0o777)
		case e.dir:
			h.SetMode(os.ModeDir | 0o755)
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		body := e.body
		if e.link != "" {
			body = e.link
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarGzOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case e.typeflag == tar.TypeXGlobalHeader:
			h = &tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "git archive"}}
		case e.typeflag != 0:
			h.Typeflag, h.Size = e.typeflag, 0
		case e.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		case e.dir:
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSniff(t *testing.T) {
	for _, tc := range []struct {
		data []byte
		want Kind
	}{
		{zipOf(t, entry{name: "a.md", body: "x"}), KindZip},
		{zipOf(t), KindZip},
		{tarGzOf(t, entry{name: "a.md", body: "x"}), KindTarGz},
		{[]byte("# notes\n"), KindFile},
		{[]byte("%PDF-1.7"), KindFile},
		{nil, KindFile},
	} {
		if got := Sniff(tc.data); got != tc.want {
			t.Errorf("Sniff(%q...) = %s, want %s", tc.data[:min(len(tc.data), 4)], got, tc.want)
		}
	}
}

func TestMediaType(t *testing.T) {
	for name, want := range map[string]string{
		"notes.md": "text/markdown", "config.yaml": "application/yaml", "data.json": "application/json",
		"spec.pdf": "application/pdf", "noext": "text/plain",
	} {
		if got := MediaType(name, []byte("plain words")); got != want {
			t.Errorf("MediaType(%s) = %s, want %s", name, got, want)
		}
	}
	if got := MediaType("x.md", zipOf(t, entry{name: "a", body: "b"})); got != MediaTypeZip {
		t.Errorf("a zip named .md is %s; the bytes decide", got)
	}
}

func TestInspect_Accepts(t *testing.T) {
	nested := zipOf(t, entry{name: "inner.md", body: "deep"})
	for _, tc := range []struct {
		name      string
		file      string
		data      []byte
		wantPaths []string
		wantSkip  int
	}{
		{"single markdown file", "notes/../design notes.md", []byte("# Design\n"), []string{"design notes.md"}, 0},
		{"zip with directories", "bundle.zip", zipOf(t,
			entry{name: "docs/", dir: true}, entry{name: "docs/a.md", body: "a"}, entry{name: "./b.txt", body: "bb"}),
			[]string{"docs/a.md", "b.txt"}, 0},
		{"zip skips macOS and Windows litter", "b.zip", zipOf(t,
			entry{name: "__MACOSX/._a.md", body: "junk"}, entry{name: "a.md", body: "a"},
			entry{name: "sub/.DS_Store", body: "junk"}, entry{name: "Thumbs.db", body: "junk"}),
			[]string{"a.md"}, 3},
		{"nested archive kept as a file", "outer.zip", zipOf(t, entry{name: "inner.zip", body: string(nested)}),
			[]string{"inner.zip"}, 0},
		{"tar.gz", "b.tar.gz", tarGzOf(t,
			entry{name: "./", dir: true}, entry{name: "x/", dir: true}, entry{name: "x/y.yaml", body: "k: v"},
			entry{name: "pax_global_header", typeflag: tar.TypeXGlobalHeader}),
			[]string{"x/y.yaml"}, 0},
		{"windows separators", "w.zip", zipOf(t, entry{name: `dir\file.md`, body: "w"}), []string{"dir/file.md"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Inspect(tc.file, tc.data, DefaultLimits())
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got := strings.Join(m.Paths(), ","); got != strings.Join(tc.wantPaths, ",") {
				t.Errorf("paths = %s, want %s", got, strings.Join(tc.wantPaths, ","))
			}
			if m.Skipped != tc.wantSkip {
				t.Errorf("skipped = %d, want %d", m.Skipped, tc.wantSkip)
			}
		})
	}
}

func TestInspect_Refuses(t *testing.T) {
	small := DefaultLimits()
	small.MaxFiles, small.MaxTotalBytes, small.MaxFileBytes = 3, 1000, 600
	bomb := strings.Repeat("\x00", 10<<20)
	many := make([]entry, 0, 4)
	for _, n := range []string{"a", "b", "c", "d"} {
		many = append(many, entry{name: n, body: n})
	}
	for _, tc := range []struct {
		name string
		file string
		data []byte
		lim  Limits
		rule string
	}{
		{"zip bomb by ratio", "bomb.zip", zipOf(t, entry{name: "zeros.bin", body: bomb}), DefaultLimits(), "compression_ratio"},
		{"tar.gz bomb by ratio", "bomb.tar.gz", tarGzOf(t, entry{name: "zeros.bin", body: bomb}), DefaultLimits(), "compression_ratio"},
		{"path traversal", "t.zip", zipOf(t, entry{name: "../../etc/passwd", body: "x"}), DefaultLimits(), "path_traversal"},
		{"traversal inside", "t.tar.gz", tarGzOf(t, entry{name: "a/../../b", body: "x"}), DefaultLimits(), "path_traversal"},
		{"absolute path", "a.tar.gz", tarGzOf(t, entry{name: "/etc/passwd", body: "x"}), DefaultLimits(), "absolute_path"},
		{"drive letter", "a.zip", zipOf(t, entry{name: `C:\evil.txt`, body: "x"}), DefaultLimits(), "absolute_path"},
		{"symlink in tar", "l.tar.gz", tarGzOf(t, entry{name: "link", link: "/etc/passwd"}), DefaultLimits(), "link"},
		{"hardlink in tar", "l.tar.gz", tarGzOf(t, entry{name: "hard", typeflag: tar.TypeLink}), DefaultLimits(), "link"},
		{"fifo in tar", "f.tar.gz", tarGzOf(t, entry{name: "pipe", typeflag: tar.TypeFifo}), DefaultLimits(), "special"},
		{"symlink in zip", "l.zip", zipOf(t, entry{name: "link", link: "/etc/passwd"}), DefaultLimits(), "link"},
		{"too many files", "m.zip", zipOf(t, many...), small, "too_many_files"},
		{"one file too large", "big.zip", zipOf(t, entry{name: "big.txt", body: randomText(700)}), small, "file_too_large"},
		{"total too large", "big.zip", zipOf(t, entry{name: "a", body: randomText(500)}, entry{name: "b", body: randomText(501)}), small, "too_large"},
		{"duplicate entries", "d.zip", zipOf(t, entry{name: "a.md", body: "1"}, entry{name: "./a.md", body: "2"}), DefaultLimits(), "duplicate"},
		{"file then directory", "d.tar.gz", tarGzOf(t, entry{name: "a", body: "1"}, entry{name: "a/b", body: "2"}), DefaultLimits(), "duplicate"},
		{"path too long", "p.zip", zipOf(t, entry{name: strings.Repeat("d/", 160) + "f", body: "x"}), DefaultLimits(), "path_too_long"},
		{"invalid UTF-8 name", "u.zip", zipOf(t, entry{name: "bad\xff.md", body: "x"}), DefaultLimits(), "name"},
		{"empty zip", "e.zip", zipOf(t), DefaultLimits(), "empty"},
		{"zip of only litter", "e.zip", zipOf(t, entry{name: "__MACOSX/x", body: "x"}), DefaultLimits(), "empty"},
		{"empty single file", "e.md", []byte{}, DefaultLimits(), "empty"},
		{"truncated zip", "t.zip", zipOf(t, entry{name: "a", body: "x"})[:20], DefaultLimits(), "malformed"},
		{"gzip that is not tar", "x.md.gz", gzipOf(t, "# just markdown, not a tar stream, long enough to fail header parsing........................................................................................................................................................................................................................................................................................................................................................................................................................................................................................................"), DefaultLimits(), "malformed"},
		{"control character in a single file name", "a\x07.md", []byte("x"), DefaultLimits(), "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Inspect(tc.file, tc.data, tc.lim)
			var refusal *Error
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a refusal under %s", err, tc.rule)
			}
			if refusal.Rule != tc.rule {
				t.Errorf("rule = %s (%v), want %s", refusal.Rule, err, tc.rule)
			}
		})
	}
}

func randomText(n int) string {
	var b strings.Builder
	x := uint32(2463534242)
	for b.Len() < n {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b.WriteByte(byte('a' + x%26))
	}
	return b.String()
}

func gzipOf(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(body))
	_ = gz.Close()
	return buf.Bytes()
}

func TestUnpack_WritesPlainFilesAndNothingElse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "01-bundle")
	data := zipOf(t, entry{name: "docs/a.md", body: "alpha"}, entry{name: "run.sh", body: "#!/bin/sh\n"}, entry{name: "__MACOSX/x", body: "j"})
	m, err := Unpack("bundle.zip", data, dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalBytes != int64(len("alpha")+len("#!/bin/sh\n")) || len(m.Files) != 2 {
		t.Errorf("manifest = %+v", m)
	}
	got, err := os.ReadFile(filepath.Join(dir, "docs", "a.md"))
	if err != nil || string(got) != "alpha" {
		t.Fatalf("docs/a.md = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, "run.sh"))
	if err != nil || info.Mode().Perm()&0o111 != 0 {
		t.Errorf("run.sh mode %v: nothing unpacked is executable", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(dir, "__MACOSX")); !os.IsNotExist(err) {
		t.Error("__MACOSX was unpacked")
	}
	if _, err := Unpack("bundle.zip", data, dir, DefaultLimits()); err == nil {
		t.Error("unpacked over an existing directory")
	}
}

func TestUnpack_SingleFileKeepsItsName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "02-notes")
	if _, err := Unpack("C:\\Users\\me\\notes.md", []byte("# n\n"), dir, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "notes.md")); err != nil || string(got) != "# n\n" {
		t.Fatalf("notes.md = %q, %v", got, err)
	}
}

func TestUnpack_RefusalLeavesNothingBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "03-bad")
	data := zipOf(t, entry{name: "good.md", body: "fine"}, entry{name: "zeros", body: strings.Repeat("\x00", 10<<20)})
	if _, err := Unpack("bad.zip", data, dir, DefaultLimits()); err == nil {
		t.Fatal("bomb unpacked")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a refused bundle left %s behind", dir)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{"a/b/c.md": "c.md", `x\y.zip`: "y.zip", "  spaced.txt ": "spaced.txt"} {
		if got, err := CleanName(in); err != nil || got != want {
			t.Errorf("CleanName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "dir/", "..", strings.Repeat("n", 201), "tab\there"} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("CleanName(%q) accepted", bad)
		}
	}
}
