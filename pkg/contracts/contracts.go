// Package contracts names the published contracts under docs/contracts that
// this Ploeg serves, and the major version of each it answers (ADR-0147).
package contracts

import "slices"

// Contract is one published contract and every major version of it that this
// Ploeg serves. Two versions are served only while a breaking change is in
// its overlap minor: the old major until the next minor removes it.
type Contract struct {
	Name     string `json:"name"`
	Versions []int  `json:"versions"`
}

// Serves reports whether this contract is served at the given major version.
func (c Contract) Serves(major int) bool { return slices.Contains(c.Versions, major) }

var served = []Contract{
	{Name: "checkpoint", Versions: []int{1}},
	{Name: "deploy-api", Versions: []int{1}},
	{Name: "operator-api", Versions: []int{1}},
	{Name: "operator-delivery", Versions: []int{1}},
	{Name: "outcomereport", Versions: []int{1}},
	{Name: "run-api", Versions: []int{1}},
	{Name: "taskspec", Versions: []int{1}},
	{Name: "tracker-execution", Versions: []int{1}},
}

// Served lists every published contract with the major versions this Ploeg
// serves, sorted by name. The schema files under docs/contracts are the
// source: a test fails when this list and the files disagree.
func Served() []Contract {
	out := make([]Contract, len(served))
	for i, c := range served {
		out[i] = Contract{Name: c.Name, Versions: slices.Clone(c.Versions)}
	}
	return out
}

// Lookup returns the served contract with the given name.
func Lookup(name string) (Contract, bool) {
	for _, c := range Served() {
		if c.Name == name {
			return c, true
		}
	}
	return Contract{}, false
}
