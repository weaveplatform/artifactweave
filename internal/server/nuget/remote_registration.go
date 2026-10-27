package nuget

import (
	"context"
	"net/http"
	"slices"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteVersion struct {
	key    string
	entry  remoteDoc
	result source.Result
}

// Registration pages and leaf links are opaque upstream URLs. Only the index
// path is constructed; page targets always come from validated parent metadata.
func (s *remoteSession) registrations(hv hive, id string) ([]remoteVersion, error) {
	if !nugetfmt.ValidID(id) {
		return nil, notFound(s.r)
	}
	id = nugetfmt.PackageName(id)
	base, err := s.endpoint(hv.path + "/")
	if err != nil {
		return nil, err
	}
	doc, result, err := s.document(
		appendResource(base, id, "index.json"),
		id+"/.nuget/"+hv.path+"/index.json",
		id,
		"registration",
		id,
	)
	if err != nil {
		return nil, err
	}
	var out []remoteVersion
	seen := make(map[string]bool)
	visited := make(map[string]bool)
	pages := arrayField(doc, "items")
	if len(pages) > 100 {
		return nil, remoteFailure()
	}
	for _, page := range pages {
		entries := arrayField(page, "items")
		pageResult := result
		if entries == nil {
			target := stringField(page, "@id")
			if visited[target] {
				return nil, remoteFailure()
			}
			visited[target] = true
			page, pageResult, err = s.document(
				target,
				id+"/.nuget/"+hv.path+"/pages/"+shortHash(target)+".json",
				id,
				"page",
				id,
			)
			if err != nil {
				return nil, err
			}
			entries = arrayField(page, "items")
		}
		for _, entry := range entries {
			key, _ := versionKey(stringField(objectField(entry, "catalogEntry"), "version"))
			if seen[key] || len(out) >= 10000 {
				return nil, remoteFailure()
			}
			seen[key] = true
			out = append(out, remoteVersion{key: key, entry: entry, result: pageResult})
		}
	}
	slices.SortFunc(out, func(a, b remoteVersion) int { return types.CompareNuGet(a.key, b.key) })
	return out, nil
}

func (s *remoteSession) preferredHive() (hive, bool) {
	for i := len(hives) - 1; i >= 0; i-- {
		if _, err := s.endpoint(hives[i].path + "/"); err == nil {
			return hives[i], true
		}
	}
	return hive{}, false
}

func (s *remoteSession) registration(
	ctx context.Context,
	w http.ResponseWriter,
	hv hive,
	parts []string,
) {
	if len(parts) == 3 && parts[0] == "Download" {
		s.packageFile(ctx, w, parts[1], parts[2], false)
		return
	}
	if len(parts) != 2 && len(parts) != 4 {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	id := nugetfmt.PackageName(parts[0])
	vs, err := s.registrations(hv, id)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	u := urlsOf(s.r, hv)
	var filtered []remoteVersion
	lo, hi := "", ""
	if len(parts) == 4 {
		if parts[1] != "page" || !strings.HasSuffix(parts[3], ".json") {
			s.h.k.Error(w, s.r, notFound(s.r))
			return
		}
		lo, _ = versionKey(parts[2])
		hi, _ = versionKey(strings.TrimSuffix(parts[3], ".json"))
		if lo == "" || hi == "" {
			s.h.k.Error(w, s.r, notFound(s.r))
			return
		}
	}
	leafKey := ""
	if len(parts) == 2 && parts[1] != "index.json" {
		if !strings.HasSuffix(parts[1], ".json") {
			s.h.k.Error(w, s.r, notFound(s.r))
			return
		}
		leafKey, _ = versionKey(strings.TrimSuffix(parts[1], ".json"))
		if leafKey == "" {
			s.h.k.Error(w, s.r, notFound(s.r))
			return
		}
	}
	for _, v := range vs {
		if !remoteHiveShows(hv, v) {
			continue
		}
		if leafKey != "" && v.key != leafKey ||
			lo != "" && (types.CompareNuGet(v.key, lo) < 0 || types.CompareNuGet(v.key, hi) > 0) {
			continue
		}
		if s.allowed(id, v.key, v.result) {
			filtered = append(filtered, v)
		}
	}
	if len(filtered) == 0 {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	if leafKey != "" {
		entry := s.rewriteEntry(hv, id, filtered[0])
		// Leaf metadata comes from its registration page; its catalog pointer is
		// optional and must not send clients back to an unproxied catalog.
		catalog := objectField(entry, "catalogEntry")
		delete(entry, "catalogEntry")
		for _, key := range []string{"listed", "published"} {
			if val, ok := catalog[key]; ok {
				entry[key] = val
			}
		}
		s.json(w, entry)
		return
	}
	entries := func(vs []remoteVersion) []remoteDoc {
		out := make([]remoteDoc, 0, len(vs))
		for _, v := range vs {
			out = append(out, s.rewriteEntry(hv, id, v))
		}
		return out
	}
	if lo != "" {
		s.json(
			w,
			map[string]any{
				"@id":    u.page(id, lo, hi),
				"parent": u.index(id),
				"count":  len(filtered),
				"lower":  lo,
				"upper":  hi,
				"items":  entries(filtered),
			},
		)
		return
	}
	pages := make([]map[string]any, 0)
	for start := 0; start < len(filtered); start += pageSize {
		page := filtered[start:min(start+pageSize, len(filtered))]
		lower, upper := page[0].key, page[len(page)-1].key
		pages = append(
			pages,
			map[string]any{
				"@id":    u.page(id, lower, upper),
				"parent": u.index(id),
				"count":  len(page),
				"lower":  lower,
				"upper":  upper,
				"items":  entries(page),
			},
		)
	}
	s.json(w, map[string]any{"@id": u.index(id), "count": len(pages), "items": pages})
}

func (s *remoteSession) rewriteEntry(hv hive, id string, v remoteVersion) remoteDoc {
	u := urlsOf(s.r, hv)
	d := v.entry
	catalog := objectField(d, "catalogEntry")
	content := v3Base(s.r) + "/flatcontainer/" + remotePackagePath(id, v.key)
	setField(d, "@id", u.leaf(id, v.key))
	setField(d, "registration", u.index(id))
	setField(d, "packageContent", content)
	setField(catalog, "@id", u.leaf(id, v.key))
	setField(catalog, "packageContent", content)
	// The catalog item's upstream URL is never a client escape hatch. Preserve
	// descriptive metadata, listed/unlisted status and dependency ranges verbatim.
	groups := arrayField(catalog, "dependencyGroups")
	for _, g := range groups {
		deps := arrayField(g, "dependencies")
		for _, dep := range deps {
			setField(dep, "registration", u.index(nugetfmt.PackageName(stringField(dep, "id"))))
			delete(dep, "@id")
		}
		setField(g, "dependencies", deps)
		delete(g, "@id")
	}
	if groups != nil {
		setField(catalog, "dependencyGroups", groups)
	}
	setField(d, "catalogEntry", catalog)
	return d
}

func remoteHiveShows(hv hive, v remoteVersion) bool {
	return hv.allowSemVer2 || !remoteSemVer2(objectField(v.entry, "catalogEntry"))
}
