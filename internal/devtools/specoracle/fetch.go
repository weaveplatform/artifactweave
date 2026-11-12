package specoracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultUserAgent identifies the fetcher to docs.jfrog.com so a polite
// operator can find us if the crawl misbehaves.
const DefaultUserAgent = "artifactweave-specoracle (https://github.com/weaveplatform/artifactweave)"

// ManifestFile is the name of the manifest written into the cache directory.
// Its presence is how TestAccSpecParity decides whether a cache exists.
const ManifestFile = "manifest.json"

// FetchOptions configures Fetch. Zero values pick polite defaults.
type FetchOptions struct {
	// Indexes are llms.txt URLs; each lists endpoint pages as "- [Title](url): text".
	Indexes []string
	// CacheDir receives <index-slug>/<page-slug>.json files and the manifest.
	CacheDir string
	// Client is used for every request; nil means http.DefaultClient with a timeout.
	Client *http.Client
	// Concurrency bounds parallel page fetches (default 4).
	Concurrency int
	// Delay is the pause before each page request (default 200 ms).
	Delay time.Duration
	// UserAgent overrides DefaultUserAgent.
	UserAgent string
	// Retries is the number of attempts on 429 and 5xx responses (default 3).
	Retries int
	// Known maps a family slug to page URLs an earlier good fetch found for
	// that family. When the family's own index fails, they pick its section
	// of the parent index (see fetchFamily); without them a failed index
	// leaves the family missing.
	Known map[string][]string
}

// ManifestEntry records one page of one index.
type ManifestEntry struct {
	Index   string `json:"index"`
	Family  string `json:"family"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	File    string `json:"file,omitempty"`
	Status  int    `json:"status,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Manifest is written as manifest.json and lists what the cache holds.
type Manifest struct {
	FetchedAt time.Time `json:"fetched_at"`
	UserAgent string    `json:"user_agent"`
	// Families has one record per index Fetch was given, so a family that
	// yielded no page is still on record (Missing, CheckFamilies).
	Families []FamilyRecord  `json:"families"`
	Entries  []ManifestEntry `json:"entries"`
}

// Blocks returns the number of entries with a cached OpenAPI block.
func (m Manifest) Blocks() int {
	n := 0
	for _, e := range m.Entries {
		if e.File != "" {
			n++
		}
	}
	return n
}

// Sentinel errors, wrapped with context where they are returned.
var (
	errNoIndexes  = errors.New("specoracle: no indexes given")
	errNoCacheDir = errors.New("specoracle: CacheDir is required")
	errEmptyIndex = errors.New("specoracle: index lists no page")
	// ErrBadStatus is wrapped around unexpected HTTP statuses.
	ErrBadStatus = errors.New("specoracle: unexpected http status")
)

// Fetch downloads every endpoint page listed by the indexes, extracts the
// embedded OpenAPI block and writes it into CacheDir. Pages without a block
// and transport failures are recorded in the manifest rather than aborting,
// because the docs site changes and a single bad page must not hide the rest.
// A family whose index fails is recorded in Families, and its pages come from
// the parent index where Known identifies them.
func Fetch(ctx context.Context, opts FetchOptions) (Manifest, error) {
	if len(opts.Indexes) == 0 {
		return Manifest{}, errNoIndexes
	}
	if opts.CacheDir == "" {
		return Manifest{}, errNoCacheDir
	}
	f := newFetcher(opts)
	if err := os.MkdirAll(opts.CacheDir, 0o750); err != nil {
		return Manifest{}, fmt.Errorf("specoracle: create cache dir: %w", err)
	}
	var entries []ManifestEntry
	families := make([]FamilyRecord, 0, len(opts.Indexes))
	for _, index := range opts.Indexes {
		family, got := f.fetchFamily(ctx, index, opts.Known[FamilySlug(index)])
		families = append(families, family)
		entries = append(entries, got...)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Index != entries[j].Index {
			return entries[i].Index < entries[j].Index
		}
		return entries[i].URL < entries[j].URL
	})
	m := Manifest{
		FetchedAt: time.Now().UTC(),
		UserAgent: f.userAgent,
		Families:  families,
		Entries:   entries,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, fmt.Errorf("specoracle: encode manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(opts.CacheDir, ManifestFile), data, 0o600); err != nil {
		return m, fmt.Errorf("specoracle: write manifest: %w", err)
	}
	return m, nil
}

type fetcher struct {
	client      *http.Client
	cacheDir    string
	concurrency int
	delay       time.Duration
	userAgent   string
	retries     int
	// parents keeps each parent index read for a fallback, because the
	// families of one docs project share it. Families are fetched one after
	// another, so it needs no lock.
	parents map[string]parentIndex
}

func newFetcher(opts FetchOptions) *fetcher {
	f := &fetcher{
		client:      opts.Client,
		cacheDir:    opts.CacheDir,
		concurrency: opts.Concurrency,
		delay:       opts.Delay,
		userAgent:   opts.UserAgent,
		retries:     opts.Retries,
		parents:     map[string]parentIndex{},
	}
	if f.client == nil {
		f.client = &http.Client{Timeout: 60 * time.Second}
	}
	if f.concurrency <= 0 {
		f.concurrency = 4
	}
	if f.delay <= 0 {
		f.delay = 200 * time.Millisecond
	}
	if f.userAgent == "" {
		f.userAgent = DefaultUserAgent
	}
	if f.retries <= 0 {
		f.retries = 3
	}
	return f
}

// linkLine matches the index entries: "- [Title](https://.../page.md): text".
var linkLine = regexp.MustCompile(`^\s*[-*]\s+\[([^\]]*)\]\((https?://[^)\s]+)\)`)

// ParseIndex extracts (title, url) pairs from an llms.txt body.
func ParseIndex(body string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(body, "\n") {
		if m := linkLine.FindStringSubmatch(line); m != nil {
			out = append(out, [2]string{m[1], m[2]})
		}
	}
	return out
}

// fetchFamily fetches the pages one family index lists. When that index
// fails, the family's section of the parent index stands in for it (#1308).
// docs.jfrog.com has renamed families, and the family's llms.txt moved to
// the new name while the parent kept listing the same pages under the new
// heading. Before this, Load then found nothing for the family, so it
// dropped out of the comparison without a failure: its regressions passed
// and its baseline entries read as improvements.
func (f *fetcher) fetchFamily(
	ctx context.Context,
	index string,
	known []string,
) (FamilyRecord, []ManifestEntry) {
	rec := FamilyRecord{Index: index, Family: FamilySlug(index), Source: index}
	links, err := f.indexLinks(ctx, index)
	if err != nil {
		rec.Source = ""
		rec.Error = err.Error()
		parent, section, found, ferr := f.parentLinks(ctx, index, known)
		if ferr != nil {
			rec.Error += "; " + ferr.Error()
			return rec, nil
		}
		rec.Source, rec.Section, links = parent, section, found
	}
	if err := os.MkdirAll(filepath.Join(f.cacheDir, rec.Family), 0o750); err != nil {
		rec.Source = ""
		rec.Error = fmt.Sprintf("specoracle: create family dir: %v", err)
		return rec, nil
	}
	return rec, f.fetchPages(ctx, index, rec.Family, links)
}

// indexLinks reads the page links of one llms.txt. An index that answers
// 200 but lists no page counts as failed: a soft 404 would otherwise drop the
// family as quietly as an error status did.
func (f *fetcher) indexLinks(ctx context.Context, index string) ([][2]string, error) {
	body, status, err := f.get(ctx, index)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("index %s: status %d: %w", index, status, ErrBadStatus)
	}
	links := ParseIndex(string(body))
	if len(links) == 0 {
		return nil, fmt.Errorf("index %s: %w", index, errEmptyIndex)
	}
	return links, nil
}

func (f *fetcher) fetchPages(
	ctx context.Context,
	index, family string,
	links [][2]string,
) []ManifestEntry {
	entries := make([]ManifestEntry, len(links))
	var wg sync.WaitGroup
	sem := make(chan struct{}, f.concurrency)
	for i, link := range links {
		wg.Add(1)
		go func(i int, title, pageURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entries[i] = f.fetchPage(ctx, index, family, title, pageURL)
		}(i, link[0], link[1])
	}
	wg.Wait()
	return entries
}

func (f *fetcher) fetchPage(
	ctx context.Context,
	index, family, title, pageURL string,
) ManifestEntry {
	e := ManifestEntry{Index: index, Family: family, Title: title, URL: pageURL}
	mdURL := pageURL
	if !strings.HasSuffix(mdURL, ".md") {
		mdURL += ".md"
	}
	select {
	case <-ctx.Done():
		e.Error = ctx.Err().Error()
		return e
	case <-time.After(f.delay):
	}
	body, status, err := f.get(ctx, mdURL)
	e.Status = status
	if err != nil {
		e.Error = err.Error()
		return e
	}
	if status != http.StatusOK {
		e.Error = fmt.Sprintf("status %d", status)
		return e
	}
	block, ok := ExtractBlock(string(body))
	if !ok {
		e.Skipped = "no OpenAPI block on page"
		return e
	}
	sum := sha256.Sum256(block)
	e.SHA256 = hex.EncodeToString(sum[:])
	e.File = filepath.ToSlash(filepath.Join(family, PageSlug(pageURL)+".json"))
	if err := os.WriteFile(
		filepath.Join(f.cacheDir, filepath.FromSlash(e.File)),
		block,
		0o600,
	); err != nil {
		e.File = ""
		e.Error = err.Error()
	}
	return e
}

// get performs a GET with retries on 429 and 5xx. The backoff doubles from the
// configured delay so a rate-limited crawl slows down instead of hammering.
func (f *fetcher) get(ctx context.Context, url string) ([]byte, int, error) {
	var lastErr error
	for attempt := range f.retries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, fmt.Errorf("specoracle: fetch %s: %w", url, ctx.Err())
			case <-time.After(f.delay * time.Duration(1<<attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("specoracle: build request %s: %w", url, err)
		}
		req.Header.Set("User-Agent", f.userAgent)
		req.Header.Set("Accept", "text/markdown, text/plain;q=0.9, */*;q=0.1")
		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("status %d: %w", resp.StatusCode, ErrBadStatus)
			continue
		}
		return body, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("%s: giving up after %d attempts: %w", url, f.retries, lastErr)
}

// FamilySlug turns an index URL into a directory name, dropping the host,
// the "reference" segment and the llms.txt file name:
// https://docs.jfrog.com/artifactory/reference/jfrog-artifactory-repositories-api/llms.txt
// becomes artifactory-jfrog-artifactory-repositories-api.
func FamilySlug(index string) string {
	path := strings.TrimPrefix(index, "https://")
	path = strings.TrimPrefix(path, "http://")
	if i := strings.Index(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	var parts []string
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "reference" || seg == "llms.txt" {
			continue
		}
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		return "index"
	}
	return strings.Join(parts, "-")
}

// PageSlug is the last path segment of a page URL without its .md suffix.
func PageSlug(pageURL string) string {
	s := strings.TrimSuffix(pageURL, ".md")
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "page"
	}
	return s
}
