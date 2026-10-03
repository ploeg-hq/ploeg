package flow

import (
	"strings"
	"testing"
	"time"
)

func amsterdam(t *testing.T, s string) time.Time {
	t.Helper()
	zone, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", s, zone)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestDefaultCalendarCountsWorkingHoursInAmsterdam(t *testing.T) {
	c := DefaultCalendar()
	hour := int64(3600)
	cases := []struct {
		name     string
		from, to string
		want     int64
	}{
		{"inside one working day", "2026-10-05 10:00", "2026-10-05 12:30", 2*hour + 1800},
		{"before opening and after closing", "2026-10-05 06:00", "2026-10-05 20:00", 8 * hour},
		{"over a night", "2026-10-05 16:00", "2026-10-06 10:00", 2 * hour},
		{"a whole weekend", "2026-10-03 00:00", "2026-10-05 00:00", 0},
		{"Friday afternoon to Monday morning", "2026-10-02 16:30", "2026-10-05 09:30", hour},
		{"a full week", "2026-10-05 09:00", "2026-10-12 09:00", 40 * hour},
		{"over the spring daylight saving change", "2026-03-27 16:00", "2026-03-30 10:00", 2 * hour},
		{"over the autumn daylight saving change", "2026-10-23 16:00", "2026-10-26 10:00", 2 * hour},
		{"over a month boundary", "2026-09-30 16:00", "2026-10-01 10:00", 2 * hour},
		{"empty", "2026-10-05 10:00", "2026-10-05 10:00", 0},
		{"backwards", "2026-10-05 12:00", "2026-10-05 10:00", 0},
	}
	for _, tc := range cases {
		if got := c.WorkingSeconds(amsterdam(t, tc.from), amsterdam(t, tc.to)); got != tc.want {
			t.Errorf("%s: %d seconds, want %d", tc.name, got, tc.want)
		}
	}
}

func TestCalendarCountsUTCInstantsInItsZone(t *testing.T) {
	c := DefaultCalendar()
	from := time.Date(2026, 7, 6, 7, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 6, 8, 0, 0, 0, time.UTC)
	if got := c.WorkingSeconds(from, to); got != 3600 {
		t.Fatalf("07:00-08:00 UTC is 09:00-10:00 in Amsterdam summer time, got %d", got)
	}
	before := time.Date(2026, 7, 6, 6, 0, 0, 0, time.UTC)
	if got := c.WorkingSeconds(before, from); got != 0 {
		t.Fatalf("08:00-09:00 in Amsterdam is before opening, got %d", got)
	}
}

func TestCalendarCountsRealTimeWhenDaylightSavingChangesInsideWorkingHours(t *testing.T) {
	c, err := NewCalendar(Hours{Days: []string{"sun"}, Start: "01:00", End: "04:00"})
	if err != nil {
		t.Fatal(err)
	}
	spring := c.WorkingSeconds(amsterdam(t, "2026-03-29 00:00"), amsterdam(t, "2026-03-29 12:00"))
	autumn := c.WorkingSeconds(amsterdam(t, "2026-10-25 00:00"), amsterdam(t, "2026-10-25 12:00"))
	normal := c.WorkingSeconds(amsterdam(t, "2026-10-18 00:00"), amsterdam(t, "2026-10-18 12:00"))
	if spring != 2*3600 || autumn != 4*3600 || normal != 3*3600 {
		t.Fatalf("01:00-04:00 lasts %d s on the spring change, %d s on the autumn change and %d s otherwise; want 7200, 14400, 10800",
			spring, autumn, normal)
	}
}

func TestCalendarSkipsConfiguredHolidaysAndDays(t *testing.T) {
	c, err := NewCalendar(Hours{Timezone: "UTC", Days: []string{"mon", "tue", "wed", "thu", "fri", "sat"}, Start: "08:00", End: "12:00",
		Holidays: []string{"2026-12-25"}})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 12, 24, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)
	if got := c.WorkingSeconds(from, to); got != 2*4*3600 {
		t.Fatalf("Thursday and Saturday count, Christmas and Sunday do not: got %d", got)
	}
	info := c.Info()
	if info.Timezone != "UTC" || strings.Join(info.Days, ",") != "mon,tue,wed,thu,fri,sat" || info.Start != "08:00" || info.End != "12:00" || info.Holidays != 1 {
		t.Fatalf("info = %+v", info)
	}
}

func TestNewCalendarRefusesBadHours(t *testing.T) {
	for _, h := range []Hours{
		{Timezone: "Mars/Olympus"},
		{Timezone: "Local"},
		{Days: []string{}},
		{Days: []string{"monday"}},
		{Days: []string{"mon", "mon"}},
		{Start: "9:00"},
		{Start: "25:00"},
		{End: "17:00:00"},
		{Start: "17:00", End: "09:00"},
		{Start: "09:00", End: "09:00"},
		{Holidays: []string{"25-12-2026"}},
		{Holidays: []string{"2026-12-25", "2026-12-25"}},
	} {
		if _, err := NewCalendar(h); err == nil {
			t.Errorf("%+v was accepted", h)
		}
	}
	info := DefaultCalendar().Info()
	if info.Timezone != "Europe/Amsterdam" || strings.Join(info.Days, ",") != "mon,tue,wed,thu,fri" || info.Start != "09:00" ||
		info.End != "17:00" || info.Holidays != 0 {
		t.Fatalf("default calendar = %+v", info)
	}
}
