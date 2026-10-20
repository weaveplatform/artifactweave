package nuget

import "net/http"

// serviceIndex is the v3 service index (decision 0038), one resource per
// @type the client may look up. Its 7.x paths are the registration hives,
// the search and the symbols push (research 29 section 3); the flat
// container's, autocomplete's and the publish URL are chosen here [I], the
// publish URL being the v2 root, where JFrog documents pushes and deletes.
// Left out: nuget.org's catalog, repository signatures (nothing here signs),
// vulnerability data, the URI templates of web pages artifactweave does not
// have, package staging, and every /Versioned type: the client needs none.
type serviceIndex struct {
	Version   string     `json:"version"`
	Resources []resource `json:"resources"`
}

//nolint:tagliatelle // NuGet's wire names
type resource struct {
	ID   string `json:"@id"`
	Type string `json:"@type"`
}

// serviceIndex answers the service index to anyone who may reach the
// repository. The client accepts it only when version is SemVer with major
// version 3 (ServiceIndexResourceV3Provider).
func (h handler) serviceIndex(w http.ResponseWriter, r *http.Request) {
	v3, v2 := v3Base(r), v2Base(r)
	var res []resource
	add := func(id string, types ...string) {
		for _, t := range types {
			res = append(res, resource{ID: id, Type: t})
		}
	}
	add(v3+"/flatcontainer/", "PackageBaseAddress/3.0.0")
	add(v3+"/"+hives[0].path+"/", "RegistrationsBaseUrl", "RegistrationsBaseUrl/3.0.0-beta",
		"RegistrationsBaseUrl/3.0.0-rc", "RegistrationsBaseUrl/3.4.0")
	add(v3+"/"+hives[1].path+"/", "RegistrationsBaseUrl/3.6.0")
	add(v3+"/query", "SearchQueryService", "SearchQueryService/3.0.0-beta",
		"SearchQueryService/3.0.0-rc", "SearchQueryService/3.5.0")
	add(v3+"/autocomplete", "SearchAutocompleteService", "SearchAutocompleteService/3.0.0-beta",
		"SearchAutocompleteService/3.0.0-rc", "SearchAutocompleteService/3.5.0")
	add(v2, "PackagePublish/2.0.0", "LegacyGallery/2.0.0")
	add(v3+"/symbols", "SymbolPackagePublish/4.9.0")
	writeJSON(w, serviceIndex{Version: "3.0.0", Resources: res})
}
