package cardimage

import (
	"strconv"
	"strings"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

// Moment is the newest moment worth showing a card has reached (ADR-0055):
// its latest play merged, that change released to production, a finish
// level climbed, or a crack mended. Key changes exactly when a new moment is
// reached and is empty while the card has none, before its first merge.
type Moment struct {
	Key string
	// Play is the number of the latest merged play, the pull request the
	// card comment goes on; 0 when no play merged.
	Play int
}

// MomentOf reads the moment card has reached at now.
func MomentOf(card store.OperatorCard, now time.Time) Moment {
	var latest *store.CardPlay
	for i := range card.Plays {
		p := &card.Plays[i]
		if p.State != "merged" {
			continue
		}
		if latest == nil || mergedAt(p).Compare(mergedAt(latest)) >= 0 {
			latest = p
		}
	}
	if latest == nil || card.State == "withdrawn" {
		return Moment{}
	}
	m := Moment{Play: latest.Number}
	parts := []string{"merged:" + strconv.Itoa(latest.Number)}
	if r := card.Release; r != nil && r.Source == "deploy" {
		parts = append(parts, "released:"+r.Environment)
	}
	if days, ok := DaysLive(card, now); ok {
		parts = append(parts, "finish:"+FinishFor(days).Key)
	}
	if mended := mendedCracks(card); mended > 0 {
		parts = append(parts, "mended:"+strconv.Itoa(mended))
	}
	m.Key = strings.Join(parts, ";")
	return m
}

// Headline names the moment that moved the card from the key previous to
// m, for the comment's heading: "Mended", "Foil finish", "Released to
// production" or "Merged".
func (m Moment) Headline(previous string) string {
	before := fields(previous)
	now := fields(m.Key)
	switch {
	case now["mended"] != "" && now["mended"] != before["mended"]:
		return "Mended"
	case now["finish"] != "" && now["finish"] != "matte" && now["finish"] != before["finish"]:
		f := now["finish"]
		return strings.ToUpper(f[:1]) + f[1:] + " finish"
	case now["released"] != "" && now["released"] != before["released"]:
		return "Released to " + now["released"]
	default:
		return "Merged"
	}
}

func fields(key string) map[string]string {
	out := map[string]string{}
	for part := range strings.SplitSeq(key, ";") {
		if k, v, ok := strings.Cut(part, ":"); ok {
			out[k] = v
		}
	}
	return out
}

func mendedCracks(card store.OperatorCard) int {
	if card.Condition == nil {
		return 0
	}
	n := 0
	for _, k := range card.Condition.Cracks {
		if k.Mended != nil && k.Mended.ConfirmedAt != nil {
			n++
		}
	}
	return n
}

func mergedAt(p *store.CardPlay) time.Time {
	if p.MergedAt == nil {
		return time.Time{}
	}
	return *p.MergedAt
}
