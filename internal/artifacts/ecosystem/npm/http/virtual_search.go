package npmhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// This ceiling bounds malformed/non-progressing upstream work independently of
// the eligible-result cap. Hidden or duplicate rows do not consume that cap.
// [I] No 7.x source states a native-search fanout bound (#216).
const remoteSearchPages = 32

type searchContribution struct {
	object    json.RawMessage
	member    *http.Request
	path      string
	authority *source.Authority
}
type searchHit = npmfmt.SearchHit[searchContribution]

func searchBudget(q searchQuery) int {
	if q.from >= searchPage || q.size >= searchPage-q.from {
		return searchPage
	}
	return q.from + q.size
}

func searchPassedOver(err error) bool {
	if errors.Is(err, format.ErrStorageFault) {
		return false
	}
	if virtualPassedOver(err) || errors.Is(err, source.ErrSuperseded) {
		return true
	}
	var answer *apierror.Error
	return errors.As(err, &answer) && (answer.Status == http.StatusServiceUnavailable || answer.Status == http.StatusTooManyRequests)
}

func (h handler) virtualSearch(w http.ResponseWriter, r *http.Request) {
	q := parseSearch(r.URL.Query())
	needed := searchBudget(q)
	blocked := map[string]map[string]bool{}
	pages := map[string]int{}
	for {
		if err := r.Context().Err(); err != nil {
			h.k.Error(w, r, err)
			return
		}
		members, err := h.k.MergeMembers(r, "")
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		parts := []virtual.Part[[]searchHit]{}
		owned := map[string]bool{}
		for _, mr := range members {
			c := pkgroute.RequestFrom(mr).Repo
			if c.Offline != nil && *c.Offline {
				continue
			}
			excluded := make(map[string]bool, len(owned)+len(blocked[c.Key]))
			for name := range owned {
				excluded[name] = true
			}
			for name := range blocked[c.Key] {
				excluded[name] = true
			}
			var hits []searchHit
			if c.Class == repo.Remote {
				hits, err = h.remoteSearchHits(r, mr, q, needed, excluded, pages)
			} else {
				hits, err = h.localSearchHits(r, mr, q, needed, excluded)
			}
			if err != nil {
				if searchPassedOver(err) {
					h.k.WarnPassedOver(mr, err)
					continue
				}
				h.k.Error(w, r, err)
				return
			}
			for _, hit := range hits {
				owned[hit.Name] = true
			}
			parts = append(parts, virtual.Part[[]searchHit]{Member: c, Doc: hits})
		}
		merged, err := (npmfmt.SearchMerger[searchContribution]{From: q.from, Size: q.size}).Merge(r.Context(), parts)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		objects := make([]json.RawMessage, 0, len(merged))
		for _, hit := range merged {
			objects = append(objects, hit.Value.object)
		}
		// Build before the last authority fence, so rendering cannot open a gap
		// between a source's final checks and the response's first bytes.
		body := encode(map[string]any{"objects": objects})
		retry := false
		for _, hit := range merged {
			v := hit.Value
			allowed, e := h.searchAllowed(r, v.member, hit.Name, v.path)
			if e == nil && allowed && v.authority != nil {
				e = v.authority.CheckPermission(r.Context(), v.path)
			}
			if e != nil && !searchPassedOver(e) {
				h.k.Error(w, r, e)
				return
			}
			if !allowed || e != nil {
				member := pkgroute.RequestFrom(v.member).Repo.Key
				if blocked[member] == nil {
					blocked[member] = map[string]bool{}
				}
				blocked[member][hit.Name] = true
				retry = true
			}
		}
		if retry {
			continue
		}
		w.Header().Set("Content-Type", jsonType)
		_, _ = w.Write(append(body, '\n'))
		return
	}
}

func (h handler) searchAllowed(r, mr *http.Request, name, path string) (bool, error) {
	if !h.k.Can(mr, path, api.ActionRead) {
		return false, nil
	}
	// Walk at the packument path to preserve virtual, nested and member
	// patterns. Permissions continue to name the actual version's tarball.
	members, err := h.k.MergeMembers(r, documentPath(name))
	if err != nil {
		if pkgroute.PassedOver(err) {
			return false, nil
		}
		return false, fmt.Errorf("npm search policy members: %w", err)
	}
	for _, member := range members {
		if pkgroute.RequestFrom(member).Repo.Key == pkgroute.RequestFrom(mr).Repo.Key {
			return true, nil
		}
	}
	return false, nil
}

func (h handler) localSearchHits(r, mr *http.Request, q searchQuery, needed int, blocked map[string]bool) ([]searchHit, error) {
	key := pkgroute.RequestFrom(mr).Repo.Key
	out := []searchHit{}
	seen := map[string]bool{}
	find := port.NpmSearch{RepoKey: key, Terms: q.terms}
	page := kernel.Page{Limit: batch(0, needed, 0)}
	for {
		found, next, err := h.k.DB().Packages().SearchNpm(mr.Context(), find, page)
		if err != nil {
			return nil, fmt.Errorf("npm virtual local search: %w", err)
		}
		for _, candidate := range found {
			hit, ok, err := h.searched(mr, candidate, q.terms)
			if err != nil {
				return nil, err
			}
			if !ok || blocked[hit.Name] || seen[hit.Name] {
				continue
			}
			path := npmfmt.CanonicalPath(hit.Name, hit.Version)
			allowed, err := h.searchAllowed(r, mr, hit.Name, path)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			seen[hit.Name] = true
			out = append(out, searchHit{Name: hit.Name, Value: searchContribution{object: encode(searchObject{Package: hit}), member: mr, path: path}})
			if len(out) == needed {
				return out, nil
			}
		}
		if next == "" {
			return out, nil
		}
		page = kernel.Page{After: next, Limit: batch(0, needed-len(out), page.Limit)}
	}
}

func (h handler) searchAuthority(mr *http.Request, resource source.Resource) (*source.Authority, error) {
	if h.sources == nil {
		return nil, source.ErrUnsupported
	}
	p, _ := kernel.FromContext(mr.Context())
	access, _ := api.DeciderFrom(mr.Context())
	q := pkgroute.RequestFrom(mr)
	authority, err := h.sources.Authorize(
		mr.Context(),
		source.Request{Repo: q.Repo, CacheOnly: q.CacheOnly, Subject: api.SubjectOf(p), Access: access, Resource: resource, Project: q.Repo.ProjectKey},
	)
	if err != nil {
		return nil, fmt.Errorf("npm search source authority: %w", err)
	}
	return authority, nil
}

func searchDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (h handler) rawSearchPage(mr *http.Request, query url.Values) ([]remoteDocument, *source.Authority, error) {
	resource, err := searchResource(mr, query, true)
	if err != nil {
		return nil, nil, err
	}
	authority, err := h.searchAuthority(mr, resource)
	if err != nil {
		return nil, nil, err
	}
	p, _ := kernel.FromContext(mr.Context())
	subject := api.SubjectOf(p)
	slices.Sort(subject.Groups)
	// Only hashes reach the cache document/log. Query and source descriptors
	// are opaque transport/cache inputs and must never become log messages.
	document := "npm-search/" + searchDigest(struct {
		Query    string
		Identity any
		Revision string
		Config   repo.Config
	}{query.Encode(), authority.Identity(), authority.Revision(), pkgroute.RequestFrom(mr).Repo})
	entry, err := h.k.MergedRawPart(mr, document, searchDigest(subject), func(mr *http.Request) (virtual.Entry, error) {
		doc, result, err := h.readSearchPage(mr, query, true)
		if err != nil {
			return virtual.Entry{}, err
		}
		if result.Revalidate != nil {
			if err := result.Revalidate(mr.Context()); err != nil {
				return virtual.Entry{}, fmt.Errorf("revalidate npm search page: %w", err)
			}
		}
		return virtual.Entry{Body: doc["objects"]}, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("cache npm search page: %w", err)
	}
	if err := authority.Revalidate(mr.Context()); err != nil {
		return nil, nil, fmt.Errorf("revalidate npm search source: %w", err)
	}
	var objects []remoteDocument
	if json.Unmarshal(entry.Body, &objects) != nil || objects == nil || len(objects) > searchPage {
		return nil, nil, remoteProtocolError()
	}
	return objects, authority, nil
}

func (h handler) remoteSearchHits(r, mr *http.Request, q searchQuery, needed int, blocked map[string]bool, pages map[string]int) ([]searchHit, error) {
	out := []searchHit{}
	seen := map[string]bool{}
	query := r.URL.Query()
	query.Set("text", strings.Join(q.terms, " "))
	at, limit := 0, batch(0, needed, 0)
	member := pkgroute.RequestFrom(mr).Repo.Key
	bytes := 0
	for {
		if pages[member] >= remoteSearchPages {
			return nil, remoteProtocolError()
		}
		pages[member]++
		query.Set("from", strconv.Itoa(at))
		query.Set("size", strconv.Itoa(limit))
		objects, authority, err := h.rawSearchPage(mr, query)
		if err != nil {
			return nil, err
		}
		for _, object := range objects {
			var pkg struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			if json.Unmarshal(object["package"], &pkg) != nil || !npmfmt.ValidName(pkg.Name) || !npmfmt.SafeSearchVersion(pkg.Version) {
				return nil, remoteProtocolError()
			}
			if blocked[pkg.Name] || seen[pkg.Name] {
				continue
			}
			path := npmfmt.CanonicalPath(pkg.Name, pkg.Version)
			allowed, err := h.searchAllowed(r, mr, pkg.Name, path)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			if err := authority.CheckPermission(mr.Context(), path); err != nil {
				if searchPassedOver(err) {
					continue
				}
				return nil, fmt.Errorf("authorize npm search hit: %w", err)
			}
			raw := encode(object)
			bytes += len(raw)
			if bytes > remoteMetadataLimit {
				return nil, remoteProtocolError()
			}
			seen[pkg.Name] = true
			out = append(out, searchHit{Name: pkg.Name, Value: searchContribution{object: raw, member: mr, path: path, authority: authority}})
			if len(out) == needed {
				return out, nil
			}
		}
		if len(objects) < limit {
			return out, nil
		}
		at += len(objects)
		limit = batch(0, needed-len(out), limit)
	}
}
