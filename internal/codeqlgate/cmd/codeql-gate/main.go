package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ploeg-hq/ploeg/internal/codeqlgate"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func noVerdict(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "codeql-gate: "+format+"\n", args...)
	os.Exit(int(codeqlgate.NoVerdict))
}

func dismissals() {
	acceptances, err := codeqlgate.LoadAcceptances(env("ACCEPTED", "ops/security/codeql-accepted.yaml"))
	if err != nil {
		noVerdict("%v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(acceptances.Accepted); err != nil {
		noVerdict("%v", err)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "dismissals" {
		dismissals()
		return
	}
	scope := codeqlgate.Scope(env("SCOPE", string(codeqlgate.Complete)))
	if scope != codeqlgate.Complete && scope != codeqlgate.DiffInformed {
		noVerdict("SCOPE is %q, want complete or diff-informed", scope)
	}
	acceptances, err := codeqlgate.LoadAcceptances(env("ACCEPTED", "ops/security/codeql-accepted.yaml"))
	if err != nil {
		noVerdict("%v", err)
	}
	sarif, err := filepath.Glob(filepath.Join(env("SARIF_DIR", "."), "*.sarif"))
	if err != nil {
		noVerdict("%v", err)
	}
	findings, err := codeqlgate.ReadFindings(sarif)
	if err != nil {
		noVerdict("%v", err)
	}
	verdict := codeqlgate.Judge(findings, acceptances, scope)
	for _, f := range verdict.Blocking {
		fmt.Printf("::error file=%s,line=%d::%s (security-severity %.1f): %s\n", f.File, f.Line, f.Rule, f.Severity, strings.ReplaceAll(f.Message, "\n", " "))
	}
	for _, a := range verdict.Stale {
		fmt.Printf("::error file=ops/security/codeql-accepted.yaml::%s in %s is accepted but CodeQL no longer reports it; remove the entry\n", a.Rule, a.File)
	}
	fmt.Printf("codeql-gate: scope=%s blocking=%d accepted=%d stale=%d\n", scope, len(verdict.Blocking), len(verdict.Accepted), len(verdict.Stale))
	os.Exit(int(verdict.Exit()))
}
