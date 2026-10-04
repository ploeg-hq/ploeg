// Command ploeg-okf works with the OKF bundles Ploeg briefs Runs from:
//
//	ploeg-okf validate <bundle-dir>
//	ploeg-okf pack [-budget bytes] [-labels a,b] [-o dir] -title <text> [-description <text>] <bundle-dir>...
//	ploeg-okf to-omnigraph [-bundle name] [-agent name] <bundle-dir>      NDJSON for an Omnigraph load
//	ploeg-okf from-omnigraph -bundle name [-o dir] <rows.json|->          rebuild a bundle from ExportQuery rows
//	ploeg-okf export-query                                                print the read from-omnigraph expects
//	ploeg-okf context inspect <file>                                      check a context bundle as ploegd would
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/contextbundle"
	"github.com/ploeg-hq/ploeg/pkg/knowledge"
	"github.com/ploeg-hq/ploeg/pkg/okf"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ploeg-okf:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("want a command: validate, pack, to-omnigraph, from-omnigraph, export-query or context")
	}
	switch args[0] {
	case "validate":
		return validate(args[1:], out)
	case "pack":
		return pack(args[1:], out)
	case "to-omnigraph":
		return toOmnigraph(args[1:], out)
	case "from-omnigraph":
		return fromOmnigraph(args[1:], out)
	case "context":
		return contextCommand(args[1:], out)
	case "export-query":
		_, err := fmt.Fprintln(out, knowledge.ExportQuery)
		return err
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func validate(args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("validate takes one bundle directory")
	}
	b, problems, err := okf.ReadDir(args[0])
	if err != nil {
		return err
	}
	types := map[string]int{}
	for _, c := range b.Concepts {
		types[c.Type]++
	}
	fmt.Fprintf(out, "%s: %d concepts %v\n", b.Name, len(b.Concepts), types)
	for _, p := range problems {
		fmt.Fprintf(out, "not a concept: %s\n", p)
	}
	for from, links := range b.Dangling() {
		fmt.Fprintf(out, "dangling links in %s: %s\n", from, strings.Join(links, ", "))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d documents do not conform", len(problems))
	}
	return nil
}

func pack(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	title := fs.String("title", "", "Work Item title")
	description := fs.String("description", "", "Work Item description")
	labels := fs.String("labels", "", "comma-separated Work Item labels")
	budget := fs.Int("budget", knowledge.DefaultBudgetBytes, "pack size limit in bytes")
	dir := fs.String("o", "", "write the pack to this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" || fs.NArg() == 0 {
		return fmt.Errorf("pack needs -title and at least one bundle directory")
	}
	var sources []knowledge.Source
	for _, d := range fs.Args() {
		b, _, err := okf.ReadDir(d)
		if err != nil {
			return err
		}
		sources = append(sources, knowledge.Source{Name: b.Name, Bundle: b})
	}
	q := knowledge.Query{Title: *title, Description: *description}
	if *labels != "" {
		q.Labels = strings.Split(*labels, ",")
	}
	p := knowledge.Select(sources, q, *budget)
	if *dir != "" {
		if _, err := p.Write(*dir); err != nil {
			return err
		}
	}
	_, err := io.WriteString(out, p.Index())
	return err
}

func toOmnigraph(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("to-omnigraph", flag.ContinueOnError)
	name := fs.String("bundle", "", "bundle name (default: the directory name)")
	agent := fs.String("agent", "ploeg-okf", "author recorded on every Note")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("to-omnigraph takes one bundle directory")
	}
	b, problems, err := okf.ReadDir(fs.Arg(0))
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d documents do not conform; run validate", len(problems))
	}
	if *name != "" {
		b.Name = *name
	}
	_, err = out.Write(knowledge.ToOmnigraph(b, *agent))
	return err
}

func fromOmnigraph(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("from-omnigraph", flag.ContinueOnError)
	name := fs.String("bundle", "", "bundle name")
	dir := fs.String("o", "", "write the bundle to this directory (default: ./<bundle>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 1 {
		return fmt.Errorf("from-omnigraph needs -bundle and one rows file (- for stdin)")
	}
	var data []byte
	var err error
	if fs.Arg(0) == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return err
	}
	b, problems, err := knowledge.FromOmnigraph(*name, data)
	if err != nil {
		return err
	}
	for _, p := range problems {
		fmt.Fprintf(out, "skipped: %s\n", p)
	}
	target := *dir
	if target == "" {
		target = filepath.Join(".", *name)
	}
	if err := okf.Write(target, *name, b.Concepts); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d concepts written to %s\n", b.Name, len(b.Concepts), target)
	return nil
}

// contextCommand checks a context bundle under the rules ploegd applies at
// upload (proposed, context bundles proof of concept) and prints its
// manifest, or the rule it breaks.
func contextCommand(args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "inspect" {
		return fmt.Errorf("context takes: inspect <file>")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	m, err := contextbundle.Inspect(filepath.Base(args[1]), data, contextbundle.DefaultLimits())
	if err != nil {
		return fmt.Errorf("refused: %w", err)
	}
	fmt.Fprintf(out, "%s: %s, %d files, %d bytes unpacked, %d skipped\n", filepath.Base(args[1]), m.MediaType, len(m.Files), m.TotalBytes, m.Skipped)
	for _, f := range m.Files {
		fmt.Fprintf(out, "%10d  %s\n", f.Bytes, f.Path)
	}
	return nil
}
