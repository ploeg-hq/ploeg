package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/okf"
)

// The memory graph's schema needs no change to hold OKF: a concept is a Note
// whose body is the whole OKF document, so a round trip loses nothing, and the
// graph adds what files cannot answer cheaply — which concepts share a type,
// tag or bundle, what cites a resource, and what links to what.
const (
	notePrefix  = "okf/"
	bundleTopic = "okf-bundle/"
	typeTopic   = "okf-type/"
	tagTopic    = "okf-tag/"
	timeLayout  = time.RFC3339
)

// NoteSlug is the memory-graph Note key of a concept.
func NoteSlug(bundle, conceptPath string) string { return notePrefix + bundle + "/" + conceptPath }

// BundleTopic is the Topic every Note of a bundle is About.
func BundleTopic(bundle string) string { return bundleTopic + bundle }

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

func topicSlug(prefix, name string) string {
	return prefix + strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

type ndjson struct {
	buf  bytes.Buffer
	seen map[string]bool
}

func (w *ndjson) node(typ string, data map[string]any) {
	id := typ + "\x00" + fmt.Sprint(data["slug"], data["name"], data["url"])
	if w.seen[id] {
		return
	}
	w.seen[id] = true
	line, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	w.buf.Write(line)
	w.buf.WriteByte('\n')
}

func (w *ndjson) edge(typ, from, to string) {
	id := typ + "\x00" + from + "\x00" + to
	if w.seen[id] {
		return
	}
	w.seen[id] = true
	line, _ := json.Marshal(map[string]any{"edge": typ, "from": from, "to": to})
	w.buf.Write(line)
	w.buf.WriteByte('\n')
}

// ToOmnigraph renders a bundle as NDJSON for an Omnigraph load into a graph
// with the memory schema (Agent, Topic, Source, Note; Wrote, About, Cites,
// Relates). agent is recorded as the author of every Note. Nodes come before
// the edges that need them, and links to concepts outside the bundle are
// dropped, since an edge to a missing Note fails the load.
func ToOmnigraph(b okf.Bundle, agent string) []byte {
	w := &ndjson{seen: map[string]bool{}}
	known := map[string]bool{}
	for _, c := range b.Concepts {
		known[c.Path] = true
	}
	w.node("Agent", map[string]any{"name": agent})
	w.node("Topic", map[string]any{"slug": BundleTopic(b.Name), "title": "OKF bundle " + b.Name})
	for _, c := range b.Concepts {
		w.node("Topic", map[string]any{"slug": topicSlug(typeTopic, c.Type), "title": c.Type})
		for _, t := range c.Tags {
			w.node("Topic", map[string]any{"slug": topicSlug(tagTopic, t), "title": t})
		}
		if c.Resource != "" {
			w.node("Source", map[string]any{"url": c.Resource})
		}
		note := map[string]any{"slug": NoteSlug(b.Name, c.Path), "title": okf.DisplayName(c), "body": string(okf.Format(c))}
		if !c.Timestamp.IsZero() {
			note["created_at"] = c.Timestamp.UTC().Format(timeLayout)
		}
		w.node("Note", note)
	}
	for _, c := range b.Concepts {
		slug := NoteSlug(b.Name, c.Path)
		w.edge("Wrote", agent, slug)
		w.edge("About", slug, BundleTopic(b.Name))
		w.edge("About", slug, topicSlug(typeTopic, c.Type))
		for _, t := range c.Tags {
			w.edge("About", slug, topicSlug(tagTopic, t))
		}
		if c.Resource != "" {
			w.edge("Cites", slug, c.Resource)
		}
		for _, l := range c.Links {
			if known[l] {
				w.edge("Relates", slug, NoteSlug(b.Name, l))
			}
		}
	}
	return w.buf.Bytes()
}

// ExportQuery is the read that returns a bundle's Notes for FromOmnigraph.
// Pass the bundle's topic as $topic.
const ExportQuery = `query okf_bundle($topic: String) {
  match {
    $t: Topic { slug: $topic }
    $n about $t
  }
  return { $n.slug as slug, $n.body as body }
  order { $n.slug asc }
  limit 1000
}`

// FromOmnigraph rebuilds a bundle from the rows of ExportQuery. It accepts the
// query result as returned ({"rows": [...]}) or the bare row array, with
// columns named slug and body or $n.slug and $n.body.
func FromOmnigraph(bundle string, result []byte) (okf.Bundle, []okf.Problem, error) {
	var rows []map[string]any
	var wrapped struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(result, &wrapped); err == nil && wrapped.Rows != nil {
		rows = wrapped.Rows
	} else if err := json.Unmarshal(result, &rows); err != nil {
		return okf.Bundle{}, nil, fmt.Errorf("omnigraph rows: %w", err)
	}
	b := okf.Bundle{Name: bundle}
	var problems []okf.Problem
	prefix := NoteSlug(bundle, "")
	for _, r := range rows {
		slug := firstString(r, "slug", "$n.slug", "n.slug")
		body := firstString(r, "body", "$n.body", "n.body")
		if !strings.HasPrefix(slug, prefix) {
			problems = append(problems, okf.Problem{Path: slug, Reason: "not a Note of bundle " + bundle})
			continue
		}
		p := strings.TrimPrefix(slug, prefix)
		if err := okf.ValidPath(p); err != nil {
			problems = append(problems, okf.Problem{Path: slug, Reason: err.Error()})
			continue
		}
		c, err := okf.Parse(p, []byte(body))
		if err != nil {
			problems = append(problems, okf.Problem{Path: p, Reason: err.Error()})
			continue
		}
		b.Concepts = append(b.Concepts, c)
	}
	sort.Slice(b.Concepts, func(i, j int) bool { return b.Concepts[i].Path < b.Concepts[j].Path })
	return b, problems, nil
}

func firstString(r map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := r[k].(string); ok {
			return s
		}
	}
	return ""
}
