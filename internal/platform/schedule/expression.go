package schedule

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Quartz's published year range bounds both input and next-fire searches.
const (
	FirstYear     = 1970
	LastYear      = 2099
	MaxExpression = 1024
)

// ErrNoFuture reports a valid expression without another occurrence.
var ErrNoFuture = errors.New("schedule: no future fire")

// Expression is an immutable six/seven-field Quartz expression. Parse supplies
// UTC when the zone is empty. Next skips nonexistent civil times and uses only
// the first instant of a repeated civil time. Construct expressions with Parse.
type Expression struct {
	text, zone                             string
	loc                                    *time.Location
	seconds, minutes, hours, months, years []int
	day                                    dayRule
}

type dayRule struct {
	mode   string
	values []int
	n      int
}

// Parse validates Quartz seconds/minutes/hours/day/month/weekday[/year]. It
// accepts ranges, lists, increments, case-insensitive names and day modifiers
// ?, L, L-offset, W, LW and #. Exactly one day field must be unspecified (?);
// five-field cron, aliases and embedded timezone directives are not Quartz.
func Parse(expression, zone string) (Expression, error) {
	bad := func() (Expression, error) {
		return Expression{}, fmt.Errorf("schedule: invalid Quartz expression: %w", kernel.ErrInvalid)
	}
	if len(expression) > MaxExpression {
		return bad()
	}
	parts := strings.Fields(strings.ToUpper(expression))
	if len(parts) != 6 && len(parts) != 7 {
		return bad()
	}
	if (parts[3] == "?") == (parts[5] == "?") {
		return bad()
	}
	for i, p := range parts {
		if i != 3 && i != 5 && strings.Contains(p, "?") {
			return bad()
		}
	}
	if zone == "" {
		zone = "UTC"
	}
	if zone == "Local" {
		return Expression{}, fmt.Errorf("schedule: use a named timezone: %w", kernel.ErrInvalid)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Expression{}, fmt.Errorf("schedule: invalid timezone: %w", kernel.ErrInvalid)
	}
	e := Expression{text: strings.Join(parts, " "), zone: zone, loc: loc}
	for _, f := range []struct {
		raw    string
		lo, hi int
		names  []string
		target *[]int
	}{
		{parts[0], 0, 59, nil, &e.seconds},
		{parts[1], 0, 59, nil, &e.minutes},
		{parts[2], 0, 23, nil, &e.hours},
		{parts[4], 1, 12, strings.Split("JAN FEB MAR APR MAY JUN JUL AUG SEP OCT NOV DEC", " "), &e.months},
	} {
		v, ok := parseField(f.raw, f.lo, f.hi, f.names)
		if !ok {
			return bad()
		}
		*f.target = v
	}
	year := "*"
	if len(parts) == 7 {
		year = parts[6]
	}
	if e.years, err = parseYears(year); err != nil {
		return bad()
	}
	if parts[5] == "?" {
		e.day, err = parseMonthDay(parts[3])
	} else {
		e.day, err = parseWeekDay(parts[5])
	}
	if err != nil {
		return bad()
	}
	return e, nil
}

// String returns the normalized expression without changing its field count.
func (e Expression) String() string { return e.text }

// Zone returns the IANA timezone used to interpret civil schedule fields.
func (e Expression) Zone() string { return e.zone }

func parseYears(raw string) ([]int, error) {
	values, ok := parseField(raw, FirstYear, LastYear, nil)
	if !ok {
		return nil, kernel.ErrInvalid
	}
	return values, nil
}

func parseField(raw string, lo, hi int, names []string) ([]int, bool) {
	seen := make([]bool, hi+1)
	for _, part := range strings.Split(raw, ",") {
		step := 1
		base := strings.Split(part, "/")
		if len(base) > 2 {
			return nil, false
		}
		if len(base) == 2 {
			var ok bool
			maxStep := hi
			if lo == FirstYear {
				maxStep = math.MaxInt32
			}
			step, ok = fieldNumber(base[1], 1, maxStep, nil)
			if !ok {
				return nil, false
			}
		}
		start, end := lo, hi
		if base[0] != "*" {
			r := strings.Split(base[0], "-")
			if len(r) > 2 {
				return nil, false
			}
			var ok bool
			start, ok = fieldNumber(r[0], lo, hi, names)
			if !ok {
				return nil, false
			}
			end = start
			if len(r) == 2 {
				end, ok = fieldNumber(r[1], lo, hi, names)
				if !ok {
					return nil, false
				}
			} else if len(base) == 2 {
				end = hi
			}
		}
		width := end - start + 1
		if end < start {
			if lo == FirstYear {
				return nil, false
			}
			width = hi - start + 1 + end - lo + 1
		}
		for offset := 0; offset < width; offset += step {
			v := start + offset
			if v > hi {
				v = lo + v - hi - 1
			}
			seen[v] = true
		}
	}
	var values []int
	for v := lo; v <= hi; v++ {
		if seen[v] {
			values = append(values, v)
		}
	}
	return values, len(values) > 0
}

func fieldNumber(raw string, lo, hi int, names []string) (int, bool) {
	if i := slices.Index(names, raw); i >= 0 {
		return lo + i, true
	}
	if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	return v, err == nil && v >= lo && v <= hi
}

func parseMonthDay(raw string) (dayRule, error) {
	switch raw {
	case "L":
		return dayRule{mode: "last"}, nil
	case "LW":
		return dayRule{mode: "last weekday"}, nil
	}
	if strings.HasPrefix(raw, "L-") {
		n, ok := fieldNumber(strings.TrimPrefix(raw, "L-"), 0, 30, nil)
		if ok {
			return dayRule{mode: "last", n: n}, nil
		}
		return dayRule{}, kernel.ErrInvalid
	}
	if strings.HasSuffix(raw, "W") {
		n, ok := fieldNumber(strings.TrimSuffix(raw, "W"), 1, 31, nil)
		if ok {
			return dayRule{mode: "nearest", n: n}, nil
		}
		return dayRule{}, kernel.ErrInvalid
	}
	values, ok := parseField(raw, 1, 31, nil)
	if !ok {
		return dayRule{}, kernel.ErrInvalid
	}
	return dayRule{mode: "month", values: values}, nil
}

func parseWeekDay(raw string) (dayRule, error) {
	names := strings.Split("SUN MON TUE WED THU FRI SAT", " ")
	if raw == "L" {
		raw = "SAT"
	}
	if strings.HasSuffix(raw, "L") {
		n, ok := fieldNumber(strings.TrimSuffix(raw, "L"), 1, 7, names)
		if ok {
			return dayRule{mode: "last of week", n: n}, nil
		}
		return dayRule{}, kernel.ErrInvalid
	}
	if pair := strings.Split(raw, "#"); len(pair) == 2 {
		weekday, ok := fieldNumber(pair[0], 1, 7, names)
		n, ordinalOK := fieldNumber(pair[1], 1, 5, nil)
		if ok && ordinalOK {
			return dayRule{mode: "ordinal", values: []int{weekday}, n: n}, nil
		}
		return dayRule{}, kernel.ErrInvalid
	}
	values, ok := parseField(raw, 1, 7, names)
	if !ok {
		return dayRule{}, kernel.ErrInvalid
	}
	return dayRule{mode: "week", values: values}, nil
}

func (r dayRule) matches(year int, month time.Month, day, last int) bool {
	weekday := func(d int) int { return int(time.Date(year, month, d, 12, 0, 0, 0, time.UTC).Weekday()) + 1 }
	switch r.mode {
	case "month":
		return slices.Contains(r.values, day)
	case "week":
		return slices.Contains(r.values, weekday(day))
	case "last":
		return day == last-r.n
	case "last of week":
		return weekday(day) == r.n && day+7 > last
	case "ordinal":
		return weekday(day) == r.values[0] && (day-1)/7+1 == r.n
	case "last weekday":
		switch weekday(last) {
		case 7:
			last--
		case 1:
			last -= 2
		}
		return day == last
	case "nearest":
		if r.n > last {
			return false
		}
		target := r.n
		switch weekday(target) {
		case 7:
			if target == 1 {
				target += 2
			} else {
				target--
			}
		case 1:
			if target == last {
				target -= 2
			} else {
				target++
			}
		}
		return day == target
	default:
		return false
	}
}

// Next returns the first occurrence strictly after after, or ErrNoFuture.
// Searching real calendar dates prevents a February 29/31 from silently
// normalizing into March. The finite year range also bounds impossible input.
func (e Expression) Next(after time.Time) (time.Time, error) {
	if e.loc == nil {
		return time.Time{}, fmt.Errorf("schedule: unparsed expression: %w", kernel.ErrInvalid)
	}
	local := after.In(e.loc)
	for _, year := range e.years {
		if year < local.Year() {
			continue
		}
		for _, month := range e.months {
			if year == local.Year() && month < int(local.Month()) {
				continue
			}
			last := time.Date(year, time.Month(month)+1, 0, 12, 0, 0, 0, time.UTC).Day()
			for day := 1; day <= last; day++ {
				if year == local.Year() && month == int(local.Month()) && day < local.Day() {
					continue
				}
				if !e.day.matches(year, time.Month(month), day, last) {
					continue
				}
				start := 0
				if year == local.Year() && month == int(local.Month()) && day == local.Day() {
					start = local.Hour()*3600 + local.Minute()*60 + local.Second() + 1
				}
				if next, ok := e.nextClock(year, time.Month(month), day, start, after); ok {
					return next, nil
				}
			}
		}
	}
	return time.Time{}, ErrNoFuture
}

func (e Expression) nextClock(year int, month time.Month, day, start int, after time.Time) (time.Time, bool) {
	for _, hour := range e.hours {
		if hour*3600+3599 < start {
			continue
		}
		for _, minute := range e.minutes {
			if hour*3600+minute*60+59 < start {
				continue
			}
			for _, second := range e.seconds {
				if hour*3600+minute*60+second < start {
					continue
				}
				candidate := time.Date(year, month, day, hour, minute, second, 0, e.loc)
				if !sameCivil(candidate, year, month, day, hour, minute, second) {
					continue
				}
				// Date may choose either side of a fold. Check the adjacent zone
				// offsets and retain the first instant with these civil fields.
				base := candidate
				begin, end := base.ZoneBounds()
				_, offset := base.Zone()
				var adjacent []time.Time
				if !begin.IsZero() {
					adjacent = append(adjacent, begin.Add(-time.Nanosecond))
				}
				if !end.IsZero() {
					adjacent = append(adjacent, end)
				}
				for _, boundary := range adjacent {
					_, other := boundary.Zone()
					alternative := base.Add(time.Duration(offset-other) * time.Second)
					if alternative.Before(candidate) && sameCivil(alternative, year, month, day, hour, minute, second) {
						candidate = alternative
					}
				}
				if candidate.After(after) {
					return candidate.UTC(), true
				}
			}
		}
	}
	return time.Time{}, false
}

func sameCivil(t time.Time, year int, month time.Month, day, hour, minute, second int) bool {
	return t.Year() == year && t.Month() == month && t.Day() == day && t.Hour() == hour && t.Minute() == minute && t.Second() == second
}
