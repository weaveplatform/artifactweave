package acceptance

import (
	"crypto/md5"  //nolint:gosec // Protocol checksum
	"crypto/sha1" //nolint:gosec // Protocol checksum
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// This controlled HTTP peer qualifies artifactweave behavior. Its replies are
// deliberately not described as observed Artifactory 7.x wire captures.
type smartPeerFixture struct {
	mu          sync.Mutex
	color       string
	present     bool
	downloads   int64
	properties  int
	requests    int
	unavailable bool
}

func (w *world) registerSmartRemote(sc *godog.ScenarioContext) {
	sc.Step(`^a controlled Smart Remote peer serves an artifact$`, w.startSmartPeer)
	sc.Step(
		`^the Smart peer property becomes "([^"]*)"$`,
		func(color string) { w.smartPeer.mu.Lock(); defer w.smartPeer.mu.Unlock(); w.smartPeer.color = color },
	)
	sc.Step(
		`^the Smart peer removes its origin artifact$`,
		func() { w.smartPeer.mu.Lock(); defer w.smartPeer.mu.Unlock(); w.smartPeer.present = false },
	)
	sc.Step(
		`^the Smart peer becomes unavailable$`,
		func() { w.smartPeer.mu.Lock(); defer w.smartPeer.mu.Unlock(); w.smartPeer.unavailable = true },
	)
	sc.Step(`^the Smart peer eventually records (\d+) downloads$`, w.smartDownloads)
	sc.Step(`^the Smart status contains a confirmed absent representation$`, w.smartAbsentStatus)
	sc.Step(`^the Smart peer received (\d+) property requests$`, func(want int) error {
		w.smartPeer.mu.Lock()
		defer w.smartPeer.mu.Unlock()
		if w.smartPeer.properties != want {
			return fmt.Errorf("property requests %d, want %d", w.smartPeer.properties, want)
		}
		return nil
	})
}

// JFrog and curl can select distinct HTTP representations. The diagnostic is
// per representation and its first row need not be the one curl just refreshed.
func (w *world) smartAbsentStatus() error {
	var status struct {
		Representations []struct {
			OriginAbsent    *bool     `json:"origin_absent"`
			OriginCheckedAt time.Time `json:"origin_checked_at"`
		} `json:"representations"`
	}
	if err := json.Unmarshal(w.body, &status); err != nil {
		return err
	}
	for _, representation := range status.Representations {
		if representation.OriginAbsent != nil && *representation.OriginAbsent &&
			!representation.OriginCheckedAt.IsZero() {
			return nil
		}
	}
	return fmt.Errorf("no confirmed absent representation in Smart status: %s", w.body)
}

func (w *world) startSmartPeer() {
	w.smartPeer = &smartPeerFixture{color: "blue", present: true}
	r := chi.NewRouter()
	r.HandleFunc("/*", w.smartPeer.serve)
	server := httptest.NewServer(r)
	w.closers = append(w.closers, server.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["smart_upstream"] = server.URL + "/artifactory/source"
}

func (p *smartPeerFixture) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	username, password, ok := r.BasicAuth()
	if !ok || username != "peer" || password != "peer-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if p.unavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/artifactory/api/system/version":
		_, _ = io.WriteString(w, `{"version":"7.133.8"}`)
	case "/artifactory/api/repositories/source":
		_, _ = io.WriteString(w, `{"rclass":"local"}`)
	case "/artifactory/api/storage/source":
		_, _ = io.WriteString(
			w,
			`{"repo":"source","path":"/","children":[{"uri":"/file.bin","folder":false}]}`,
		)
	case "/artifactory/api/search/aql":
		p.search(w, r)
	case "/artifactory/api/storage/source/file.bin":
		if r.URL.Query().Has("properties") {
			p.properties++
		}
		if !p.present {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Has("properties") {
			_ = json.NewEncoder(w).
				Encode(map[string]any{"properties": map[string][]string{"color": {p.color}}})
			return
		}
		_, _ = io.WriteString(w, `{"repo":"source","path":"/file.bin"}`)
	case "/artifactory/api/storage/statistics/source":
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var stats []wire.SmartStatistics
		if json.NewDecoder(r.Body).Decode(&stats) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, stat := range stats {
			if stat.RepoPath != "file.bin" || stat.DownloadCount < 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			p.downloads += stat.DownloadCount
		}
	case "/artifactory/source/file.bin":
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "controlled Smart Remote artifact")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (w *world) smartDownloads(want int64) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.smartPeer.mu.Lock()
		got := w.smartPeer.downloads
		w.smartPeer.mu.Unlock()
		if got == want {
			return nil
		}
		if got > want || time.Now().After(deadline) {
			return fmt.Errorf("peer downloads %d, want %d", got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *smartPeerFixture) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "text/plain" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, aql.MaxLength+1))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	q, err := aql.Parse(string(body))
	if err != nil || q.Transitive || q.Action != aql.Find {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	target, err := q.TransitiveTarget()
	if err != nil || target != "source" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data := []byte("controlled Smart Remote artifact")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	corpus := []aql.Hit{}
	if p.present {
		corpus = append(
			corpus,
			aql.Hit{
				Item: types.Item{
					RepoKey:  "source",
					Path:     ".",
					Name:     "file.bin",
					Size:     int64(len(data)),
					Created:  now,
					Modified: now,
					Updated:  now,
					Actual: types.Checksums{
						SHA1:   fmt.Sprintf("%x", sha1.Sum(data)),
						MD5:    fmt.Sprintf("%x", md5.Sum(data)),
						SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
					},
				},
				Properties: types.Properties{"color": {p.color}},
			},
		)
	}
	_ = json.NewEncoder(w).Encode(q.Response(aql.Select(q, aql.Options{Now: now}, corpus)))
}
