package main

import (
	"github.com/ploeg-hq/ploeg/pkg/provider/clickup"
	"github.com/ploeg-hq/ploeg/pkg/work"
)

// registerReferenceNamers gives each tracker with a reference format of its
// own its branch and commit-trailer names. ploegd and ploeg-worker register
// the same table, because one names a Shift's branch and the other its
// commits.
func registerReferenceNamers() {
	work.RegisterReferenceNamer("clickup", clickup.Reference)
}
