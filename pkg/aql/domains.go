package aql

import (
	"slices"
	"strings"
)

// Domain is a query's primary domain (research 22 §1.1).
type Domain int

// Primary domains. The zero value is items, so a Query built by hand
// searches items.
const (
	DomainItems Domain = iota
	DomainBuilds
	DomainPromotions
)

func (d Domain) String() string {
	switch d {
	case DomainBuilds:
		return "builds"
	case DomainPromotions:
		return "build.promotions"
	}
	return "items"
}

// rel is the domain a relation path reaches, named as paths name it:
// "artifact.module.build" from items steps through artifact and module to
// build.
type rel int

const (
	relItem rel = iota
	relBuild
	relPromotion
	relModule
	relArtifact
	relDependency
)

// rel is the domain of the rows d finds.
func (d Domain) rel() rel {
	switch d {
	case DomainBuilds:
		return relBuild
	case DomainPromotions:
		return relPromotion
	}
	return relItem
}

// table is the table, and the SQL alias, of the rows d finds.
func (d Domain) table() string {
	switch d {
	case DomainBuilds:
		return "builds"
	case DomainPromotions:
		return "build_promotions"
	}
	return "items"
}

var relNames = map[string]rel{
	"item": relItem, "build": relBuild, "promotion": relPromotion,
	"module": relModule, "artifact": relArtifact, "dependency": relDependency,
}

// relSteps lists where a relation path may step from each domain. An item
// is related to the build artifacts and dependencies with its SHA-1, which
// is how Artifactory joins them (its AqlTableGraph links nodes.sha1_actual
// to both); every other step follows the build-info document's structure.
var relSteps = map[rel][]rel{
	relItem:       {relArtifact, relDependency},
	relArtifact:   {relModule, relItem},
	relDependency: {relModule, relItem},
	relModule:     {relBuild, relArtifact, relDependency},
	relBuild:      {relModule, relPromotion},
	relPromotion:  {relBuild},
}

// relFields are the fields of the build domains by kind (research 22
// §1.1); item fields are fieldKinds. A dependency's name is its id, and its
// scope its scopes joined with commas, as Artifactory stores them
// (BuildStoreServiceImpl writes dependency.getId() and the scope set
// delimited by ","): build-info dependencies carry an id, not a name.
var relFields = map[rel]map[string]kind{
	relBuild: {
		"url": kindText, "name": kindText, "number": kindText, "created": kindTime,
		"created_by": kindText, "modified": kindTime, "modified_by": kindText,
		"started": kindTime, "repo": kindText,
	},
	relPromotion: {
		"created": kindTime, "created_by": kindText, "status": kindText, "repo": kindText,
		"comment": kindText, "user": kindText,
	},
	relModule:   {"name": kindText},
	relArtifact: {"name": kindText, "type": kindText, "sha1": kindText, "md5": kindText},
	relDependency: {
		"name": kindText, "scope": kindText, "type": kindText, "sha1": kindText, "md5": kindText,
	},
}

// buildFieldOrder and promotionFieldOrder are the order results render the
// fields of a builds and a build.promotions query: alphabetical, because
// Artifactory's result rows serialise their properties alphabetically
// (AqlRestResult.Row), as "build.<field>" and "build.promotion.<field>".
var (
	buildFieldOrder = []string{
		"created", "created_by", "modified", "modified_by", "name", "number", "repo",
		"started", "url",
	}
	promotionFieldOrder = []string{"comment", "created", "created_by", "repo", "status", "user"}
)

// fieldKind returns the kind of field f of r.
func (r rel) fieldKind(f string) (kind, bool) {
	if r == relItem {
		k, ok := fieldKinds[Field(f)]
		return k, ok
	}
	k, ok := relFields[r][f]
	return k, ok
}

// hasProperties reports whether r's rows carry properties: items, builds
// and modules do (research 22 §1.1).
func (r rel) hasProperties() bool { return r == relItem || r == relBuild || r == relModule }

// splitPath reads the relation path at the start of a criterion key, from
// the domain from: "artifact.module.build.name" from items is the path
// "artifact.module.build", reaching a build, and the rest "name". The rest
// is a field, "@<key>", "property.key" or "property.value"; a property key
// may itself contain dots. stepped is false when the key starts with no
// step from.
func splitPath(from rel, key string) (path string, to rel, rest string, stepped bool) {
	to, rest = from, key
	var steps []string
	for {
		seg, after, more := strings.Cut(rest, ".")
		next, ok := relNames[seg]
		if !more || !ok || !slices.Contains(relSteps[to], next) {
			break
		}
		steps = append(steps, seg)
		to, rest = next, after
	}
	return strings.Join(steps, "."), to, rest, len(steps) > 0
}

// pathRel is the domain a path from the primary domain reaches: the domain
// its last step names.
func pathRel(primary rel, path string) rel {
	if path == "" {
		return primary
	}
	return relNames[path[strings.LastIndexByte(path, '.')+1:]]
}

// parentPath is the path one step shorter.
func parentPath(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return ""
}

// under reports whether path is at or below dead.
func under(path, dead string) bool {
	return path == dead || strings.HasPrefix(path, dead+".")
}
