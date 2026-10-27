package remote

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type cacheRules struct {
	mustNotStore   bool
	shouldValidate bool
	mustRevalidate bool
	maxAge         *time.Duration
}

func parseCacheRules(header string) cacheRules {
	var rules cacheRules
	for _, field := range strings.Split(header, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(field), "=")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "no-store":
			rules.mustNotStore = true
		case "no-cache":
			rules.shouldValidate = true
		case "must-revalidate":
			rules.mustRevalidate = true
		case "max-age":
			seconds, err := strconv.ParseInt(strings.Trim(value, " \""), 10, 32)
			if err != nil || seconds < 0 {
				rules.shouldValidate = true
				continue
			}
			age := time.Duration(seconds) * time.Second
			// Repeated contradictory limits take the more restrictive value.
			if rules.maxAge == nil || age < *rules.maxAge {
				rules.maxAge = &age
			}
		}
	}
	return rules
}

func (r cacheRules) fresh(mutable bool, validated, now time.Time, ttl time.Duration) bool {
	if r.mustNotStore || r.shouldValidate || validated.IsZero() || now.Before(validated) {
		return false
	}
	if !mutable && r.maxAge == nil {
		return true
	}
	if r.maxAge != nil && (!mutable || *r.maxAge < ttl) {
		ttl = *r.maxAge
	}
	return ttl > 0 && now.Sub(validated) < ttl
}

func (r cacheRules) permitsStale() bool {
	return !r.mustNotStore && !r.shouldValidate && !r.mustRevalidate
}

// Responses varying on state outside the representation identity cannot be
// shared. Accept is included in that identity; no automatic compression is used.
func cacheableVary(v string) bool {
	for _, name := range strings.Split(v, ",") {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "", "accept", "accept-encoding":
		default:
			return false
		}
	}
	return true
}

func retryAfter(raw string, now time.Time) time.Duration {
	d := time.Second
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32); err == nil {
		d = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(raw); err == nil {
		d = at.Sub(now)
	}
	return max(time.Second, min(300*time.Second, d))
}
