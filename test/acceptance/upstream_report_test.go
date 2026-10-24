package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	upstreamBodyLimit   = 16 << 20
	upstreamReportLimit = 1 << 20
)

type upstreamEvent struct {
	Family    string `json:"family"`
	Phase     string `json:"phase"`
	Category  string `json:"category"`
	Outcome   string `json:"outcome"`
	Assertion string `json:"assertion"`
	Status    int    `json:"status,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Client    string `json:"client,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type upstreamReport struct {
	Revision   string          `json:"revision"`
	RunURL     string          `json:"run_url"`
	HTTPClient string          `json:"http_client"`
	Events     []upstreamEvent `json:"events"`
}

// Reports contain server-owned descriptions and digests, never raw responses,
// commands, credential-bearing URLs or environment variables. Client failures
// retain only a bounded, sanitized diagnostic excerpt.
func writeUpstreamReport(dir string, report upstreamReport) error {
	if len(report.Events) > 1024 {
		return fmt.Errorf("upstream report event budget exceeded")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode upstream report: %w", err)
	}
	if len(data) > upstreamReportLimit {
		return fmt.Errorf("upstream report size budget exceeded")
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	target := filepath.Join(dir, "outcomes.json")
	if err = os.WriteFile(target+".tmp", data, 0o600); err != nil {
		return fmt.Errorf("write upstream report: %w", err)
	}
	if err = os.Rename(target+".tmp", target); err != nil {
		return fmt.Errorf("publish upstream report: %w", err)
	}
	return nil
}

func upstreamStatus(status int, transport bool) string {
	if transport {
		return "upstream_unavailable"
	}
	if status == http.StatusTooManyRequests {
		return "upstream_rate_limit"
	}
	if status >= 500 {
		return "upstream_unavailable"
	}
	if status >= 200 && status < 400 || status == http.StatusUnauthorized {
		return "upstream_reachable"
	}
	return "upstream_contract"
}

func localUpstreamLink(origin, target string) (string, error) {
	base, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("invalid test origin")
	}
	link, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("invalid rewritten link")
	}
	link = base.ResolveReference(link)
	if link.Scheme != base.Scheme || link.Host != base.Host || link.User != nil {
		return "", fmt.Errorf("rewritten link bypasses the repository")
	}
	link.Fragment = ""
	return link.String(), nil
}

type boundedUpstreamOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *boundedUpstreamOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	if len(data) > b.limit-b.data.Len() {
		data = data[:max(0, b.limit-b.data.Len())]
	}
	_, _ = b.data.Write(data)
	return n, nil
}

func (b *boundedUpstreamOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func TestAccUpstreamConformanceReportBoundaries(t *testing.T) {
	for _, tc := range []struct {
		status    int
		transport bool
		want      string
	}{{200, false, "upstream_reachable"}, {401, false, "upstream_reachable"}, {429, false, "upstream_rate_limit"}, {503, false, "upstream_unavailable"}, {404, false, "upstream_contract"}, {0, true, "upstream_unavailable"}} {
		if got := upstreamStatus(tc.status, tc.transport); got != tc.want {
			t.Fatalf("status %d: %s", tc.status, got)
		}
	}
	for _, target := range []string{"https://evil.example/archive", "//evil.example/archive", "http://secret:password@localhost:123/a"} {
		if _, err := localUpstreamLink("http://localhost:123", target); err == nil {
			t.Fatal("external or credential-bearing link admitted")
		}
	}
	if got, err := localUpstreamLink(
		"http://localhost:123",
		"/archive?sig=private#fragment",
	); err != nil ||
		got != "http://localhost:123/archive?sig=private" {
		t.Fatal(got, err)
	}
	output := &boundedUpstreamOutput{limit: 4}
	if n, err := io.WriteString(
		output,
		"123456",
	); err != nil || n != 6 ||
		output.String() != "1234" {
		t.Fatal(n, err, output.String())
	}
	if _, err := io.WriteString(output, "more"); err != nil || output.String() != "1234" {
		t.Fatal(err)
	}
	dir := t.TempDir()
	report := upstreamReport{
		Events: []upstreamEvent{
			{Family: "npm", Outcome: "protocol_assertion", Assertion: "SHA-256 mismatch"},
		},
	}
	if err := writeUpstreamReport(dir, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "outcomes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "SHA-256 mismatch") {
		t.Fatal("missing actionable assertion")
	}
	report.Events = make([]upstreamEvent, 1025)
	if writeUpstreamReport(dir, report) == nil {
		t.Fatal("unbounded events")
	}
	report.Events = []upstreamEvent{{Assertion: strings.Repeat("x", upstreamReportLimit)}}
	if writeUpstreamReport(dir, report) == nil {
		t.Fatal("unbounded report")
	}
}

var (
	upstreamDiagnosticURL = regexp.MustCompile(`(?i)(?:https?|oci)://[^\s<>"']+`)
	upstreamDiagnosticJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
)

func upstreamDiagnostic(raw string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			raw = strings.ReplaceAll(raw, secret, "[redacted]")
		}
	}
	raw = upstreamDiagnosticURL.ReplaceAllString(raw, "[url]")
	raw = upstreamDiagnosticJWT.ReplaceAllString(raw, "[token]")
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "authorization:") || strings.Contains(lower, "cookie:") ||
			strings.Contains(lower, "password=") ||
			strings.Contains(lower, "token=") {
			lines[i] = "[credential-bearing line omitted]"
		}
	}
	raw = strings.Join(lines, "\n")
	raw = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, raw)
	if len(raw) > 4096 {
		raw = raw[:4096]
	}
	return raw
}

func upstreamRequestBudget(next http.Handler, count *atomic.Int64, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) > limit {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func TestAccUpstreamConformanceDiagnosticAndBudget(t *testing.T) {
	raw := "signature verification failed\nhttps://user:secret@example.org/a?sig=private\nAuthorization: Bearer private\nsecret eyJabc.def.ghi\x1b"
	clean := upstreamDiagnostic(raw, "secret")
	for _, secret := range []string{"secret", "example.org", "private", "eyJabc", "\x1b"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("diagnostic retained sensitive content %q", secret)
		}
	}
	if !strings.Contains(clean, "signature verification failed") {
		t.Fatal("diagnostic lost useful failure")
	}
	if len(upstreamDiagnostic(strings.Repeat("x", 8192))) != 4096 {
		t.Fatal("unbounded diagnostic")
	}
	var count, served atomic.Int64
	handler := upstreamRequestBudget(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { served.Add(1); w.WriteHeader(204) },
		),
		&count,
		4,
	)
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			r := httptest.NewRecorder()
			handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/", nil))
			if r.Code != 204 && r.Code != 429 {
				t.Errorf("unexpected status %d", r.Code)
			}
		})
	}
	workers.Wait()
	if count.Load() != 12 || served.Load() != 4 {
		t.Fatal("request budget did not contain concurrent requests")
	}
}
