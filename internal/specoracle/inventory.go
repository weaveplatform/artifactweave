package specoracle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The inventory is the fact base for the data model and the authored specs:
// names, types, enums and status codes extracted from the cache. JFrog's
// prose is proprietary (decision 0001), so descriptions, summaries, titles and
// examples are dropped before anything is written; proseKeys lists what goes.
var proseKeys = map[string]bool{
	"description": true,
	"summary":     true,
	"title":       true,
	"example":     true,
	"examples":    true,
	"x-readme":    true,
}

// SchemaField is one property of a named schema.
type SchemaField struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"`
	Format   string   `json:"format,omitempty"`
	Ref      string   `json:"ref,omitempty"`
	Items    string   `json:"items,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Required bool     `json:"required,omitempty"`
	ReadOnly bool     `json:"read_only,omitempty"`
	// WriteOnly marks fields JFrog accepts but never returns, such as passwords.
	WriteOnly bool `json:"write_only,omitempty"`
	Nullable  bool `json:"nullable,omitempty"`
	Default   any  `json:"default,omitempty"`
}

// SchemaRecord is a named component schema within one family. Variants counts
// the distinct shapes the same name has across that family's blocks; anything
// above one means authors must look at both pages before trusting the fields.
type SchemaRecord struct {
	Family   string        `json:"family"`
	Name     string        `json:"name"`
	Type     string        `json:"type,omitempty"`
	Composed []string      `json:"composed,omitempty"`
	Fields   []SchemaField `json:"fields,omitempty"`
	Variants int           `json:"variants"`
	UsedBy   int           `json:"used_by"`
}

// ParamRecord is an operation parameter with the facts clients depend on.
type ParamRecord struct {
	Name     string   `json:"name"`
	In       string   `json:"in"`
	Required bool     `json:"required,omitempty"`
	Type     string   `json:"type,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Default  any      `json:"default,omitempty"`
}

// ContentRecord names the schema behind one media type.
type ContentRecord struct {
	Media  string `json:"media"`
	Schema string `json:"schema,omitempty"`
}

// OpRecord is one documented operation.
type OpRecord struct {
	Key        string                     `json:"key"`
	Family     string                     `json:"family"`
	Tag        string                     `json:"tag,omitempty"`
	ID         string                     `json:"id,omitempty"`
	Method     string                     `json:"method"`
	Path       string                     `json:"path"`
	Source     string                     `json:"source"`
	Params     []ParamRecord              `json:"params,omitempty"`
	Request    []ContentRecord            `json:"request,omitempty"`
	Responses  map[string][]ContentRecord `json:"responses"`
	Deprecated bool                       `json:"deprecated,omitempty"`
}

// Inventory is what `specoracle inventory` writes.
type Inventory struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Pages       int            `json:"pages"`
	Schemas     []SchemaRecord `json:"schemas"`
	Operations  []OpRecord     `json:"operations"`
	Errors      []string       `json:"errors,omitempty"`
}

// block is one cached page, parsed.
type block struct {
	family string
	page   string
	source string
	doc    map[string]any
}

// readBlocks lists the cache the same way Load does and resolves each page's
// source URL through the manifest so the inventory can cite it.
func readBlocks(cacheDir string) ([]block, []string, error) {
	sources := manifestSources(cacheDir)
	families, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, nil, fmt.Errorf("specoracle: read cache %s: %w", cacheDir, err)
	}
	var blocks []block
	var problems []string
	for _, fam := range families {
		if !fam.IsDir() {
			continue
		}
		pages, err := os.ReadDir(filepath.Join(cacheDir, fam.Name()))
		if err != nil {
			return nil, nil, fmt.Errorf("specoracle: read family %s: %w", fam.Name(), err)
		}
		for _, page := range pages {
			if page.IsDir() || !strings.HasSuffix(page.Name(), ".json") {
				continue
			}
			rel := fam.Name() + "/" + page.Name()
			data, err := os.ReadFile(filepath.Join(cacheDir, fam.Name(), page.Name()))
			if err != nil {
				return nil, nil, fmt.Errorf("specoracle: read %s: %w", rel, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
				continue
			}
			src := sources[rel]
			if src == "" {
				src = rel
			}
			blocks = append(blocks, block{
				family: fam.Name(),
				page:   strings.TrimSuffix(page.Name(), ".json"),
				source: src,
				doc:    doc,
			})
		}
	}
	return blocks, problems, nil
}

// manifestSources maps cache file paths to the page URLs they came from. A
// missing or unreadable manifest degrades to citing the cache path.
func manifestSources(cacheDir string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(cacheDir, ManifestFile))
	if err != nil {
		return out
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return out
	}
	for _, e := range m.Entries {
		if e.File != "" {
			out[e.File] = e.URL
		}
	}
	return out
}

// BuildInventory extracts schemas and operations from every cached block.
func BuildInventory(cacheDir string) (Inventory, error) {
	blocks, problems, err := readBlocks(cacheDir)
	if err != nil {
		return Inventory{}, err
	}
	inv := Inventory{GeneratedAt: time.Now().UTC(), Pages: len(blocks), Errors: problems}
	inv.Schemas = inventorySchemas(blocks)
	for _, b := range blocks {
		inv.Operations = append(inv.Operations, inventoryOps(b)...)
	}
	sort.Slice(inv.Operations, func(i, j int) bool {
		if inv.Operations[i].Family != inv.Operations[j].Family {
			return inv.Operations[i].Family < inv.Operations[j].Family
		}
		return inv.Operations[i].Key < inv.Operations[j].Key
	})
	return inv, nil
}

// inventorySchemas deduplicates named schemas per family. The first page in
// name order supplies the field list; other shapes only bump Variants.
func inventorySchemas(blocks []block) []SchemaRecord {
	type acc struct {
		rec    SchemaRecord
		shapes map[string]bool
		first  string
	}
	byKey := map[string]*acc{}
	sorted := append([]block(nil), blocks...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].family != sorted[j].family {
			return sorted[i].family < sorted[j].family
		}
		return sorted[i].page < sorted[j].page
	})
	for _, b := range sorted {
		comps, _ := b.doc["components"].(map[string]any)
		schemas, _ := comps["schemas"].(map[string]any)
		r := resolver{doc: b.doc}
		for name, sv := range schemas {
			sch, ok := sv.(map[string]any)
			if !ok {
				continue
			}
			key := b.family + "/" + name
			shape := canonical(stripProse(sch))
			a := byKey[key]
			if a == nil {
				a = &acc{shapes: map[string]bool{}, first: b.page}
				a.rec = schemaRecord(b.family, name, sch, r)
				byKey[key] = a
			}
			a.rec.UsedBy++
			a.shapes[shape] = true
		}
	}
	out := make([]SchemaRecord, 0, len(byKey))
	for _, a := range byKey {
		a.rec.Variants = len(a.shapes)
		out = append(out, a.rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func schemaRecord(family, name string, sch map[string]any, r resolver) SchemaRecord {
	rec := SchemaRecord{Family: family, Name: name, Type: schemaType(sch)}
	for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
		list, _ := sch[comb].([]any)
		for _, sub := range list {
			sm, _ := sub.(map[string]any)
			if ref, ok := sm["$ref"].(string); ok {
				rec.Composed = append(rec.Composed, comb+":"+refName(ref))
			} else {
				rec.Fields = append(rec.Fields, fields(sm, r)...)
			}
		}
		if len(list) > 0 && rec.Type == "" {
			rec.Type = comb
		}
	}
	if len(rec.Fields) == 0 && len(rec.Composed) == 0 && rec.Type == "" &&
		sch["properties"] != nil {
		rec.Type = "object"
	}
	rec.Fields = append(rec.Fields, fields(sch, r)...)
	// A bare enum schema (ChecksumPolicyType and friends) has no properties;
	// its values are the data model, so they get a pseudo field.
	if ev := enumValues(sch); len(ev) > 0 {
		rec.Fields = append(rec.Fields, SchemaField{Name: "(value)", Type: rec.Type, Enum: ev})
	}
	sort.SliceStable(
		rec.Fields,
		func(i, j int) bool { return rec.Fields[i].Name < rec.Fields[j].Name },
	)
	return rec
}

// fields lists the direct properties of a schema object.
func fields(sch map[string]any, r resolver) []SchemaField {
	props, _ := sch["properties"].(map[string]any)
	required := map[string]bool{}
	if list, ok := sch["required"].([]any); ok {
		for _, x := range list {
			required[fmt.Sprint(x)] = true
		}
	}
	out := make([]SchemaField, 0, len(props))
	for name, pv := range props {
		pm, _ := pv.(map[string]any)
		f := SchemaField{Name: name, Required: required[name]}
		if ref, ok := pm["$ref"].(string); ok {
			f.Ref = refName(ref)
			pm, _ = r.deref(pm).(map[string]any)
		}
		f.Type = schemaType(pm)
		f.Format, _ = pm["format"].(string)
		f.Enum = enumValues(pm)
		f.ReadOnly, _ = pm["readOnly"].(bool)
		f.WriteOnly, _ = pm["writeOnly"].(bool)
		f.Nullable = isNullable(pm)
		f.Default = pm["default"]
		if items, ok := pm["items"].(map[string]any); ok {
			if ref, ok := items["$ref"].(string); ok {
				f.Items = refName(ref)
			} else {
				f.Items = schemaType(items)
			}
		}
		out = append(out, f)
	}
	return out
}

func isNullable(sch map[string]any) bool {
	if n, ok := sch["nullable"].(bool); ok && n {
		return true
	}
	if list, ok := sch["type"].([]any); ok {
		for _, x := range list {
			if x == "null" {
				return true
			}
		}
	}
	return false
}

func refName(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// inventoryOps extracts every operation of one block with schema names rather
// than flattened property paths, which is what spec authors need.
func inventoryOps(b block) []OpRecord {
	base := serverBasePath(b.doc)
	paths, _ := b.doc["paths"].(map[string]any)
	r := resolver{doc: b.doc}
	var out []OpRecord
	for p, item := range paths {
		pi, ok := item.(map[string]any)
		if !ok {
			continue
		}
		full := joinPath(base, p)
		shared := paramRecords(pi["parameters"], r)
		for method, opv := range pi {
			op, ok := opv.(map[string]any)
			if !ok || !isMethod(method) {
				continue
			}
			rec := OpRecord{
				Family:    b.family,
				Method:    strings.ToUpper(method),
				Path:      full,
				Source:    b.source,
				Responses: map[string][]ContentRecord{},
			}
			rec.Key = rec.Method + " " + NormalisePath(full)
			rec.ID, _ = op["operationId"].(string)
			rec.Deprecated, _ = op["deprecated"].(bool)
			if tags, ok := op["tags"].([]any); ok && len(tags) > 0 {
				rec.Tag = fmt.Sprint(tags[0])
			}
			rec.Params = mergeParamRecords(shared, paramRecords(op["parameters"], r))
			if rb, ok := r.deref(op["requestBody"]).(map[string]any); ok {
				rec.Request = contentRecords(rb["content"])
			}
			if resps, ok := op["responses"].(map[string]any); ok {
				for code, rv := range resps {
					resp, _ := r.deref(rv).(map[string]any)
					rec.Responses[code] = contentRecords(resp["content"])
				}
			}
			out = append(out, rec)
		}
	}
	return out
}

func paramRecords(v any, r resolver) []ParamRecord {
	list, _ := v.([]any)
	out := make([]ParamRecord, 0, len(list))
	for _, pv := range list {
		pm, ok := r.deref(pv).(map[string]any)
		if !ok {
			continue
		}
		p := ParamRecord{}
		p.Name, _ = pm["name"].(string)
		p.In, _ = pm["in"].(string)
		p.Required, _ = pm["required"].(bool)
		if sch, ok := r.deref(pm["schema"]).(map[string]any); ok {
			p.Type = schemaType(sch)
			p.Enum = enumValues(sch)
			p.Default = sch["default"]
		}
		out = append(out, p)
	}
	return out
}

func mergeParamRecords(shared, own []ParamRecord) []ParamRecord {
	seen := map[string]bool{}
	out := make([]ParamRecord, 0, len(shared)+len(own))
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

func contentRecords(v any) []ContentRecord {
	cm, _ := v.(map[string]any)
	if len(cm) == 0 {
		return nil
	}
	out := make([]ContentRecord, 0, len(cm))
	for mt, mv := range cm {
		mm, _ := mv.(map[string]any)
		rec := ContentRecord{Media: mt}
		if sch, ok := mm["schema"].(map[string]any); ok {
			if ref, ok := sch["$ref"].(string); ok {
				rec.Schema = refName(ref)
			} else if items, ok := sch["items"].(map[string]any); ok {
				if ref, ok := items["$ref"].(string); ok {
					rec.Schema = "[]" + refName(ref)
				} else {
					rec.Schema = "[]" + schemaType(items)
				}
			} else {
				rec.Schema = schemaType(sch)
			}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Media < out[j].Media })
	return out
}

// stripProse returns a deep copy without the keys in proseKeys, so shapes
// can be compared and written without carrying JFrog's text.
func stripProse(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if proseKeys[k] {
				continue
			}
			out[k] = stripProse(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = stripProse(t[i])
		}
		return out
	default:
		return v
	}
}

// canonical renders a value as sorted-key JSON for shape comparison.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

var errNoInventoryPath = errors.New("specoracle: inventory path is required")

// WriteJSON writes the inventory as indented JSON.
func (i Inventory) WriteJSON(path string) error {
	if path == "" {
		return errNoInventoryPath
	}
	data, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return fmt.Errorf("specoracle: marshal inventory: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("specoracle: write %s: %w", path, err)
	}
	return nil
}

// WriteMarkdown writes the human-readable inventory: one operations table and
// one fields table per family, no prose from the source.
func (i Inventory) WriteMarkdown(path string) error {
	if path == "" {
		return errNoInventoryPath
	}
	var sb strings.Builder
	sb.WriteString("# Data model inventory (generated by `make oracle-inventory`, do not edit)\n\n")
	fmt.Fprintf(&sb, "Generated %s from %d cached pages: %d operations, %d named schemas.\n\n",
		i.GeneratedAt.Format("2006-01-02"), i.Pages, len(i.Operations), len(i.Schemas))
	sb.WriteString("Facts only: names, types, enums, required flags and status codes. ")
	sb.WriteString("Descriptions are written by the project in `api/src/` (decision 0001).\n")
	families := i.families()
	for _, fam := range families {
		fmt.Fprintf(&sb, "\n## %s\n\n### Operations\n\n", fam)
		sb.WriteString(
			"| Method | Path | Operation | Params | Request | Responses | Deprecated |\n|---|---|---|---|---|---|---|\n",
		)
		for _, op := range i.Operations {
			if op.Family != fam {
				continue
			}
			fmt.Fprintf(&sb, "| %s | `%s` | %s | %s | %s | %s | %s |\n",
				op.Method, op.Path, op.ID, paramSummary(op.Params), contentSummary(op.Request),
				responseSummary(op.Responses), yesNo(op.Deprecated))
		}
		sb.WriteString(
			"\n### Schemas\n\n| Schema | Field | Type | Enum | Required | Used by |\n|---|---|---|---|---|---|\n",
		)
		for _, s := range i.Schemas {
			if s.Family != fam {
				continue
			}
			label := s.Name
			if s.Variants > 1 {
				label = fmt.Sprintf("%s (%d shapes)", s.Name, s.Variants)
			}
			if len(s.Fields) == 0 || len(s.Composed) > 0 {
				fmt.Fprintf(&sb, "| %s | | %s | | | %d |\n", label, composedSummary(s), s.UsedBy)
			}
			for _, f := range s.Fields {
				fmt.Fprintf(
					&sb,
					"| %s | `%s` | %s | %s | %s | %d |\n",
					label,
					f.Name,
					fieldType(f),
					strings.Join(f.Enum, ", "),
					yesNo(f.Required),
					s.UsedBy,
				)
			}
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("specoracle: write %s: %w", path, err)
	}
	return nil
}

func (i Inventory) families() []string {
	set := map[string]bool{}
	for _, op := range i.Operations {
		set[op.Family] = true
	}
	for _, s := range i.Schemas {
		set[s.Family] = true
	}
	return sortedKeys(set)
}

func paramSummary(ps []ParamRecord) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := p.In + ":" + p.Name
		if p.Required {
			s += "*"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func contentSummary(cs []ContentRecord) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Schema != "" {
			parts = append(parts, c.Media+" "+c.Schema)
		} else {
			parts = append(parts, c.Media)
		}
	}
	return strings.Join(parts, "; ")
}

func responseSummary(rs map[string][]ContentRecord) string {
	codes := make([]string, 0, len(rs))
	for c := range rs {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		if cs := contentSummary(rs[c]); cs != "" {
			parts = append(parts, c+" ("+cs+")")
		} else {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "; ")
}

func composedSummary(s SchemaRecord) string {
	if len(s.Composed) > 0 {
		return strings.Join(s.Composed, ", ")
	}
	return s.Type
}

func fieldType(f SchemaField) string {
	switch {
	case f.Ref != "":
		return f.Ref
	case f.Items != "":
		return "[]" + f.Items
	case f.Format != "":
		return f.Type + " (" + f.Format + ")"
	}
	return f.Type
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return ""
}
