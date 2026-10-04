// Package contextbundle checks and unpacks the files a person attaches to a
// Work Item (proposed, context bundles proof of concept). One set of rules
// serves ploegd, which refuses a bad upload at once, and the worker, which
// unpacks the same bytes for a Run: a zip, a tar.gz, or one plain file.
//
// Nothing an archive declares is trusted. Sizes are counted while the bytes
// stream, every path is checked before anything is written, and links,
// devices and nested extraction are refused or never attempted.
package contextbundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is how a bundle's bytes are read.
type Kind string

// The kinds Sniff tells apart.
const (
	KindZip   Kind = "zip"
	KindTarGz Kind = "tar.gz"
	KindFile  Kind = "file"
)

// Media types of the two archive kinds.
const (
	MediaTypeZip   = "application/zip"
	MediaTypeTarGz = "application/gzip"
)

// Limits bound what one bundle may unpack to.
type Limits struct {
	MaxFiles      int
	MaxTotalBytes int64
	MaxFileBytes  int64
	// MaxRatio bounds uncompressed to compressed bytes per entry. It is
	// checked once an entry has grown past RatioFloorBytes, so a small file
	// that compresses well is not mistaken for a bomb.
	MaxRatio        int64
	RatioFloorBytes int64
	MaxPathBytes    int
}

// DefaultLimits are the contract's limits.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:        2000,
		MaxTotalBytes:   100 << 20,
		MaxFileBytes:    25 << 20,
		MaxRatio:        200,
		RatioFloorBytes: 64 << 10,
		MaxPathBytes:    300,
	}
}

// File is one regular file in a bundle.
type File struct {
	Path  string
	Bytes int64
}

// Manifest describes a bundle that passed every rule.
type Manifest struct {
	Kind       Kind
	MediaType  string
	Files      []File
	TotalBytes int64
	// Skipped counts entries left out on purpose: __MACOSX/, .DS_Store and
	// Thumbs.db.
	Skipped int
}

// Paths lists the manifest's file paths in archive order.
func (m Manifest) Paths() []string {
	out := make([]string, len(m.Files))
	for i, f := range m.Files {
		out[i] = f.Path
	}
	return out
}

// Error is a bundle refused under one rule. Rule is a short stable name;
// Path is the entry that broke it, empty for the bundle as a whole.
type Error struct {
	Rule   string
	Path   string
	Detail string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return e.Rule + ": " + e.Detail
	}
	return fmt.Sprintf("%s: entry %q %s", e.Rule, e.Path, e.Detail)
}

func refuse(rule, p, format string, args ...any) error {
	return &Error{Rule: rule, Path: p, Detail: fmt.Sprintf(format, args...)}
}

// Sniff reads the kind from the leading bytes; the name and any declared
// content type are never consulted.
func Sniff(data []byte) Kind {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")):
		return KindZip
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return KindTarGz
	}
	return KindFile
}

var extraTypes = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".yaml": "application/yaml", ".yml": "application/yaml",
	".json": "application/json", ".txt": "text/plain", ".csv": "text/csv", ".pdf": "application/pdf",
}

// MediaType names what a bundle is: the archive type, or for a single file
// the type its extension says, falling back to the content.
func MediaType(name string, data []byte) string {
	switch Sniff(data) {
	case KindZip:
		return MediaTypeZip
	case KindTarGz:
		return MediaTypeTarGz
	}
	ext := strings.ToLower(path.Ext(name))
	if t, ok := extraTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		if media, _, err := mime.ParseMediaType(t); err == nil {
			return media
		}
	}
	media, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	return media
}

// CleanName reduces an uploaded file name to its base name and checks it:
// 1 to 200 characters, valid UTF-8, no control characters.
func CleanName(name string) (string, error) {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	switch {
	case !utf8.ValidString(name):
		return "", refuse("name", "", "is not valid UTF-8")
	case name == "" || name == "." || name == "..":
		return "", refuse("name", "", "is empty")
	case utf8.RuneCountInString(name) > 200:
		return "", refuse("name", "", "is longer than 200 characters")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", refuse("name", "", "contains a control character")
	}
	return name, nil
}

// Inspect applies every rule to a bundle without writing anything and
// returns its manifest, or an *Error naming the rule it broke.
func Inspect(name string, data []byte, lim Limits) (Manifest, error) {
	return walk(name, data, lim, func(string, io.Reader) error { return nil })
}

// Unpack applies every rule while writing the bundle under dir, which must
// not exist yet. Directories are created 0755 and files 0644; nothing is
// ever executable or a link. On any refusal dir is removed again.
func Unpack(name string, data []byte, dir string, lim Limits) (Manifest, error) {
	if _, err := os.Lstat(dir); err == nil {
		return Manifest{}, fmt.Errorf("unpack target %s already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Manifest{}, err
	}
	m, err := walk(name, data, lim, func(p string, r io.Reader) error {
		target := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return Manifest{}, err
	}
	return m, nil
}

// walker holds the running counts every rule is checked against.
type walker struct {
	lim     Limits
	m       Manifest
	seen    map[string]bool
	dirs    map[string]bool
	entries int
	write   func(p string, r io.Reader) error
}

func walk(name string, data []byte, lim Limits, write func(string, io.Reader) error) (Manifest, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}
	w := &walker{lim: lim, seen: map[string]bool{}, dirs: map[string]bool{}, write: write}
	w.m.Kind = Sniff(data)
	w.m.MediaType = MediaType(name, data)
	var err error
	switch w.m.Kind {
	case KindZip:
		err = w.zip(data)
	case KindTarGz:
		err = w.tarGz(data)
	default:
		err = w.single(name, data)
	}
	if err != nil {
		return Manifest{}, err
	}
	if len(w.m.Files) == 0 {
		return Manifest{}, refuse("empty", "", "the bundle holds no files")
	}
	return w.m, nil
}

func (w *walker) single(name string, data []byte) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return refuse("empty", clean, "is an empty file")
	}
	return w.file(clean, bytes.NewReader(data), int64(len(data)))
}

func (w *walker) zip(data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return refuse("malformed", "", "not a readable zip archive: %v", err)
	}
	for _, f := range zr.File {
		if err := w.count(); err != nil {
			return err
		}
		mode := f.Mode()
		p, skip, err := w.path(f.Name, mode.IsDir() || strings.HasSuffix(f.Name, "/"))
		if err != nil {
			return err
		}
		if skip {
			w.skip(p)
			continue
		}
		switch {
		case mode&os.ModeSymlink != 0:
			return refuse("link", p, "is a symbolic link")
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			continue
		case !mode.IsRegular():
			return refuse("special", p, "is not a regular file")
		}
		rc, err := f.Open()
		if err != nil {
			return refuse("malformed", p, "cannot be read: %v", err)
		}
		err = w.file(p, rc, int64(f.CompressedSize64))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) tarGz(data []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return refuse("malformed", "", "not a readable gzip stream: %v", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return refuse("malformed", "", "not a readable tar.gz archive: %v", err)
		}
		if err := w.count(); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		p, skip, err := w.path(h.Name, h.Typeflag == tar.TypeDir)
		if err != nil {
			return err
		}
		if skip {
			w.skip(p)
			continue
		}
		switch h.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return refuse("link", p, "is a link")
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return refuse("special", p, "is not a regular file (tar type %q)", h.Typeflag)
		}
		// A tar entry carries no compressed size of its own; the whole
		// stream's compressed length is the bound the ratio is held to.
		if err := w.file(p, tr, int64(len(data))); err != nil {
			return err
		}
	}
}

// count bounds the entries read, directories and skipped entries included,
// so an archive of nothing but directories cannot keep the walk busy.
func (w *walker) count() error {
	w.entries++
	if w.entries > 4*w.lim.MaxFiles {
		return refuse("too_many_files", "", "more than %d entries", 4*w.lim.MaxFiles)
	}
	return nil
}

// skip counts an entry left out on purpose; the bundle root itself is not
// one.
func (w *walker) skip(p string) {
	if p != "" {
		w.m.Skipped++
	}
}

func skipped(p string) bool {
	base := path.Base(p)
	if base == ".DS_Store" || base == "Thumbs.db" {
		return true
	}
	return p == "__MACOSX" || strings.HasPrefix(p, "__MACOSX/")
}

// path checks an entry name and returns its cleaned slash path. A skipped
// entry reports skip and is not checked further.
func (w *walker) path(raw string, isDir bool) (string, bool, error) {
	if !utf8.ValidString(raw) {
		return "", false, refuse("name", fmt.Sprintf("%q", raw), "is not valid UTF-8")
	}
	p := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':') {
		return "", false, refuse("absolute_path", raw, "is an absolute path")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false, refuse("path_traversal", raw, "leaves the bundle with a .. segment")
		}
	}
	p = strings.TrimPrefix(path.Clean("./"+p), "./")
	if p == "." || p == "" {
		return "", true, nil
	}
	if strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return "", false, refuse("name", raw, "contains a control character")
	}
	if len(p) > w.lim.MaxPathBytes {
		return "", false, refuse("path_too_long", raw, "is longer than %d bytes", w.lim.MaxPathBytes)
	}
	if skipped(p) {
		return p, true, nil
	}
	if isDir {
		if w.seen[p] {
			return "", false, refuse("duplicate", raw, "is both a file and a directory")
		}
		w.dirs[p] = true
		return p, false, nil
	}
	if w.seen[p] || w.dirs[p] {
		return "", false, refuse("duplicate", raw, "appears more than once")
	}
	for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
		if w.seen[parent] {
			return "", false, refuse("duplicate", raw, "sits under a path that is a file")
		}
		w.dirs[parent] = true
	}
	w.seen[p] = true
	return p, false, nil
}

// file streams one entry through the size, total and ratio rules into the
// writer. compressed is the entry's compressed length, or the bound on it.
func (w *walker) file(p string, r io.Reader, compressed int64) error {
	if len(w.m.Files) >= w.lim.MaxFiles {
		return refuse("too_many_files", p, "is past the limit of %d files", w.lim.MaxFiles)
	}
	c := &counter{r: r, w: w, path: p, compressed: max(compressed, 1)}
	if err := w.write(p, c); err != nil {
		var refusal *Error
		if errors.As(err, &refusal) {
			return refusal
		}
		return fmt.Errorf("write %s: %w", p, err)
	}
	if _, err := io.Copy(io.Discard, c); err != nil {
		return err
	}
	w.m.Files = append(w.m.Files, File{Path: p, Bytes: c.n})
	return nil
}

// counter counts an entry's bytes as they are read and fails the read the
// moment a limit is passed, whatever the archive declared.
type counter struct {
	r          io.Reader
	w          *walker
	path       string
	n          int64
	compressed int64
}

func (c *counter) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	c.w.m.TotalBytes += int64(n)
	lim := c.w.lim
	switch {
	case c.n > lim.MaxFileBytes:
		return n, refuse("file_too_large", c.path, "is larger than %d bytes", lim.MaxFileBytes)
	case c.w.m.TotalBytes > lim.MaxTotalBytes:
		return n, refuse("too_large", c.path, "takes the bundle past %d bytes uncompressed", lim.MaxTotalBytes)
	case c.n > lim.RatioFloorBytes && c.n/c.compressed > lim.MaxRatio:
		return n, refuse("compression_ratio", c.path, "expands more than %d:1", lim.MaxRatio)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, refuse("malformed", c.path, "cannot be read: %v", err)
	}
	return n, err
}
