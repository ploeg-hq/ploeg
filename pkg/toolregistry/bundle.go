package toolregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Bundle limits of ADR-0199 section 2.
const (
	MaxBundleCompressed = 10 << 20
	MaxBundleUnpacked   = 50 << 20
	MaxBundleEntries    = 2000
	MaxBundlePath       = 512
)

// Bundle is an unpacked, verified skill bundle.
type Bundle struct {
	// Digest is the canonical digest over the bundle's files.
	Digest string
	Files  map[string][]byte
	Modes  map[string]int64
	Skills []string
}

// DigestOf is sha256:<hex> of data.
func DigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CanonicalDigest is the digest of a bundle's files independent of how the
// archive was packed: every path in byte order, each with whether it is
// executable, its length and its content.
func CanonicalDigest(files map[string][]byte, modes map[string]int64) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		exec := "0"
		if modes[n]&0o111 != 0 {
			exec = "1"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", n, exec, len(files[n]))
		h.Write(files[n])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{32,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|token|password|passwd)\s*[:=]\s*["']?[A-Za-z0-9+/_-]{24,}`),
}

// CarriesCredential reports whether data looks like it holds a credential.
func CarriesCredential(data []byte) bool {
	for _, p := range secretPatterns {
		if p.Match(data) {
			return true
		}
	}
	return false
}

var skillName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

func bundleRefusal(message string) *Error {
	return refuse(422, "bundle_refused", message)
}

// ReadBundle unpacks a tar or gzip-compressed tar skill bundle under the
// rules of ADR-0199 section 2 and refuses the whole bundle on any breach.
func ReadBundle(data []byte) (Bundle, *Error) {
	if len(data) > MaxBundleCompressed {
		return Bundle{}, bundleRefusal("The bundle is larger than 10 MiB.")
	}
	var r io.Reader = bytes.NewReader(data)
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return Bundle{}, bundleRefusal("The bundle is not a valid gzip archive.")
		}
		defer gz.Close()
		r = gz
	}
	b := Bundle{Files: map[string][]byte{}, Modes: map[string]int64{}}
	tr := tar.NewReader(io.LimitReader(r, MaxBundleUnpacked+1<<20))
	entries, total := 0, int64(0)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Bundle{}, bundleRefusal("The bundle is not a valid tar archive or exceeds 50 MiB unpacked.")
		}
		entries++
		if entries > MaxBundleEntries {
			return Bundle{}, bundleRefusal("The bundle has more than 2000 entries.")
		}
		name, e := bundlePath(h.Name)
		if e != nil {
			return Bundle{}, e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		case tar.TypeXGlobalHeader:
			continue
		default:
			return Bundle{}, bundleRefusal(fmt.Sprintf("%s is not a regular file or directory; links, devices and FIFOs are refused.", name))
		}
		if name == "" {
			return Bundle{}, bundleRefusal("A file has an empty path.")
		}
		if _, dup := b.Files[name]; dup {
			return Bundle{}, bundleRefusal(fmt.Sprintf("%s appears twice.", name))
		}
		total += h.Size
		if h.Size < 0 || total > MaxBundleUnpacked {
			return Bundle{}, bundleRefusal("The bundle exceeds 50 MiB unpacked.")
		}
		content, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil || int64(len(content)) != h.Size {
			return Bundle{}, bundleRefusal(fmt.Sprintf("%s is truncated.", name))
		}
		if CarriesCredential(content) {
			return Bundle{}, &Error{Status: 422, Code: "bundle_carries_credential", Message: fmt.Sprintf("%s looks like it holds a credential; a skill is mounted into the Run pod and carries none.", name)}
		}
		b.Files[name] = content
		b.Modes[name] = h.Mode & 0o777
	}
	skills, e := bundleSkills(b.Files)
	if e != nil {
		return Bundle{}, e
	}
	b.Skills = skills
	b.Digest = CanonicalDigest(b.Files, b.Modes)
	return b, nil
}

func bundlePath(raw string) (string, *Error) {
	if len(raw) > MaxBundlePath || strings.ContainsAny(raw, "\x00\\") {
		return "", bundleRefusal(fmt.Sprintf("%q is not an allowed path.", raw))
	}
	if strings.HasPrefix(raw, "/") {
		return "", bundleRefusal(fmt.Sprintf("%q is absolute.", raw))
	}
	trimmed := strings.TrimPrefix(raw, "./")
	for _, seg := range strings.Split(strings.TrimSuffix(trimmed, "/"), "/") {
		if seg == ".." {
			return "", bundleRefusal(fmt.Sprintf("%q leaves the bundle.", raw))
		}
	}
	clean := path.Clean(trimmed)
	if clean == "." {
		return "", nil
	}
	return clean, nil
}

func bundleSkills(files map[string][]byte) ([]string, *Error) {
	tops := map[string]bool{}
	for name := range files {
		top, _, nested := strings.Cut(name, "/")
		if !nested {
			return nil, bundleRefusal(fmt.Sprintf("%s is at the top level; every top-level directory is one skill.", name))
		}
		tops[top] = true
	}
	if len(tops) == 0 {
		return nil, bundleRefusal("The bundle holds no skill.")
	}
	var skills []string
	for top := range tops {
		manifest, ok := files[top+"/SKILL.md"]
		if !ok {
			return nil, bundleRefusal(fmt.Sprintf("%s has no SKILL.md.", top))
		}
		name := frontMatterName(manifest)
		if name == "" || !skillName.MatchString(name) {
			return nil, bundleRefusal(fmt.Sprintf("%s/SKILL.md names no skill in its front matter.", top))
		}
		skills = append(skills, name)
	}
	sort.Strings(skills)
	return skills, nil
}

func frontMatterName(doc []byte) string {
	text := strings.ReplaceAll(string(doc), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	body, _, ok := strings.Cut(text[4:], "\n---")
	if !ok {
		return ""
	}
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// Archive packs a bundle back into a deterministic tar for a Run to unpack.
func (b Bundle) Archive() []byte {
	names := make([]string, 0, len(b.Files))
	for n := range b.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range names {
		mode := int64(0o444)
		if b.Modes[n]&0o111 != 0 {
			mode = 0o555
		}
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: mode, Size: int64(len(b.Files[n])), Typeflag: tar.TypeReg, Format: tar.FormatPAX})
		_, _ = tw.Write(b.Files[n])
	}
	_ = tw.Close()
	return buf.Bytes()
}
