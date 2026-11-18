package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/gatewaycontract"
	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/harness/skills"
	"github.com/ploeg-hq/ploeg/pkg/toolregistry"
)

var errNoRunTools = errors.New("ploegd serves no registry tools")

// RunTools reads the MCP server keys and skill bundles a Run was given
// (ADR-0199). A ploegd without the route is errNoRunTools.
func (a *APIClient) RunTools(ctx context.Context, runToken string) (gatewaycontract.RunTools, error) {
	var out gatewaycontract.RunTools
	resp, err := a.request(ctx, http.MethodGet, "/api/v1/runs/"+runToken+"/tools", nil, a.runCredential(runToken))
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return out, errNoRunTools
	default:
		return out, fmt.Errorf("run tools: HTTP %d", resp.StatusCode)
	}
	return out, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
}

// SkillBundle fetches one skill bundle the Run's grant names.
func (a *APIClient) SkillBundle(ctx context.Context, runToken, digest string) ([]byte, error) {
	resp, err := a.request(ctx, http.MethodGet, "/api/v1/runs/"+runToken+"/skills/"+digest, nil, a.runCredential(runToken))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skill bundle: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, toolregistry.MaxBundleUnpacked+(1<<20)))
}

// registryTools are what a Run installs and configures from the registry.
type registryTools struct {
	SkillPaths []string
	MCPServers []harness.MCPServer
	Notes      []string
}

type runToolsSource interface {
	RunTools(ctx context.Context, runToken string) (gatewaycontract.RunTools, error)
	SkillBundle(ctx context.Context, runToken, digest string) ([]byte, error)
}

// loadRegistryTools installs a Run's registry skills read-only, each only
// when its content matches its digest, and names the MCP servers its
// harness reaches at the gateway's MCP URL with the Run's capability. An
// unavailable bundle is left out with a note; content that does not match
// its digest is never installed (ADR-0199 section 2).
func loadRegistryTools(ctx context.Context, src runToolsSource, runToken, home string, dirs []string, gatewayMCPURL string) registryTools {
	var out registryTools
	rt, err := src.RunTools(ctx, runToken)
	if errors.Is(err, errNoRunTools) {
		return out
	}
	if err != nil {
		out.Notes = append(out.Notes, "registry tools unavailable: "+err.Error())
		return out
	}
	if rt.Withheld != "" {
		out.Notes = append(out.Notes, "MCP servers withheld: "+rt.Withheld)
	}
	if gatewayMCPURL != "" {
		for _, key := range rt.MCPServers {
			if toolregistry.ValidKey(key) {
				out.MCPServers = append(out.MCPServers, harness.MCPServer{Name: key, URL: strings.TrimRight(gatewayMCPURL, "/") + "/mcp/" + key})
			}
		}
	} else if len(rt.MCPServers) > 0 {
		out.Notes = append(out.Notes, "MCP servers withheld: this worker names no credential gateway MCP URL")
	}
	for _, sk := range rt.Skills {
		archive, err := src.SkillBundle(ctx, runToken, sk.Digest)
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("skill bundle %s left out: %v", sk.Key, err))
			continue
		}
		b, e := toolregistry.ReadBundle(archive)
		if e != nil || b.Digest != sk.Digest {
			out.Notes = append(out.Notes, fmt.Sprintf("skill bundle %s left out: its content does not match its digest", sk.Key))
			continue
		}
		paths, err := installReadOnly(home, dirs, b)
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("skill bundle %s left out: %v", sk.Key, err))
			continue
		}
		out.SkillPaths = append(out.SkillPaths, paths...)
	}
	return out
}

func installReadOnly(home string, dirs []string, b toolregistry.Bundle) ([]string, error) {
	targets := []string{skills.CanonicalDir}
	for _, d := range dirs {
		if d != skills.CanonicalDir {
			targets = append(targets, d)
		}
	}
	names := make([]string, 0, len(b.Files))
	for n := range b.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	var paths []string
	for _, d := range targets {
		if filepath.IsAbs(d) || strings.HasPrefix(filepath.Clean(d), "..") {
			return nil, fmt.Errorf("skill directory %q must be relative to HOME", d)
		}
		root := filepath.Join(home, d)
		for _, top := range topDirs(names) {
			if _, err := os.Lstat(filepath.Join(root, top)); err == nil {
				return nil, fmt.Errorf("skill %s is already installed", top)
			}
		}
		for _, n := range names {
			path := filepath.Join(root, n)
			if !strings.HasPrefix(path, root+string(filepath.Separator)) {
				return nil, fmt.Errorf("%s leaves the skill directory", n)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, err
			}
			mode := os.FileMode(0o444)
			if b.Modes[n]&0o111 != 0 {
				mode = 0o555
			}
			if err := os.WriteFile(path, b.Files[n], 0o600); err != nil {
				return nil, err
			}
			if err := os.Chmod(path, mode); err != nil {
				return nil, err
			}
			if d == skills.CanonicalDir && filepath.Base(n) == "SKILL.md" && strings.Count(n, "/") == 1 {
				paths = append(paths, path)
			}
		}
		for _, top := range topDirs(names) {
			if err := readOnlyTree(filepath.Join(root, top)); err != nil {
				return nil, err
			}
		}
	}
	return paths, nil
}

func topDirs(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		top, _, _ := strings.Cut(n, "/")
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
		}
	}
	return out
}

func readOnlyTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}
