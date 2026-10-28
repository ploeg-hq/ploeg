package worker

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ploeg-hq/ploeg/pkg/harness"
	"github.com/ploeg-hq/ploeg/pkg/work"
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

type hiddenCharacters struct {
	Found []hiddenCharacter
	Total int
}

func (h *hiddenCharacters) record(c hiddenCharacter) {
	h.Total++
	if len(h.Found) < maxReportedHiddenCharacters {
		h.Found = append(h.Found, c)
	}
}

type runeCursor struct {
	source       string
	line, column int
}

func newRuneCursor(source string) runeCursor { return runeCursor{source: source, line: 1} }

func (c *runeCursor) step(r rune, hidden *hiddenCharacters) {
	c.column++
	if isHiddenRune(r) {
		hidden.record(hiddenCharacter{Path: c.source, Line: c.line, Column: c.column, Rune: r})
	}
	if r == '\n' {
		c.line, c.column = c.line+1, 0
	}
}

type instructionScan struct {
	Files       []work.InstructionFile
	Hidden      hiddenCharacters
	LinkTargets []string
}

const (
	firstUnicodeTag = 0xE0000
	lastUnicodeTag  = 0xE007F
)

func isHiddenRune(r rune) bool {
	return unicode.Is(unicode.Cf, r) || (r >= firstUnicodeTag && r <= lastUnicodeTag)
}

func scanWorkItemText(item work.WorkItem) hiddenCharacters {
	var hidden hiddenCharacters
	for _, field := range []struct{ source, text string }{
		{"Work Item title", item.Title},
		{"Work Item description", item.Description},
	} {
		cursor := newRuneCursor(field.source)
		for _, r := range html.UnescapeString(field.text) {
			cursor.step(r, &hidden)
		}
	}
	return hidden
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
