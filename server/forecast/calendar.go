package forecast

import (
	"math"
	"time"
)

// Dates are calendar days, held as midnight UTC so arithmetic never trips over DST. "Today" is
// decided by the caller in whatever timezone the deployment plans in.

// Day returns t's calendar date as midnight UTC.
func Day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

// ParseDate parses YYYY-MM-DD.
func ParseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

func FormatDate(t time.Time) string { return t.Format("2006-01-02") }

func isWeekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

func nextDay(t time.Time) time.Time { return t.AddDate(0, 0, 1) }

// NextWorkday is the first working day on or after t.
func NextWorkday(t time.Time) time.Time {
	x := Day(t)
	for isWeekend(x) {
		x = nextDay(x)
	}
	return x
}

// AddWorkdays moves n working days forward from the first working day on or after t.
func AddWorkdays(t time.Time, n int) time.Time {
	x := NextWorkday(t)
	for i := 0; i < n; {
		x = nextDay(x)
		if !isWeekend(x) {
			i++
		}
	}
	return x
}

// WorkdaysBetween counts working days from a to b (positive when b is later), or 0 if b is not after a.
func WorkdaysBetween(a, b time.Time) int {
	n := 0
	end := Day(b)
	for x := Day(a); x.Before(end); x = nextDay(x) {
		if !isWeekend(nextDay(x)) {
			n++
		}
	}
	return n
}

// DateAt is the calendar day a plan `days` working days long finishes on (nil when nothing is left):
// work starts today, and a 1-day task finishes today.
func DateAt(today time.Time, days float64) *time.Time {
	if days <= 0 {
		return nil
	}
	d := AddWorkdays(NextWorkday(today), int(math.Ceil(days))-1)
	return &d
}

type IssueForecast struct {
	Start  time.Time
	Finish time.Time
	// Working days past the due date the forecast lands (0 when on time or undated).
	DaysLate int
}

// Forecast converts the critical-path schedule (in working days from now) into calendar dates.
// A task with earliest finish ef completes at the end of working day ceil(ef).
func Forecast(a *Analysis, dueDates map[string]string, today time.Time) map[string]IssueForecast {
	origin := NextWorkday(today)
	out := make(map[string]IssueForecast, len(a.Order))
	for _, id := range a.Order {
		s := a.Schedule[id]
		f := IssueForecast{
			Start:  AddWorkdays(origin, int(math.Floor(s.EarliestStart))),
			Finish: AddWorkdays(origin, max(0, int(math.Ceil(s.EarliestFinish))-1)),
		}
		if due, ok := dueDates[id]; ok && due != "" {
			if d, err := ParseDate(due); err == nil {
				f.DaysLate = WorkdaysBetween(d, f.Finish)
			}
		}
		out[id] = f
	}
	return out
}
