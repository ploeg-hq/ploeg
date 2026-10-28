package spendcap

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestPeriodStartsAndEndsInUTC(t *testing.T) {
	at := time.Date(2026, 10, 29, 23, 30, 0, 0, time.FixedZone("CET", 3600))
	cases := map[Period][2]string{
		Day:   {"2026-10-29", "2026-10-30"},
		Week:  {"2026-10-26", "2026-11-02"},
		Month: {"2026-10-01", "2026-11-01"},
	}
	for period, want := range cases {
		if got := period.Start(at).Format(time.DateOnly); got != want[0] {
			t.Errorf("%s start = %s, want %s", period, got, want[0])
		}
		if got := period.End(at).Format(time.DateOnly); got != want[1] {
			t.Errorf("%s end = %s, want %s", period, got, want[1])
		}
	}
}

func TestWeekStartsOnMondayOnASunday(t *testing.T) {
	sunday := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if got := Week.Start(sunday).Format(time.DateOnly); got != "2026-10-26" {
		t.Fatalf("week of Sunday 2026-11-01 starts %s, want Monday 2026-10-26", got)
	}
}

func TestValidateRefusesWhatCannotBound(t *testing.T) {
	refused := map[string]Cap{
		"negative":       {USD: -1, Period: Day},
		"not finite":     {USD: math.Inf(1), Period: Day},
		"not a number":   {USD: math.NaN(), Period: Day},
		"unknown period": {USD: 5, Period: "hour"},
		"no period":      {USD: 5},
	}
	for name, c := range refused {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, c)
		}
	}
	for _, c := range []Cap{{}, {USD: 20, Period: Day}, {USD: 100, Period: Week}, {USD: 300, Period: Month}} {
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", c, err)
		}
	}
}

func TestParseReadsTheChartsShape(t *testing.T) {
	caps, err := Parse(`{"silver":{"usd":20,"period":"day"},"gold":{"usd":0,"period":""}}`)
	if err != nil {
		t.Fatal(err)
	}
	if caps["silver"] != (Cap{USD: 20, Period: Day}) || caps["gold"].Capped() {
		t.Fatalf("Parse = %+v", caps)
	}
	if caps, err := Parse("  "); err != nil || len(caps) != 0 {
		t.Fatalf("Parse(blank) = %v, %v", caps, err)
	}
	if _, err := Parse(`{"silver":{"usd":20,"period":"hour"}}`); err == nil || !strings.Contains(err.Error(), "team silver") {
		t.Fatalf("Parse(hour) = %v, want an error naming the team", err)
	}
}
