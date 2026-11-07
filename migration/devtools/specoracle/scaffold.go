package specoracle

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Placeholder is written into every description a scaffolded fragment
// carries. TestAccOpenAPIFieldDescriptions rejects it once the fragment loses its
// x-draft marker, so an author cannot ship a scaffold by accident.
const Placeholder = "TODO(author)"

// ScaffoldOptions selects what to scaffold and how to name it.
type ScaffoldOptions struct {
	// CacheDir is the oracle cache.
	CacheDir string
	// Family is the cache directory (index slug) to scaffold.
	Family string
	// ServiceBase is our service prefix, for example "/artifactory". Only
	// operations whose full path starts with it are emitted; the rest are
	// listed in Result.Skipped so the author can scaffold them elsewhere.
	ServiceBase string
	// Tag is written on every operation; defaults to the family's last word.
	Tag string
	// SkipSchemas are component names the service already defines (the shared
	// error envelope, for instance); references are kept but the schema is not
	// emitted again.
	SkipSchemas []string
}

// ScaffoldResult reports what was written and what the author must resolve.
type ScaffoldResult struct {
	YAML       []byte
	Operations int
	Schemas    int
	// Skipped lists operation keys outside ServiceBase.
	Skipped []string
	// Renamed maps original schema names to the family-prefixed names used
	// because another family defines the same name with a different shape.
	Renamed map[string]string
}

var (
	errScaffoldFamily  = errors.New("specoracle: scaffold family is required")
	errScaffoldService = errors.New("specoracle: scaffold service base must start with /")
	errFamilyEmpty     = errors.New("specoracle: no cached blocks for family")
)

// Scaffold converts every block of one family into an OpenAPI 3.0.3 fragment
// (paths plus components) with placeholder descriptions. Structure is a fact
// about the wire protocol; prose is not, so none of JFrog's is carried over.
func Scaffold(opts ScaffoldOptions) (ScaffoldResult, error) {
	if opts.Family == "" {
		return ScaffoldResult{}, errScaffoldFamily
	}
	if !strings.HasPrefix(opts.ServiceBase, "/") {
		return ScaffoldResult{}, errScaffoldService
	}
	blocks, _, err := readBlocks(opts.CacheDir)
	if err != nil {
		return ScaffoldResult{}, err
	}
	var mine, others []block
	for _, b := range blocks {
		if b.family == opts.Family {
			mine = append(mine, b)
		} else {
			others = append(others, b)
		}
	}
	if len(mine) == 0 {
		return ScaffoldResult{}, fmt.Errorf("%w: %s", errFamilyEmpty, opts.Family)
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].page < mine[j].page })
	tag := opts.Tag
	if tag == "" {
		tag = defaultTag(opts.Family)
	}
	s := &scaffolder{
		opts:     opts,
		tag:      tag,
		skip:     toSet(opts.SkipSchemas),
		foreign:  foreignShapes(others),
		paths:    map[string]map[string]any{},
		comps:    map[string]map[string]any{},
		shapes:   map[string]string{},
		renamed:  map[string]string{},
		opIDs:    map[string]bool{},
		familyID: camel(opts.Family),
	}
	for _, b := range mine {
		s.addBlock(b)
	}
	out, err := s.render()
	if err != nil {
		return ScaffoldResult{}, err
	}
	res := ScaffoldResult{
		YAML:       out,
		Operations: s.ops,
		Schemas:    len(s.comps["schemas"]),
		Skipped:    s.skipped,
		Renamed:    s.renamed,
	}
	sort.Strings(res.Skipped)
	return res, nil
}

type scaffolder struct {
	opts     ScaffoldOptions
	tag      string
	skip     map[string]bool
	foreign  map[string]map[string]bool
	paths    map[string]map[string]any
	comps    map[string]map[string]any
	shapes   map[string]string
	renamed  map[string]string
	opIDs    map[string]bool
	ops      int
	skipped  []string
	familyID string
}

// foreignShapes collects, per schema name, the canonical shapes other
// families use, so a name reused with a different shape can be prefixed.
func foreignShapes(blocks []block) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, b := range blocks {
		comps, _ := b.doc["components"].(map[string]any)
		for kind, kv := range comps {
			if kind == "securitySchemes" {
				continue
			}
			items, _ := kv.(map[string]any)
			for name, sv := range items {
				key := kind + "/" + name
				if out[key] == nil {
					out[key] = map[string]bool{}
				}
				out[key][canonical(stripProse(sv))] = true
			}
		}
	}
	return out
}

func (s *scaffolder) addBlock(b block) {
	base := serverBasePath(b.doc)
	comps, _ := b.doc["components"].(map[string]any)
	rename := s.registerComponents(comps, b.page)
	paths, _ := b.doc["paths"].(map[string]any)
	for p, item := range paths {
		pi, ok := item.(map[string]any)
		if !ok {
			continue
		}
		full := joinPath(base, p)
		if !strings.HasPrefix(full, s.opts.ServiceBase+"/") {
			for method := range pi {
				if isMethod(method) {
					s.skipped = append(s.skipped, strings.ToUpper(method)+" "+full)
				}
			}
			continue
		}
		rel := strings.TrimPrefix(full, s.opts.ServiceBase)
		entry := s.paths[rel]
		if entry == nil {
			entry = map[string]any{}
			s.paths[rel] = entry
		}
		for k, v := range pi {
			if !isMethod(k) {
				if k == "parameters" {
					entry[k] = s.convert(v, rename)
				}
				continue
			}
			op, ok := v.(map[string]any)
			if !ok {
				continue
			}
			entry[k] = s.operation(op, b, rename)
			s.ops++
		}
	}
}

// registerComponents adds a block's components, renaming a name whose shape
// differs from a shape already registered in this family or in another one.
func (s *scaffolder) registerComponents(comps map[string]any, page string) map[string]string {
	rename := map[string]string{}
	for kind, kv := range comps {
		if kind == "securitySchemes" {
			continue
		}
		items, _ := kv.(map[string]any)
		names := make([]string, 0, len(items))
		for n := range items {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			if kind == "schemas" && s.skip[name] {
				continue
			}
			shape := canonical(stripProse(items[name]))
			target := name
			if other, ok := s.foreign[kind+"/"+name]; ok && (len(other) != 1 || !other[shape]) {
				target = s.familyID + name
			}
			// The same name with another shape inside one family is suffixed
			// with its page so the author can see which endpoint it serves.
			if prev, seen := s.shapes[kind+"/"+target]; seen && prev != shape {
				target += camel(page)
			}
			if target != name {
				rename[kind+"/"+name] = target
				s.renamed[name] = target
			}
			if s.comps[kind] == nil {
				s.comps[kind] = map[string]any{}
			}
			if _, exists := s.comps[kind][target]; !exists {
				s.comps[kind][target] = items[name]
				s.shapes[kind+"/"+target] = shape
			}
		}
	}
	// Second pass: component bodies may reference renamed siblings. Schemas
	// are marked x-draft like operations so the description gate skips them
	// until an author replaces the placeholders.
	for kind, items := range s.comps {
		for name, v := range items {
			conv := s.convert(v, rename)
			if m, ok := conv.(map[string]any); ok && kind == "schemas" {
				m["x-draft"] = true
			}
			items[name] = conv
		}
		s.comps[kind] = items
	}
	return rename
}

func (s *scaffolder) operation(
	op map[string]any,
	b block,
	rename map[string]string,
) map[string]any {
	out := map[string]any{}
	for k, v := range op {
		switch k {
		case "security", "servers", "tags", "x-readme", "summary", "description", "externalDocs":
			continue
		default:
			out[k] = s.convert(v, rename)
		}
	}
	id, _ := op["operationId"].(string)
	id = lowerCamel(id)
	if id == "" {
		id = lowerCamel(b.page)
	}
	if s.opIDs[id] {
		id += s.familyID
	}
	s.opIDs[id] = true
	out["operationId"] = id
	out["tags"] = []any{s.tag}
	out["description"] = Placeholder
	out["x-draft"] = true
	out["x-oracle-source"] = b.source
	if _, ok := out["responses"]; !ok {
		out["responses"] = map[string]any{"default": map[string]any{"description": Placeholder}}
	}
	return out
}

// namedMaps are keywords whose value is a map of user-chosen names (property
// names, status codes, media types) rather than keywords. A property called
// "description" must survive; a keyword called "description" must not.
var namedMaps = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"responses":         true,
	"content":           true,
	"headers":           true,
	"encoding":          true,
	"mapping":           true,
	"links":             true,
	"callbacks":         true,
}

// convert deep-copies a value into OpenAPI 3.0.3 form: prose replaced by the
// placeholder, 3.1 nullable type arrays folded into nullable, const turned
// into a one-value enum, numeric exclusive bounds split, examples dropped.
func (s *scaffolder) convert(v any, rename map[string]string) any {
	return s.convertNode(v, rename, false)
}

func (s *scaffolder) convertNode(v any, rename map[string]string, named bool) any {
	switch t := v.(type) {
	case map[string]any:
		if named {
			out := make(map[string]any, len(t))
			for k, val := range t {
				out[k] = s.convertNode(val, rename, false)
			}
			return out
		}
		return s.convertKeywords(t, rename)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = s.convertNode(t[i], rename, false)
		}
		return out
	default:
		return v
	}
}

func (s *scaffolder) convertKeywords(t map[string]any, rename map[string]string) map[string]any {
	out := make(map[string]any, len(t))
	for k, val := range t {
		switch k {
		case "example", "examples", "x-readme", "summary", "title", "$schema", "externalDocs":
			continue
		case "description":
			out[k] = Placeholder
			continue
		case "$ref":
			if ref, ok := val.(string); ok {
				out[k] = s.renameRef(ref, rename)
				continue
			}
		case "const":
			out["enum"] = []any{val}
			continue
		case "type":
			if list, ok := val.([]any); ok {
				out["type"] = firstNonNull(list)
				if hasNull(list) {
					out["nullable"] = true
				}
				continue
			}
		case "exclusiveMinimum", "exclusiveMaximum":
			if n, ok := val.(float64); ok {
				bound := strings.TrimPrefix(k, "exclusive")
				out[strings.ToLower(bound[:1])+bound[1:]] = n
				out[k] = true
				continue
			}
		}
		out[k] = s.convertNode(val, rename, namedMaps[k])
	}
	if _, isRef := out["$ref"]; !isRef && needsDescription(t) {
		if _, has := out["description"]; !has {
			out["description"] = Placeholder
		}
	}
	return out
}

// needsDescription says whether a mapping is a schema, parameter or response
// that the description gate will inspect. Properties and parameters always
// get one; response objects need one to be valid 3.0.
func needsDescription(m map[string]any) bool {
	_, isParam := m["in"]
	_, hasType := m["type"]
	_, hasProps := m["properties"]
	_, hasItems := m["items"]
	_, hasContent := m["content"]
	_, hasEnum := m["enum"]
	return isParam || hasType || hasProps || hasItems || hasContent || hasEnum
}

func (s *scaffolder) renameRef(ref string, rename map[string]string) string {
	if !strings.HasPrefix(ref, "#/components/") {
		return ref
	}
	rest := strings.TrimPrefix(ref, "#/components/")
	if target, ok := rename[rest]; ok {
		kind, _, _ := strings.Cut(rest, "/")
		return "#/components/" + kind + "/" + target
	}
	return ref
}

func firstNonNull(list []any) any {
	for _, x := range list {
		if x != "null" {
			return x
		}
	}
	return "string"
}

func hasNull(list []any) bool {
	for _, x := range list {
		if x == "null" {
			return true
		}
	}
	return false
}

// render encodes paths and components with sorted keys so the fragment is
// stable across runs.
func (s *scaffolder) render() ([]byte, error) {
	doc := map[string]any{"paths": s.paths}
	if len(s.comps) > 0 {
		comps := map[string]any{}
		for kind, items := range s.comps {
			comps[kind] = items
		}
		doc["components"] = comps
	}
	var sb strings.Builder
	sb.WriteString(
		"# Scaffolded by `make scaffold`: structure from the oracle, prose to be written.\n",
	)
	sb.WriteString(
		"# Every description is " + Placeholder + " and every operation carries x-draft until\n",
	)
	sb.WriteString(
		"# an author replaces them (docs/implementation/api-description-conventions.md).\n",
	)
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(sortNode(doc)); err != nil {
		return nil, fmt.Errorf("specoracle: encode scaffold: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("specoracle: close encoder: %w", err)
	}
	return []byte(sb.String()), nil
}

// sortNode builds a yaml.Node tree with mapping keys in sorted order, which
// yaml.v3 then preserves on output (plain maps would be sorted too, but the
// explicit tree lets quoted status codes stay strings).
func sortNode(v any) *yaml.Node {
	switch t := v.(type) {
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kn := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
			if looksNumeric(k) {
				kn.Style = yaml.DoubleQuotedStyle
			}
			n.Content = append(n.Content, kn, sortNode(t[k]))
		}
		return n
	case map[string]map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = val
		}
		return sortNode(m)
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, x := range t {
			n.Content = append(n.Content, sortNode(x))
		}
		return n
	default:
		n := &yaml.Node{}
		_ = n.Encode(v)
		if str, ok := v.(string); ok &&
			(looksNumeric(str) || str == "true" || str == "false" || str == "null") {
			n.Style = yaml.DoubleQuotedStyle
		}
		return n
	}
}

func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && r != '.' {
			return false
		}
	}
	return true
}

// defaultTag derives a tag from the family slug: the last meaningful word
// before "api", so "artifactory-repositories-api" becomes "repositories".
func defaultTag(family string) string {
	parts := strings.Split(family, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "api" && parts[i] != "apis" && parts[i] != "" {
			return parts[i]
		}
	}
	return family
}

// camel turns "artifactory-repositories-api" into "ArtifactoryRepositoriesApi".
func camel(s string) string {
	var sb strings.Builder
	upper := true
	for _, r := range s {
		if r == '-' || r == '_' || r == ' ' {
			upper = true
			continue
		}
		if upper {
			sb.WriteRune(unicode.ToUpper(r))
			upper = false
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func lowerCamel(s string) string {
	c := camel(s)
	if c == "" {
		return ""
	}
	return strings.ToLower(c[:1]) + c[1:]
}
