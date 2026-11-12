package npmhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const (
	// maxSearchTerms is how many of the text's terms must match: the three
	// a 7.x local repository matches (7.125.4, "Improved npm Search").
	maxSearchTerms = 3
	// defaultSearchSize is npm's own searchlimit, which it always sends.
	defaultSearchSize = 20
	// searchPage is the most packages one read of the index returns.
	searchPage = 500
)

// searchQuery is what GET /-/v1/search asks for. npm also sends quality,
// popularity and maintenance, which weigh a score nothing here computes.
type searchQuery struct {
	terms      []string
	from, size int
}

// parseSearch reads text, from and size. A from or size that is no
// number, or out of range, takes its default, as the query is npm's own
// and a search should not fail on it. size has no cap: JFrog's KB on npm
// search has users raise --searchlimit to 1000 to see past the first 20,
// and what a search returns is bounded by the repository anyway.
func parseSearch(q url.Values) searchQuery {
	terms := strings.Fields(strings.ToLower(q.Get("text")))
	if len(terms) > maxSearchTerms {
		terms = terms[:maxSearchTerms]
	}
	s := searchQuery{terms: terms, size: defaultSearchSize}
	if n, err := strconv.Atoi(q.Get("from")); err == nil && n > 0 {
		s.from = n
	}
	if n, err := strconv.Atoi(q.Get("size")); err == nil && n > 0 {
		s.size = n
	}
	return s
}

// searchAnswer is a search's body in 7.x's shape (RTFACT-26660): objects
// alone, each a package with no score, and no total or time.
type searchAnswer struct {
	Objects []searchObject `json:"objects"`
}

type searchObject struct {
	Package searchPackage `json:"package"`
}

// searchPackage is one package as 7.x lists it, in its order: the latest
// version's description, keywords and author, null or empty when its
// package.json has none, as RTFACT-26660 shows them. maintainers is always
// empty: nothing here records who publishes a package, and npm's search
// output reads the field as a list, so it cannot be left out. date is when
// the latest version was deployed; [I] RTFACT-26660 shows null for every
// package, and whether 7.x ever fills it is on #216.
type searchPackage struct {
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Maintainers []json.RawMessage `json:"maintainers"`
	Version     string            `json:"version"`
	Date        string            `json:"date"`
	Keywords    []string          `json:"keywords"`
	Author      json.RawMessage   `json:"author"`
}

// search answers npm search from the package index, which every deploy
// keeps current in its own transaction, so a package is found once its
// publish commits; 7.x calculates its index asynchronously (DV-071). Each
// package is matched as its caller sees it: by its latest among the
// versions the caller may read, every term of the text appearing, ignoring
// case, in its name, description or a keyword. A package the caller may
// read no version of is left out. Results are in name order, the index's.
//
// The store returns, a page at a time and in that order, only the
// packages whose name or a version's metadata may hold every term
// (port.NpmSearch); the match and the caller's permissions are decided
// here, so from is counted here too. Permissions are path patterns the
// access module evaluates in Go, and latest and the text are what the
// caller may read of each package, so no query can count from exactly.
// The scan stops once it has from+size, and holds one page of candidates.
func (h handler) search(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteSearch(w, r)
		return
	}
	q := parseSearch(r.URL.Query())
	key := pkgroute.RequestFrom(r).Repo.Key
	answer := searchAnswer{Objects: []searchObject{}}
	skip := q.from
	find := port.NpmSearch{RepoKey: key, Terms: q.terms}
	page := kernel.Page{Limit: batch(skip, q.size, 0)}
	for {
		found, next, err := h.k.DB().Packages().SearchNpm(r.Context(), find, page)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("npm: search %s: %w", key, err))
			return
		}
		for _, c := range found {
			hit, ok, err := h.searched(r, c, q.terms)
			switch {
			case err != nil:
				h.k.Error(w, r, err)
				return
			case !ok:
				continue
			case skip > 0:
				skip--
				continue
			}
			answer.Objects = append(answer.Objects, searchObject{Package: hit})
			if len(answer.Objects) == q.size {
				writeJSON(w, http.StatusOK, jsonType, answer)
				return
			}
		}
		if next == "" {
			break
		}
		page = kernel.Page{After: next, Limit: batch(skip, q.size-len(answer.Objects), page.Limit)}
	}
	writeJSON(w, http.StatusOK, jsonType, answer)
}

// batch is how many candidates the next read of the index asks for: as
// many as the page still lacks (skip more to pass over, then wanted), so
// a caller who may read every candidate reads no more than its page needs.
// After a read that left the page short (last), it asks at least twice
// as many, so a caller who may read few of the candidates does not read
// them a handful at a time. It never asks more than searchPage.
func batch(skip, wanted, last int) int {
	lacking := searchPage
	if skip < searchPage && wanted < searchPage {
		lacking = skip + wanted
	}
	return min(searchPage, max(lacking, 2*last))
}

// searched is the package c as the caller's search lists it, if the
// caller may read a version of it and its latest matches every term.
func (h handler) searched(
	r *http.Request,
	c port.SearchCandidate,
	terms []string,
) (searchPackage, bool, error) {
	p, ok, err := h.visible(r, c.Package, c.Versions)
	if err != nil || !ok {
		return searchPackage{}, false, err
	}
	v, ok := p.resolve(latestTag)
	if !ok {
		return searchPackage{}, false, nil
	}
	fields, err := manifestFields(v)
	if err != nil {
		return searchPackage{}, false, err
	}
	hit := searchPackage{
		Name: p.name, Description: description(fields["description"]),
		Maintainers: []json.RawMessage{}, Version: v.number, Date: stamp(v.meta.Published),
		Keywords: npmfmt.Keywords(fields["keywords"]), Author: fields["author"],
	}
	if hit.Keywords == nil {
		hit.Keywords = []string{}
	}
	return hit, hit.matches(terms), nil
}

// matches reports whether every term appears in p's name, description or
// one of its keywords, ignoring case; terms are lower case already.
func (p searchPackage) matches(terms []string) bool {
	text := []string{strings.ToLower(p.Name)}
	if p.Description != nil {
		text = append(text, strings.ToLower(*p.Description))
	}
	for _, k := range p.Keywords {
		text = append(text, strings.ToLower(k))
	}
	for _, t := range terms {
		found := false
		for _, s := range text {
			if strings.Contains(s, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
