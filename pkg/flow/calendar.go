package flow

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

// Hours is one team's working calendar as configuration writes it. Every
// field is optional and falls back to DefaultHours.
type Hours struct {
	// Timezone is an IANA zone name, such as Europe/Amsterdam.
	Timezone string `yaml:"timezone"`
	// Days are the working weekdays as mon, tue, wed, thu, fri, sat and sun.
	Days []string `yaml:"days"`
	// Start and End are the working hours of each working day as HH:MM in
	// Timezone; Start comes before End.
	Start string `yaml:"start"`
	End   string `yaml:"end"`
	// Holidays are dates (YYYY-MM-DD) in Timezone without working hours.
	Holidays []string `yaml:"holidays"`
}

// DefaultHours is the calendar of a team that configures none: Monday to
// Friday, 09:00 to 17:00 in Europe/Amsterdam, without holidays.
var DefaultHours = Hours{Timezone: "Europe/Amsterdam", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "17:00"}

// MaxHolidays is the most holidays one calendar may list.
const MaxHolidays = 1000

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

var weekdayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Calendar counts working time: the seconds of a span that fall inside the
// working hours of a working day that is not a holiday, in its zone. It is
// deterministic: the same span always gives the same count.
type Calendar struct {
	zone     *time.Location
	days     [7]bool
	start    int
	end      int
	holidays map[int]bool
}

// DefaultCalendar is the calendar of DefaultHours.
func DefaultCalendar() Calendar {
	c, err := NewCalendar(Hours{})
	if err != nil {
		panic(err)
	}
	return c
}

// NewCalendar validates h and fills what it leaves out from DefaultHours.
func NewCalendar(h Hours) (Calendar, error) {
	if h.Timezone == "" {
		h.Timezone = DefaultHours.Timezone
	}
	if h.Days == nil {
		h.Days = DefaultHours.Days
	}
	if h.Start == "" {
		h.Start = DefaultHours.Start
	}
	if h.End == "" {
		h.End = DefaultHours.End
	}
	var c Calendar
	zone, err := time.LoadLocation(h.Timezone)
	if err != nil || h.Timezone == "Local" {
		return Calendar{}, fmt.Errorf("timezone %q is not an IANA zone name", h.Timezone)
	}
	c.zone = zone
	if len(h.Days) == 0 {
		return Calendar{}, fmt.Errorf("days: name at least one working day")
	}
	for _, d := range h.Days {
		wd, ok := weekdays[d]
		if !ok {
			return Calendar{}, fmt.Errorf("days: %q is not one of mon, tue, wed, thu, fri, sat, sun", d)
		}
		if c.days[wd] {
			return Calendar{}, fmt.Errorf("days: %q is listed twice", d)
		}
		c.days[wd] = true
	}
	if c.start, err = clock(h.Start); err != nil {
		return Calendar{}, fmt.Errorf("start: %w", err)
	}
	if c.end, err = clock(h.End); err != nil {
		return Calendar{}, fmt.Errorf("end: %w", err)
	}
	if c.start >= c.end {
		return Calendar{}, fmt.Errorf("start %s must come before end %s", h.Start, h.End)
	}
	if len(h.Holidays) > MaxHolidays {
		return Calendar{}, fmt.Errorf("holidays: at most %d dates, got %d", MaxHolidays, len(h.Holidays))
	}
	c.holidays = map[int]bool{}
	for _, d := range h.Holidays {
		day, err := time.Parse(time.DateOnly, d)
		if err != nil {
			return Calendar{}, fmt.Errorf("holidays: %q is not a YYYY-MM-DD date", d)
		}
		key := civil(day.Date())
		if c.holidays[key] {
			return Calendar{}, fmt.Errorf("holidays: %q is listed twice", d)
		}
		c.holidays[key] = true
	}
	return c, nil
}

func civil(y int, m time.Month, d int) int { return y*10000 + int(m)*100 + d }

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// WorkingSeconds counts the seconds between from and to that fall in
// working hours. A day's working hours are the wall-clock hours of its own
// date, so a daylight saving change inside them shortens or lengthens that
// day by the real elapsed time. It is 0 when to is not after from.
func (c Calendar) WorkingSeconds(from, to time.Time) int64 {
	if c.zone == nil || !to.After(from) {
		return 0
	}
	local := from.In(c.zone)
	y, m, d := local.Date()
	var total time.Duration
	for {
		dayStart := time.Date(y, m, d, 0, 0, 0, 0, c.zone)
		if !dayStart.Before(to) {
			break
		}
		if c.days[dayStart.Weekday()] && !c.holidays[civil(y, m, d)] {
			open := time.Date(y, m, d, c.start/60, c.start%60, 0, 0, c.zone)
			shut := time.Date(y, m, d, c.end/60, c.end%60, 0, 0, c.zone)
			if open.Before(from) {
				open = from
			}
			if shut.After(to) {
				shut = to
			}
			if shut.After(open) {
				total += shut.Sub(open)
			}
		}
		d++
		next := time.Date(y, m, d, 0, 0, 0, 0, c.zone)
		y, m, d = next.Date()
	}
	return int64(total / time.Second)
}

// CalendarInfo describes the calendar a card's working seconds were counted
// with. Holidays is how many holidays it lists.
type CalendarInfo struct {
	Timezone string   `json:"timezone"`
	Days     []string `json:"days"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Holidays int      `json:"holidays"`
}

// Info describes c.
func (c Calendar) Info() CalendarInfo {
	info := CalendarInfo{Days: []string{}, Holidays: len(c.holidays)}
	if c.zone != nil {
		info.Timezone = c.zone.String()
	}
	for _, wd := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		if c.days[wd] {
			info.Days = append(info.Days, weekdayNames[wd])
		}
	}
	info.Start = fmt.Sprintf("%02d:%02d", c.start/60, c.start%60)
	info.End = fmt.Sprintf("%02d:%02d", c.end/60, c.end%60)
	return info
}

// String names the calendar in logs.
func (c Calendar) String() string {
	info := c.Info()
	return fmt.Sprintf("%s %s %s-%s, %d holidays", info.Timezone, strings.Join(info.Days, ","), info.Start, info.End, info.Holidays)
}
