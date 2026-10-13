package specoracle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Param is a normalised parameter.
type Param struct {
	Name     string
	In       string
	Required bool
	Type     string
	Enum     []string
}

// Operation is one method on one full path, flattened to what matters for
// wire compatibility. Property paths use "a.b" for nested objects and "a[].b"
// for array items.
type Operation struct {
	// Key is "METHOD /full/path" with every path parameter name replaced by
	// {}, because clients never see parameter names.
	Key          string
	Family       string
	Source       string
	ID           string
	Method       string
	Path         string
	Params       []Param
	RequestMedia []string
	RequestProps []string
	// Responses maps a status code to the property paths of its JSON body.
	Responses  map[string][]string
	Deprecated bool
}

// Oracle is a set of operations plus the parse problems met while loading.
type Oracle struct {
	Ops    map[string]Operation
	Errors []string
}

// Load reads every cached block under cacheDir. A block that fails to parse
// is recorded in Errors and skipped: one bad page must not hide the rest.
// The cache is exactly two levels deep (family/page.json), so a plain
// directory listing is enough and avoids walking symlinks.
func Load(cacheDir string) (Oracle, error) {
	o := Oracle{Ops: map[string]Operation{}}
	families, err := os.ReadDir(cacheDir)
	if err != nil {
		return o, fmt.Errorf("specoracle: read cache %s: %w", cacheDir, err)
	}
	for _, fam := range families {
		if !fam.IsDir() {
			continue
		}
		pages, err := os.ReadDir(filepath.Join(cacheDir, fam.Name()))
		if err != nil {
			return o, fmt.Errorf("specoracle: read family %s: %w", fam.Name(), err)
		}
		for _, page := range pages {
			if page.IsDir() || !strings.HasSuffix(page.Name(), ".json") {
				continue
			}
			rel := fam.Name() + "/" + page.Name()
			data, err := os.ReadFile(filepath.Join(cacheDir, fam.Name(), page.Name()))
			if err != nil {
				return o, fmt.Errorf("specoracle: read %s: %w", rel, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				o.Errors = append(o.Errors, fmt.Sprintf("%s: %v", rel, err))
				continue
			}
			o.add(doc, fam.Name(), rel)
		}
	}
	return o, nil
}

// LoadOurs reads the project's own YAML specs the same way, so both sides go
// through one normaliser and the comparison cannot be skewed by a second parser.
func LoadOurs(paths ...string) (Oracle, error) {
	o := Oracle{Ops: map[string]Operation{}}
	if len(paths) == 0 {
		return o, errNoSpecPaths
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return o, fmt.Errorf("specoracle: read spec: %w", err)
		}
		var node any
		if err := yaml.Unmarshal(data, &node); err != nil {
			return o, fmt.Errorf("%s: %w", p, err)
		}
		doc, ok := toJSONish(node).(map[string]any)
		if !ok {
			return o, fmt.Errorf("%s: %w", p, errNotMapping)
		}
		o.add(doc, "ours", filepath.Base(p))
	}
	return o, nil
}

// toJSONish converts yaml.v3's map[string]any / []any output into the same
// shape json.Unmarshal produces, so the walker handles both inputs.
func toJSONish(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = toJSONish(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = toJSONish(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = toJSONish(t[i])
		}
		return t
	case int:
		return float64(t)
	default:
		return v
	}
}

var (
	errNoSpecPaths = errors.New("specoracle: no spec paths given")
	errNotMapping  = errors.New("specoracle: spec is not a mapping")
)

var pathParam = regexp.MustCompile(`\{[^}/]*\}`)

// NormalisePath replaces every {name} with {} so differently named path
// parameters still match.
func NormalisePath(p string) string { return pathParam.ReplaceAllString(p, "{}") }

func (o *Oracle) add(doc map[string]any, family, source string) {
	base := serverBasePath(doc)
	paths, _ := doc["paths"].(map[string]any)
	r := resolver{doc: doc}
	for p, item := range paths {
		pi, ok := item.(map[string]any)
		if !ok {
			continue
		}
		full := joinPath(base, p)
		shared := r.params(pi["parameters"])
		for method, opv := range pi {
			op, ok := opv.(map[string]any)
			if !ok || !isMethod(method) {
				continue
			}
			rec := Operation{
				Family:    family,
				Source:    source,
				Method:    strings.ToUpper(method),
				Path:      full,
				Responses: map[string][]string{},
			}
			rec.ID, _ = op["operationId"].(string)
			rec.Deprecated, _ = op["deprecated"].(bool)
			rec.Key = rec.Method + " " + NormalisePath(full)
			rec.Params = mergeParams(shared, r.params(op["parameters"]))
			if rb, ok := r.deref(op["requestBody"]).(map[string]any); ok {
				rec.RequestMedia, rec.RequestProps = r.content(rb["content"])
			}
			if resps, ok := op["responses"].(map[string]any); ok {
				for code, rv := range resps {
					resp, _ := r.deref(rv).(map[string]any)
					_, props := r.content(resp["content"])
					rec.Responses[code] = props
				}
			}
			if prev, ok := o.Ops[rec.Key]; ok {
				rec = mergeOps(prev, rec)
			}
			o.Ops[rec.Key] = rec
		}
	}
}

// mergeOps folds two operations that share a key into one. JFrog documents
// some wire operations as several pages on the same path template (deploy,
// deploy by checksum and deploy from archive are all PUT /{repo}/{path};
// replication update and enable/disable are both POST /replications/{x}).
// The server sees one route, so the union is the contract: every parameter,
// media type, property and status of either variant, with a parameter required
// only when every variant requires it. Without the merge the last page loaded
// wins and the comparison changes from run to run.
func mergeOps(a, b Operation) Operation {
	out := a
	if out.ID == "" || (b.ID != "" && b.ID < out.ID) {
		out.ID, out.Source = b.ID, b.Source
	}
	out.Deprecated = a.Deprecated && b.Deprecated
	out.Params = unionParams(a.Params, b.Params)
	out.RequestMedia = unionStrings(a.RequestMedia, b.RequestMedia)
	out.RequestProps = unionStrings(a.RequestProps, b.RequestProps)
	out.Responses = make(map[string][]string, len(a.Responses)+len(b.Responses))
	for code, props := range a.Responses {
		out.Responses[code] = props
	}
	for code, props := range b.Responses {
		out.Responses[code] = unionStrings(out.Responses[code], props)
	}
	return out
}

func unionParams(a, b []Param) []Param {
	index := map[string]int{}
	out := make([]Param, 0, len(a)+len(b))
	for _, p := range a {
		index[p.In+":"+p.Name] = len(out)
		out = append(out, p)
	}
	inA := len(out)
	seenInB := map[string]bool{}
	for _, p := range b {
		k := p.In + ":" + p.Name
		seenInB[k] = true
		if i, ok := index[k]; ok {
			out[i].Required = out[i].Required && p.Required
			out[i].Enum = unionStrings(out[i].Enum, p.Enum)
			continue
		}
		p.Required = false
		index[k] = len(out)
		out = append(out, p)
	}
	for i := range inA {
		if !seenInB[out[i].In+":"+out[i].Name] {
			out[i].Required = false
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].In+":"+out[i].Name < out[j].In+":"+out[j].Name
	})
	return out
}

func unionStrings(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	set := make(map[string]bool, len(a)+len(b))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func isMethod(s string) bool {
	switch strings.ToLower(s) {
	case "get", "put", "post", "delete", "patch", "head", "options":
		return true
	}
	return false
}

// serverBasePath returns the path component of servers[0].url. JFrog's URLs
// have a variable in the host ("https://{jfrog_url}/artifactory/api"), which
// url.Parse rejects, so the split is done by hand.
func serverBasePath(doc map[string]any) string {
	servers, _ := doc["servers"].([]any)
	if len(servers) == 0 {
		return ""
	}
	first, _ := servers[0].(map[string]any)
	u, _ := first["url"].(string)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.Index(u, "/"); j >= 0 {
			u = u[j:]
		} else {
			u = ""
		}
	}
	return strings.TrimRight(u, "/")
}

func joinPath(base, p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return base + p
}

type resolver struct {
	doc map[string]any
}

// deref follows a local $ref ("#/components/...") one level.
func (r resolver) deref(v any) any {
	for range 8 {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return v
		}
		var cur any = r.doc
		for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
			cm, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = cm[seg]
		}
		v = cur
	}
	return v
}

func (r resolver) params(v any) []Param {
	list, _ := v.([]any)
	var out []Param
	for _, pv := range list {
		pm, ok := r.deref(pv).(map[string]any)
		if !ok {
			continue
		}
		p := Param{}
		p.Name, _ = pm["name"].(string)
		p.In, _ = pm["in"].(string)
		p.Required, _ = pm["required"].(bool)
		if sch, ok := r.deref(pm["schema"]).(map[string]any); ok {
			p.Type = schemaType(sch)
			p.Enum = enumValues(sch)
		}
		out = append(out, p)
	}
	return out
}

func mergeParams(shared, own []Param) []Param {
	seen := map[string]bool{}
	var out []Param
	for _, p := range own {
		seen[p.In+":"+p.Name] = true
		out = append(out, p)
	}
	for _, p := range shared {
		if !seen[p.In+":"+p.Name] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].In != out[j].In {
			return out[i].In < out[j].In
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// content returns the media types and the union of body property paths.
func (r resolver) content(v any) ([]string, []string) {
	cm, _ := v.(map[string]any)
	if len(cm) == 0 {
		return nil, nil
	}
	media := make([]string, 0, len(cm))
	props := map[string]bool{}
	for mt, mv := range cm {
		media = append(media, mt)
		mm, _ := mv.(map[string]any)
		r.walk(mm["schema"], "", props, map[string]bool{}, 0)
	}
	sort.Strings(media)
	return media, sortedKeys(props)
}

// walk collects property paths. Refs are tracked per branch so recursive
// schemas terminate; depth is capped because clients never nest deeper.
func (r resolver) walk(v any, prefix string, out map[string]bool, seen map[string]bool, depth int) {
	if depth > 8 {
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if ref, ok := m["$ref"].(string); ok {
		if seen[ref] {
			return
		}
		seen = copyWith(seen, ref)
		m, ok = r.deref(m).(map[string]any)
		if !ok {
			return
		}
	}
	for _, comb := range []string{"allOf", "oneOf", "anyOf"} {
		if list, ok := m[comb].([]any); ok {
			for _, sub := range list {
				r.walk(sub, prefix, out, seen, depth+1)
			}
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for name, sub := range props {
			out[prefix+name] = true
			r.walk(sub, prefix+name+".", out, seen, depth+1)
		}
	}
	if items, ok := m["items"]; ok {
		p := strings.TrimSuffix(prefix, ".")
		r.walk(items, p+"[].", out, seen, depth+1)
	}
	if ap, ok := m["additionalProperties"].(map[string]any); ok {
		r.walk(ap, prefix+"*.", out, seen, depth+1)
	}
}

func copyWith(m map[string]bool, k string) map[string]bool {
	n := make(map[string]bool, len(m)+1)
	for key := range m {
		n[key] = true
	}
	n[k] = true
	return n
}

func schemaType(sch map[string]any) string {
	switch t := sch["type"].(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "null" {
				parts = append(parts, s)
			}
		}
		sort.Strings(parts)
		return strings.Join(parts, "|")
	}
	return ""
}

func enumValues(sch map[string]any) []string {
	list, _ := sch["enum"].([]any)
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, fmt.Sprint(e))
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
