package worker

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ploeg-hq/ploeg/pkg/harness"
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

// CommandVersionProbe runs path --version in the worker's own container with
// no credentials in its environment and a throwaway HOME, so the probe
// neither sees the worker's secrets nor writes into its home.
func CommandVersionProbe(ctx context.Context, path string) ([]byte, error) {
	return SpawnedVersionProbe(harness.NewLocalSpawner(), os.Environ(), "")(ctx, path)
}

// SpawnedVersionProbe runs path --version through spawner, on the PATH of
// environ, with a throwaway HOME made under scratch ("" is the system's
// temporary directory). With the harness in its own container (ADR-0138)
// scratch is the work directory both containers mount.
func SpawnedVersionProbe(spawner harness.Spawner, environ []string, scratch string) VersionProbe {
	return func(ctx context.Context, path string) ([]byte, error) {
		home, err := os.MkdirTemp(scratch, "ploeg-harness-version-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(home)
		searchPath, _ := lookupEnv(environ, "PATH")
		var out lockedBuffer
		proc, err := spawner.Start(ctx, harness.Command{
			Argv: []string{path, "--version"}, Dir: home,
			Env:    []string{"PATH=" + searchPath, "HOME=" + home, "NO_COLOR=1", "TERM=dumb"},
			Stdout: &out, Stderr: &out,
		})
		if err != nil {
			return nil, err
		}
		_, err = proc.Wait()
		return out.Bytes(), err
	}
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
