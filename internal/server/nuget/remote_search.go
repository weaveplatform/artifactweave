package nuget

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Limit autocomplete's package lookups as well as registration traversal. The
// upstream is allowed to return fewer results than take; totals describe only
// the visible response, never the unfiltered upstream collection.
func (s *remoteSession) queryTarget(route string) (string, error) {
	endpoint, err := s.endpoint(route)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(endpoint)
	query := u.Query()
	allowed := []string{"q", "skip", "take", "prerelease", "semVerLevel", "packageType"}
	if route == "autocomplete" {
		allowed = append(allowed, "id")
	}
	for _, key := range allowed {
		if value, ok := s.r.URL.Query()[key]; ok && len(value) > 0 {
			query.Set(key, value[0])
		}
	}
	if route == "autocomplete" && query.Get("id") != "" {
		query.Set("id", nugetfmt.PackageName(query.Get("id")))
	}
	page := pageOf(query)
	query.Set("take", strconv.Itoa(min(page.take, 100)))
	query.Set("skip", strconv.Itoa(page.skip))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (s *remoteSession) search(w http.ResponseWriter) {
	target, err := s.queryTarget("query")
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	d, result, err := s.document(
		target,
		".nuget/search/"+shortHash(target)+".json",
		"",
		"search",
		"",
	)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	entries := arrayField(d, "data")
	out := make([]remoteDoc, 0, len(entries))
	hv, _ := s.preferredHive()
	if hv.path == "" {
		hv = hives[1]
	}
	u := urlsOf(s.r, hv)
	for _, entry := range entries {
		id := nugetfmt.PackageName(stringField(entry, "id"))
		version := stringField(entry, "version")
		if !listedEntry(entry) || !s.searchShows(entry) || !s.allowed(id, version, result) {
			continue
		}
		versions := make([]remoteDoc, 0)
		for _, v := range arrayField(entry, "versions") {
			key, _ := versionKey(stringField(v, "version"))
			if s.searchShows(v) && s.allowed(id, key, result) {
				setField(v, "@id", u.leaf(id, key))
				versions = append(versions, v)
			}
		}
		if len(versions) == 0 {
			continue
		}
		setField(entry, "versions", versions)
		setField(entry, "@id", u.index(id))
		setField(entry, "registration", u.index(id))
		// Upstream totals combine downloads from versions which may be forbidden.
		delete(entry, "totalDownloads")
		out = append(out, entry)
	}
	setField(d, "data", out)
	setField(d, "totalHits", len(out))
	s.json(w, d)
}

func (s *remoteSession) autocomplete(w http.ResponseWriter) {
	target, err := s.queryTarget("autocomplete")
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	id := s.r.URL.Query().Get("id")
	kind := "autocomplete-id"
	if id != "" {
		if !nugetfmt.ValidID(id) {
			s.h.k.Error(w, s.r, notFound(s.r))
			return
		}
		id = nugetfmt.PackageName(id)
		kind = "autocomplete-version"
	}
	d, result, err := s.document(
		target,
		".nuget/autocomplete/"+shortHash(target)+".json",
		"",
		kind,
		id,
	)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	var values []string
	_ = json.Unmarshal(d["data"], &values)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if id != "" {
			if s.allowed(id, value, result) {
				out = append(out, value)
			}
			continue
		}
		if !s.h.k.CanFolder(s.r, nugetfmt.PackageName(value), types.ActionRead) {
			continue
		}
		versions, owner, versionErr := s.versions(value)
		if versionErr != nil {
			s.h.k.Error(w, s.r, versionErr)
			return
		}
		for _, v := range versions {
			if s.allowed(value, v, owner) {
				out = append(out, value)
				break
			}
		}
	}
	setField(d, "data", out)
	setField(d, "totalHits", len(out))
	s.json(w, d)
}

func signatureVersion(
	path string,
) bool {
	return path == "4.7.0" || path == "4.9.0" || path == "5.0.0"
}

func listedEntry(d remoteDoc) bool {
	return strings.TrimSpace(string(d["listed"])) != "false" &&
		!strings.HasPrefix(stringField(d, "published"), "1900-01-01")
}

func remoteSemVer2(d remoteDoc) bool {
	version, _ := nugetfmt.ParseVersion(stringField(d, "version"))
	n := nugetfmt.Nuspec{Version: version}
	for _, g := range arrayField(d, "dependencyGroups") {
		group := nugetfmt.DependencyGroup{}
		for _, dep := range arrayField(g, "dependencies") {
			group.Dependencies = append(
				group.Dependencies,
				nugetfmt.Dependency{ID: stringField(dep, "id"), Range: stringField(dep, "range")},
			)
		}
		n.DependencyGroups = append(n.DependencyGroups, group)
	}
	return n.IsSemVer2()
}

func (s *remoteSession) searchShows(d remoteDoc) bool {
	version, _ := nugetfmt.ParseVersion(stringField(d, "version"))
	q := s.r.URL.Query()
	return (strings.EqualFold(q.Get("prerelease"), "true") || len(version.Labels) == 0) &&
		(semVer2(q.Get("semVerLevel")) || !remoteSemVer2(d))
}
