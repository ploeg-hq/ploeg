package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReviewPairsNameEveryReviewerAfterTheWriter(t *testing.T) {
	p, err := Parse(`{"gold": {"pool": "6", "rounds": [
		{"roles": [{"name": "analyst", "writes": false, "cap": "0.5"}, {"name": "splitter", "writes": false, "planner": true, "cap": "0.5"}]},
		{"roles": [{"name": "builder", "writes": true, "cap": "3"}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": "0.5"}, {"name": "tests", "writes": false, "cap": "0.5"},
			{"name": "shadow-reviewer", "writes": false, "shadow": true, "cap": "0.4"}]},
		{"roles": [{"name": "fixer", "writes": true, "cap": "1"}]},
		{"roles": [{"name": "reviewer", "writes": false, "cap": "0.5"}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReviewPair{
		{Reviewer: "reviewer", Writer: "builder"}, {Reviewer: "reviewer", Writer: "fixer"},
		{Reviewer: "tests", Writer: "builder"}, {Reviewer: "tests", Writer: "fixer"},
	}
	if got := p["gold"].ReviewPairs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReviewPairs = %+v, want %+v", got, want)
	}
}

func TestReviewPairsOfAPlanWithoutAWriterAreNone(t *testing.T) {
	p, err := Parse(`{"gold": {"rounds": [{"roles": [{"name": "reviewer", "writes": false}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p["gold"].ReviewPairs(); len(got) != 0 {
		t.Fatalf("ReviewPairs = %+v, want none", got)
	}
}

func TestIndependentReviewParsesAndKeepsTheHashOfPlansWithoutIt(t *testing.T) {
	const rounds = `"rounds": [{"roles": [{"name": "builder", "writes": true}]}, {"roles": [{"name": "reviewer", "writes": false}]}]`
	p, err := Parse(`{"plain": {"pool": "1", ` + rounds + `}, "strict": {"pool": "1", "independentReview": {"model": true, "harness": true}, ` + rounds + `}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p["strict"].IndependentReview; got == nil || !got.Model || !got.Harness {
		t.Fatalf("independentReview = %+v, want model and harness", got)
	}
	if p["plain"].IndependentReview != nil {
		t.Fatal("a plan without independentReview parsed one")
	}
	if p.Hash("plain") == p.Hash("strict") {
		t.Fatal("independentReview does not change the plan hash")
	}
	encoded, err := json.Marshal(p["plain"])
	if err != nil || strings.Contains(string(encoded), "independentReview") {
		t.Fatalf("a plan without independentReview encodes it, so its recorded hash would change: %s %v", encoded, err)
	}
}

func TestIndependentReviewAskingForNothingIsRefused(t *testing.T) {
	_, err := Parse(`{"gold": {"independentReview": {}, "rounds": [{"roles": [{"name": "builder", "writes": true}]}]}}`)
	if err == nil || !strings.Contains(err.Error(), "independentReview asks for neither") {
		t.Fatalf("err = %v, want independentReview asking for nothing refused", err)
	}
}
