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

type instructionScan struct {
	Files       []work.InstructionFile
	Hidden      hiddenCharacters
	LinkTargets []string
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
		wanted := inside || work.IsInstructionEntry(childRel)
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			if !wanted {
				continue
			}
			if err := s.followLink(childPath, childRel, wanted); err != nil {
				return err
			}
		case e.IsDir():
			if err := s.walk(childPath, childRel, wanted); err != nil {
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
	inRepo, err := filepath.Rel(s.root, target)
	if err != nil || inRepo == ".." || strings.HasPrefix(inRepo, ".."+string(filepath.Separator)) {
		return fmt.Errorf("instruction path %s is a symlink that leaves the repository", rel)
	}
	s.scan.LinkTargets = append(s.scan.LinkTargets, filepath.ToSlash(inRepo))
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
	cursor := newRuneCursor(rel)
	for {
		c, _, err := r.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		cursor.step(c, &s.scan.Hidden)
	}
	cursor.finish(&s.scan.Hidden)
	s.scan.Files = append(s.scan.Files, work.InstructionFile{Path: rel, SHA256: hex.EncodeToString(digest.Sum(nil))})
	return nil
}

func hiddenUnicodeReport(hidden hiddenCharacters) harness.OutcomeReport {
	positions := make([]string, len(hidden.Found))
	for i, h := range hidden.Found {
		positions[i] = h.String()
	}
	reason := fmt.Sprintf("agent instructions contain %d invisible, bidi, zero-width or tag character(s) "+
		"and the harness was not started; a human must inspect them: %s",
		hidden.Total, strings.Join(positions, "; "))
	if more := hidden.Total - len(hidden.Found); more > 0 {
		reason += fmt.Sprintf("; and %d more", more)
	}
	return stuckReport("hidden Unicode in agent instructions", reason)
}
