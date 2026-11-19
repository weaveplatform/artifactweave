package schedule_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

func instant(t *testing.T, raw string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUnitQuartzCalendarConformance(t *testing.T) {
	for _, tc := range []struct{ name, expression, after, want string }{
		{"documented policy", "0 0 2 ? * MON-SAT *", "2026-11-21T02:00:00Z", "2026-11-23T02:00:00Z"},
		{"six fields", "0 15 10 * * ?", "2026-11-19T10:15:00Z", "2026-11-20T10:15:00Z"},
		{"seconds", "0/5 * * * * ?", "2026-11-19T10:15:00.5Z", "2026-11-19T10:15:05Z"},
		{"ranges lists steps", "0 0/5 14,18 * * ?", "2026-11-19T14:55:00Z", "2026-11-19T18:00:00Z"},
		{"named months", "0 10,44 14 ? MAR WED", "2026-03-04T14:10:00Z", "2026-03-04T14:44:00Z"},
		{"named range", "0 15 10 ? * MON-FRI", "2026-11-20T10:15:00Z", "2026-11-23T10:15:00Z"},
		{"overflow range", "0 0 22-2 * * ?", "2026-11-19T23:00:00Z", "2026-11-20T00:00:00Z"},
		{"month wrap", "0 0 0 1 NOV-FEB ?", "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"leap day", "0 0 0 29 2 ?", "2027-02-28T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"non leap century", "0 0 0 29 2 ? 2096-2099", "2096-02-29T00:00:00Z", ""},
		{"impossible February", "0 0 0 31 2 ?", "2026-01-01T00:00:00Z", ""},
		{"last day", "0 0 0 L * ?", "2026-02-01T00:00:00Z", "2026-02-28T00:00:00Z"},
		{"last leap day", "0 0 0 L FEB ?", "2028-02-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"last offset", "0 0 0 L-2 * ?", "2026-02-01T00:00:00Z", "2026-02-26T00:00:00Z"},
		{"zero offset", "0 0 0 L-0 * ?", "2026-02-01T00:00:00Z", "2026-02-28T00:00:00Z"},
		{"offset absent", "0 0 0 L-30 FEB ?", "2026-01-01T00:00:00Z", ""},
		{"weekday Saturday boundary", "0 0 0 1W * ?", "2027-04-30T00:00:00Z", "2027-05-03T00:00:00Z"},
		{"weekday Saturday", "0 0 0 15W * ?", "2026-08-01T00:00:00Z", "2026-08-14T00:00:00Z"},
		{"weekday Sunday", "0 0 0 15W * ?", "2026-02-01T00:00:00Z", "2026-02-16T00:00:00Z"},
		{"weekday Sunday boundary", "0 0 0 31W * ?", "2026-05-01T00:00:00Z", "2026-05-29T00:00:00Z"},
		{"weekday actual weekday", "0 0 0 15W * ?", "2026-12-01T00:00:00Z", "2026-12-15T00:00:00Z"},
		{"weekday absent", "0 0 0 31W FEB ?", "2026-01-01T00:00:00Z", ""},
		{"last weekday Saturday", "0 0 0 LW * ?", "2026-02-01T00:00:00Z", "2026-02-27T00:00:00Z"},
		{"last weekday Sunday", "0 0 0 LW * ?", "2026-05-01T00:00:00Z", "2026-05-29T00:00:00Z"},
		{"last weekday actual", "0 0 0 LW * ?", "2026-11-01T00:00:00Z", "2026-11-30T00:00:00Z"},
		{"last Friday", "0 0 0 ? * 6L", "2026-02-01T00:00:00Z", "2026-02-27T00:00:00Z"},
		{"last named Friday", "0 0 0 ? * FRIL", "2026-02-01T00:00:00Z", "2026-02-27T00:00:00Z"},
		{"L alone is Saturday", "0 0 0 ? * L", "2026-02-01T00:00:00Z", "2026-02-07T00:00:00Z"},
		{"third Friday", "0 0 0 ? * 6#3", "2026-02-01T00:00:00Z", "2026-02-20T00:00:00Z"},
		{"fifth absent in month", "0 0 0 ? * MON#5", "2026-02-01T00:00:00Z", "2026-03-30T00:00:00Z"},
		{"year", "0 0 0 1 1 ? 2028", "2026-01-01T00:00:00Z", "2028-01-01T00:00:00Z"},
		{"year increment", "0 0 0 1 1 ? 2026/2", "2026-01-01T00:00:00Z", "2028-01-01T00:00:00Z"},
		{"large year increment", "0 0 0 1 1 ? 2026/200", "2025-12-31T00:00:00Z", "2026-01-01T00:00:00Z"},
		{"large year increment expires", "0 0 0 1 1 ? 2026/200", "2026-01-01T00:00:00Z", ""},
		{"expired year", "0 0 0 1 1 ? 2025", "2026-01-01T00:00:00Z", ""},
		{"expired range", "0 0 0 * * ?", "2100-01-01T00:00:00Z", ""},
		{"first supported year", "0 0 0 * * ?", "1960-01-01T00:00:00Z", "1970-01-01T00:00:00Z"},
		{"last supported second", "59 59 23 31 DEC ? 2099", "2099-12-31T23:59:58Z", "2099-12-31T23:59:59Z"},
		{"wildcard year end", "* * * * * ?", "2099-12-31T23:59:59.1Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := schedule.Parse(tc.expression, "")
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.Next(instant(t, tc.after))
			if tc.want == "" {
				if !errors.Is(err, schedule.ErrNoFuture) || !got.IsZero() {
					t.Fatalf("next=%v, error=%v; want expired", got, err)
				}
				return
			}
			if err != nil || !got.Equal(instant(t, tc.want)) {
				t.Fatalf("next=%v, error=%v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestUnitQuartzTimezoneAndDST(t *testing.T) {
	for _, tc := range []struct{ name, zone, expression, after, want string }{
		{"NY spring gap", "America/New_York", "0 30 2 * * ?", "2026-03-08T06:59:59Z", "2026-03-09T06:30:00Z"},
		{"NY first fold", "America/New_York", "0 30 1 * * ?", "2026-11-01T05:29:59Z", "2026-11-01T05:30:00Z"},
		{"NY skip repeated", "America/New_York", "0 30 1 * * ?", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
		{"NY after second fold begins", "America/New_York", "0 30 1 * * ?", "2026-11-01T06:15:00Z", "2026-11-02T06:30:00Z"},
		{"Berlin spring gap", "Europe/Berlin", "0 30 2 * * ?", "2026-03-29T00:59:59Z", "2026-03-30T00:30:00Z"},
		{"Berlin first fold", "Europe/Berlin", "0 30 2 * * ?", "2026-10-25T00:29:59Z", "2026-10-25T00:30:00Z"},
		{"Berlin skip repeated", "Europe/Berlin", "0 30 2 * * ?", "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"},
		{"fixed offset", "Asia/Kathmandu", "0 0 12 * * ?", "2026-11-19T00:00:00Z", "2026-11-19T06:15:00Z"},
		{"skipped civil date", "Pacific/Apia", "0 0 12 * * ?", "2011-12-30T00:00:00Z", "2011-12-30T22:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := schedule.Parse(tc.expression, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.Next(instant(t, tc.after))
			if err != nil || !got.Equal(instant(t, tc.want)) {
				t.Fatalf("next=%v, error=%v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestUnitQuartzValidation(t *testing.T) {
	for _, raw := range []string{
		"", "* * * * *", "@daily", "CRON_TZ=UTC 0 0 0 * * ?", "0 0 0 * * *", "0 0 0 ? * ?",
		"? 0 0 * * ?", "0 0 ? * * ?", "0 0 0 * ? ?", "60 0 0 * * ?", "0 60 0 * * ?", "0 0 24 * * ?",
		"0 0 0 0 * ?", "0 0 0 * 13 ?", "0 0 0 ? * 0", "0 0 0 1, * ?", "0 0 0 -1 * ?", "0 0 0 L-31 * ?",
		"0 0 0 LW,1 * ?", "0 0 0 W * ?", "0 0 0 32W * ?", "0 0 0 ? * 8L", "0 0 0 ? * 1#0", "0 0 0 ? * 1#6",
		"0 0 0 ? * 1#2#3", "0 0 0 ? * FOO", "0 0 0 * * ? 1969", "0 0 0 * * ? 2100", "0 0 0 * * ? 2028-2026",
		"0/0 * * * * ?", "0/61 * * * * ?", "*/x * * * * ?", "0/2/3 * * * * ?", "0-1-2 * * * * ?",
		"+1 0 0 * * ?", "0/+2 * * * * ?", "0 0 0 L-+2 * ?", "0 0 0 ? * 1#+2",
		"*/60 * * * * ?", "0 */60 * * * ?", "0 0 */24 * * ?", "0 0 0 * * ? 2026/2147483648",
		"0 0 0 1-FOO * ?", "0 0 0 L-FOO * ?", "0 0 0 ? * FOO#2", strings.Repeat("*", schedule.MaxExpression+1),
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := schedule.Parse(raw, ""); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("accepted invalid expression %q: %v", raw, err)
			}
		})
	}
	if _, err := schedule.Parse("0 0 0 * * ?", "invalid/timezone"); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("timezone error=%v", err)
	}
	var zero schedule.Expression
	if _, err := schedule.Parse("0 0 0 * * ?", "Local"); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("process-local timezone error=%v", err)
	}
	if _, err := zero.Next(time.Now()); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("zero expression error=%v", err)
	}
	e, err := schedule.Parse(" 0\t0 2 ? * mon-sat * ", "")
	if err != nil || e.String() != "0 0 2 ? * MON-SAT *" || e.Zone() != "UTC" {
		t.Fatalf("normalization=%v, error=%v", e, err)
	}
}

func TestUnitQuartzConcurrentEvaluation(t *testing.T) {
	e, err := schedule.Parse("0 0 2 ? * MON-SAT *", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				got, err := e.Next(instant(t, "2026-11-21T02:00:00Z"))
				if err != nil || !got.Equal(instant(t, "2026-11-23T02:00:00Z")) {
					t.Errorf("concurrent next=%v, error=%v", got, err)
				}
			}
		})
	}
	wg.Wait()
}
