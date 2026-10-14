package worker

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness/adapters/execbin"
)

const maxHarnessVersionBytes = 128

var harnessVersionTimeout = 5 * time.Second

// VersionProbe runs the program at path with --version and returns what it
// printed.
type VersionProbe func(ctx context.Context, path string) ([]byte, error)

// HarnessVersion reads the configured Harness binary's version once, before
// the first claim (ADR-0097): the first non-empty line it prints for
// --version, within harnessVersionTimeout, cut to 128 bytes. It returns ""
// when the version stays unknown: for exec and the custom acp profile, whose
// program is the Team's own; for a program that cannot be resolved before
// the clone exists; and when the probe fails or times out. It never fails
// the worker.
func HarnessVersion(hc HarnessConfig, lookPath func(string) (string, error), probe VersionProbe) string {
	if hc.Name == "exec" || (hc.Name == "acp" && strings.EqualFold(strings.TrimSpace(hc.ACP.Profile), "custom")) {
		return ""
	}
	bin, err := HarnessBinary(hc)
	if err != nil || bin == "" || strings.Contains(bin, execbin.PlaceholderTaskSpec) || strings.Contains(bin, execbin.PlaceholderTaskFile) ||
		(strings.ContainsRune(bin, '/') && !filepath.IsAbs(bin)) {
		return ""
	}
	path, err := lookPath(bin)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), harnessVersionTimeout)
	defer cancel()
	out, err := probe(ctx, path)
	if err != nil || ctx.Err() != nil {
		return ""
	}
	return firstVersionLine(out)
}

// CommandVersionProbe runs path --version with no credentials in its
// environment and a throwaway HOME, so the probe neither sees the worker's
// secrets nor writes into its home.
func CommandVersionProbe(ctx context.Context, path string) ([]byte, error) {
	home, err := os.MkdirTemp("", "ploeg-harness-version-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "NO_COLOR=1", "TERM=dumb"}
	cmd.Dir = home
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

func firstVersionLine(out []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.IndexFunc(line, unicode.IsControl) >= 0 {
			return ""
		}
		return cutUTF8(strings.ToValidUTF8(line, ""), maxHarnessVersionBytes)
	}
	return ""
}

func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s)
}
