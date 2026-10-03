package worker

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

var (
	instructionFileNames = map[string]bool{"AGENTS.md": true, "CLAUDE.md": true, ".cursorrules": true, ".mcp.json": true}
	instructionDirNames  = map[string]bool{".claude": true, ".agents": true, ".openhands": true}
)

const maxReportedHiddenCharacters = 20

type hiddenCharacter struct {
	Path   string
	Line   int
	Column int
	Rune   rune
}

func (h hiddenCharacter) String() string {
	return fmt.Sprintf("%s line %d column %d (U+%04X)", h.Path, h.Line, h.Column, h.Rune)
}

type instructionScan struct {
	Files       []work.InstructionFile
	Hidden      []hiddenCharacter
	HiddenTotal int
}

func isHiddenRune(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F,
		r >= 0x202A && r <= 0x202E,
		r >= 0x2060 && r <= 0x2064,
		r >= 0x2066 && r <= 0x2069,
		r == 0xFEFF:
		return true
	}
	return false
}

func scanInstructionFiles(root string) (instructionScan, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return instructionScan{}, err
	}
	s := &instructionScanner{root: resolved, visited: map[string]bool{resolved: true}}
	err = s.walk(resolved, "", false)
	return s.scan, err
}

type instructionScanner struct {
	root    string
	visited map[string]bool
	scan    instructionScan
}

func (s *instructionScanner) walk(dir, rel string, inside bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if rel == "" && name == ".git" {
			continue
		}
		childRel := path.Join(rel, name)
		childPath := filepath.Join(dir, name)
		childInside := inside || instructionDirNames[name]
		wanted := childInside || instructionFileNames[name]
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			if !wanted {
				continue
			}
			if err := s.followLink(childPath, childRel, childInside); err != nil {
				return err
			}
		case e.IsDir():
			if err := s.walk(childPath, childRel, childInside); err != nil {
				return err
			}
		case !wanted:
		case e.Type().IsRegular():
			if err := s.file(childPath, childRel); err != nil {
				return err
			}
		default:
			return fmt.Errorf("instruction path %s is not a regular file", childRel)
		}
	}
	return nil
}

func (s *instructionScanner) followLink(link, rel string, inside bool) error {
	target, err := filepath.EvalSymlinks(link)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("instruction path %s: %w", rel, err)
	}
	if r, err := filepath.Rel(s.root, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return fmt.Errorf("instruction path %s is a symlink that leaves the repository", rel)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("instruction path %s: %w", rel, err)
	}
	switch {
	case info.IsDir():
		if s.visited[target] {
			return fmt.Errorf("instruction path %s is a symlink loop", rel)
		}
		s.visited[target] = true
		return s.walk(target, rel, inside)
	case info.Mode().IsRegular():
		return s.file(target, rel)
	}
	return fmt.Errorf("instruction path %s is not a regular file", rel)
}

func (s *instructionScanner) file(name, rel string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	digest := sha256.New()
	r := bufio.NewReader(io.TeeReader(f, digest))
	line, column := 1, 0
	for {
		c, _, err := r.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		column++
		if isHiddenRune(c) {
			s.scan.HiddenTotal++
			if len(s.scan.Hidden) < maxReportedHiddenCharacters {
				s.scan.Hidden = append(s.scan.Hidden, hiddenCharacter{Path: rel, Line: line, Column: column, Rune: c})
			}
		}
		if c == '\n' {
			line, column = line+1, 0
		}
	}
	s.scan.Files = append(s.scan.Files, work.InstructionFile{Path: rel, SHA256: hex.EncodeToString(digest.Sum(nil))})
	return nil
}

func hiddenInstructionReport(scan instructionScan) harness.OutcomeReport {
	positions := make([]string, len(scan.Hidden))
	for i, h := range scan.Hidden {
		positions[i] = h.String()
	}
	reason := fmt.Sprintf("agent instruction files contain %d invisible, bidi or zero-width character(s) "+
		"and the harness was not started; a human must inspect them: %s",
		scan.HiddenTotal, strings.Join(positions, "; "))
	if more := scan.HiddenTotal - len(scan.Hidden); more > 0 {
		reason += fmt.Sprintf("; and %d more", more)
	}
	return stuckReport("hidden Unicode in agent instruction files", reason)
}
