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

// MustNotStore reports whether a response with header h may not be kept:
// its Cache-Control says no-store, or its Vary names anything but Accept and
// Accept-Encoding, so it could differ by request state that the cache
// identity leaves out. The remote cache does not use it for what it stores
// (mustNotPersist); the smart remote's discovery documents still do.
func MustNotStore(h http.Header) bool {
	return parseCacheRules(h.Get("Cache-Control")).mustNotStore || !cacheableVary(h.Get("Vary"))
}

// mustNotPersist reports whether the remote cache may not keep an upstream
// answer with header h: only when its Vary names anything but Accept and
// Accept-Encoding. An upstream Cache-Control: no-store does not stop it.
// 6.20's remote fetch (HttpRepo) never reads the upstream's Cache-Control,
// and 7.x sends no-store on every Docker manifest (RTFACT-27199, 7.33.12)
// and on Maven metadata and snapshots (RTFACT-15016, RTFACT-6983) for the
// proxies in front of it (RTFACT-17758), while a 7.x remote over a 7.x
// upstream still caches them (RTFACT-30785, an edge's smart remote over its
// main server). Honouring it here would leave a remote over artifactweave
// or Artifactory with nothing to serve offline.
func mustNotPersist(h http.Header) bool { return !cacheableVary(h.Get("Vary")) }

// persistedCacheControl is the upstream Cache-Control value v as the remote
// cache keeps it beside an entry: without no-store, for the reasons
// mustNotPersist gives, so that the entry's freshness follows the
// repository's retrieval cache period and a failed upstream may still be
// answered from it. no-cache, must-revalidate and max-age are kept: they
// stay [I] RC-3. The cache's own no-store marker for an uncacheable Vary is
// written after this, on the stored entry.
func persistedCacheControl(v string) string {
	var kept []string
	for _, field := range strings.Split(v, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(field), "=")
		if strings.EqualFold(strings.TrimSpace(name), "no-store") {
			continue
		}
		if f := strings.TrimSpace(field); f != "" {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, ", ")
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

// RetryAfter reads a Retry-After header value, in seconds or as an HTTP
// date measured from now, and bounds the pause to between 1 and 300
// seconds; an empty or unreadable value means 1 second. The circuit, the
// policy's 429 answers and the registry token exchange share it, so every
// upstream pause is bounded alike.
func RetryAfter(raw string, now time.Time) time.Duration {
	d := time.Second
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32); err == nil {
		d = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(raw); err == nil {
		d = at.Sub(now)
	}
	return max(time.Second, min(300*time.Second, d))
}
