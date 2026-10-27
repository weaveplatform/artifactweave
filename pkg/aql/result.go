package aql

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// timeFormat is how results render times: ISO 8601 with milliseconds, as
// Artifactory does.
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// Response is the body of POST /api/search/aql:
// {"results":[...],"range":{"start_pos","end_pos","total","limit"?,"notification"?}}.
// It is marshalled by hand so rows keep a fixed field order.
type Response struct {
	Rows []Row
	// Start is the offset; Total the number of rows returned.
	Start, Total int
	// Limit is the query's limit, rendered only when it had one.
	Limit    int
	HasLimit bool
	// Notification explains a capped result; empty renders nothing.
	Notification string
}

// Row is one result: the query's fields of an item and, when included, its
// virtual repositories, properties and statistics; or the query's fields
// of a run or a promotion.
type Row struct {
	fields                    []Field
	hit                       Hit
	build                     *BuildHit
	shouldIncludeVirtualRepos bool
	shouldIncludeProperties   bool
	keys                      []string
	stats                     []StatField
	related                   *relRow
}

// Response builds the body for hits, which Select or a store returned for
// q and the caller gave their virtual repositories when q includes them.
func (q *Query) Response(hits []Hit) Response {
	row := Row{
		fields:                    q.Fields(),
		shouldIncludeVirtualRepos: q.VirtualRepos(),
		shouldIncludeProperties:   q.Properties(),
		keys:                      q.PropertyKeys(),
		stats:                     q.Stats(),
	}
	rows := make([]Row, len(hits))
	for i, h := range hits {
		rows[i] = row
		rows[i].hit = h
	}
	return Response{
		Rows: rows, Start: q.Offset, Total: len(rows), Limit: q.Limit, HasLimit: q.HasLimit,
	}
}

// BuildResponse builds the body for hits, which SelectBuilds or a store
// returned for a builds or build.promotions query.
func (q *Query) BuildResponse(hits []BuildHit) Response {
	fields := q.Fields()
	rows := make([]Row, len(hits))
	for i := range hits {
		rows[i] = Row{fields: fields, build: &hits[i]}
	}
	return Response{
		Rows: rows, Start: q.Offset, Total: len(rows), Limit: q.Limit, HasLimit: q.HasLimit,
	}
}

// MarshalJSON renders the response.
func (r Response) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"results":[`)
	for i, row := range r.Rows {
		if i > 0 {
			b.WriteByte(',')
		}
		row.write(&b)
	}
	b.WriteString(`],"range":{"start_pos":`)
	writeJSON(&b, r.Start)
	b.WriteString(`,"end_pos":`)
	writeJSON(&b, r.Start+r.Total)
	b.WriteString(`,"total":`)
	writeJSON(&b, r.Total)
	if r.HasLimit {
		b.WriteString(`,"limit":`)
		writeJSON(&b, r.Limit)
	}
	if r.Notification != "" {
		b.WriteString(`,"notification":`)
		writeJSON(&b, r.Notification)
	}
	b.WriteString("}}")
	return b.Bytes(), nil
}

// MarshalJSON renders the row alone.
func (r Row) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	r.write(&b)
	return b.Bytes(), nil
}

// write renders the row. Empty texts and missing times are left out, as
// Artifactory leaves out null fields; numbers are always present. An item
// without a statistics record renders no "stats".
func (r Row) write(b *bytes.Buffer) {
	if r.build != nil {
		r.writeBuild(b)
		return
	}
	it := r.hit.Item
	b.WriteByte('{')
	first := true
	put := func(key string, v any) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeJSON(b, key)
		b.WriteByte(':')
		writeJSON(b, v)
	}
	for _, f := range r.fields {
		switch fieldKinds[f] {
		case kindType:
			put(string(f), itemType(it))
		case kindNumber:
			put(string(f), number(it, f))
		case kindTime:
			if t := itemTime(it, f); !t.IsZero() {
				put(string(f), t.UTC().Format(timeFormat))
			}
		default:
			if s := text(it, f); s != "" || f == FieldPath || f == FieldName || f == FieldRepo {
				put(string(f), s)
			}
		}
	}
	for _, p := range r.related.parts() {
		put(p.key, p.arr)
	}
	if r.shouldIncludeVirtualRepos {
		put(FieldVirtualRepos, append([]string{}, r.hit.VirtualRepos...))
	}
	if r.shouldIncludeProperties {
		type kv struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		var list []kv
		keys := make([]string, 0, len(r.hit.Properties))
		for k := range r.hit.Properties {
			if r.keys == nil || slices.Contains(r.keys, k) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			for _, v := range r.hit.Properties[k] {
				list = append(list, kv{k, v})
			}
		}
		if len(list) > 0 {
			put("properties", list)
		}
	}
	if st := r.hit.Stats; len(r.stats) > 0 && st != nil {
		var sb bytes.Buffer
		writeStats(&sb, st, r.stats)
		// A record with none of the included values is no row: 6.20 drops a
		// row without data (InflatableRow.clean), as Nest does for an item
		// nested below another row.
		if sb.Len() > 2 {
			put("stats", []json.RawMessage{sb.Bytes()})
		}
	}
	b.WriteByte('}')
}

// writeBuild renders a run's fields as "build.<field>", or a promotion's
// as "build.promotion.<field>" (Artifactory's AqlRestResult names). Empty
// texts and missing times are left out, as for items.
func (r Row) writeBuild(b *bytes.Buffer) {
	found, prefix := hitRow(r.build), "build."
	if found.rel == relPromotion {
		prefix = "build.promotion."
	}
	b.WriteByte('{')
	first := true
	put := func(key string, v any) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeJSON(b, key)
		b.WriteByte(':')
		writeJSON(b, v)
	}
	for _, f := range r.fields {
		if v, ok := relValue(found, string(f)); ok {
			put(prefix+string(f), v)
		}
	}
	for _, p := range r.related.parts() {
		put(p.key, p.arr)
	}
	b.WriteByte('}')
}

// render writes the group's rows as a JSON array, leaving out the rows
// with nothing to show; ok is false when none is left, and the group is
// then left out, as Artifactory drops related rows without data.
func (g relGroup) render() (arr []byte, ok bool) {
	var b bytes.Buffer
	b.WriteByte('[')
	for _, rr := range g.rows {
		obj, data := rr.render()
		if !data {
			continue
		}
		if ok {
			b.WriteByte(',')
		}
		ok = true
		b.Write(obj)
	}
	b.WriteByte(']')
	return b.Bytes(), ok
}

// render writes a nested row: its node's fields with their domain's
// prefix, then its groups. data is false when it shows nothing.
func (rr *relRow) render() (obj []byte, data bool) {
	var b bytes.Buffer
	b.WriteByte('{')
	put := func(key string, v any) {
		if data {
			b.WriteByte(',')
		}
		data = true
		writeJSON(&b, key)
		b.WriteByte(':')
		writeJSON(&b, v)
	}
	for _, f := range rr.node.fields {
		if v, ok := relValue(rr.r, f); ok {
			put(relFieldPrefixes[rr.node.rel]+f, v)
		}
	}
	for _, p := range rr.parts() {
		put(p.key, p.arr)
	}
	b.WriteByte('}')
	return b.Bytes(), data
}

// relArray is one array a row renders after its fields: a group of related
// rows, or the properties or statistics of a nested row or a run.
type relArray struct {
	key string
	arr json.RawMessage
}

// parts are the arrays rr renders after its fields, ordered by key, as
// Artifactory's rows serialise their keys alphabetically
// (AqlRestResult.Row): the groups with rows to show, its properties as
// {"<prefix>key":k,"<prefix>value":v} rows, and its statistics. A nil rr,
// the holder of a found row nothing nests in, has none.
func (rr *relRow) parts() []relArray {
	if rr == nil {
		return nil
	}
	var out []relArray
	for _, g := range rr.groups {
		if arr, ok := g.render(); ok {
			out = append(out, relArray{g.key, arr})
		}
	}
	if len(rr.props) > 0 {
		prefix := propFieldPrefixes[rr.node.rel]
		var b bytes.Buffer
		b.WriteByte('[')
		for i, kv := range rr.props {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('{')
			writeJSON(&b, prefix+"key")
			b.WriteByte(':')
			writeJSON(&b, kv[0])
			b.WriteByte(',')
			writeJSON(&b, prefix+"value")
			b.WriteByte(':')
			writeJSON(&b, kv[1])
			b.WriteByte('}')
		}
		b.WriteByte(']')
		out = append(out, relArray{propGroupKeys[rr.node.rel], b.Bytes()})
	}
	if len(rr.stats) > 0 {
		arr := append([]byte{'['}, bytes.Join(rr.stats, []byte{','})...)
		out = append(out, relArray{"stats", append(arr, ']')})
	}
	slices.SortStableFunc(out, func(a, b relArray) int { return strings.Compare(a.key, b.key) })
	return out
}

// writeStats renders the statistic fields of st as one object, the shape
// jfrog-client-go reads ("stats":[{...}]).
func writeStats(b *bytes.Buffer, st *types.ItemStats, fields []StatField) {
	b.WriteByte('{')
	first := true
	put := func(key StatField, v any) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeJSON(b, string(key))
		b.WriteByte(':')
		writeJSON(b, v)
	}
	for _, f := range fields {
		switch statKinds[f] {
		case kindNumber:
			put(f, statNumber(st, f))
		case kindTime:
			if t := statTime(st, f); !t.IsZero() {
				put(f, t.UTC().Format(timeFormat))
			}
		default:
			if s := statText(st, f); s != "" {
				put(f, s)
			}
		}
	}
	b.WriteByte('}')
}

// writeJSON appends v's JSON. Every value here (strings, integers, the
// property list) marshals without error.
func writeJSON(b *bytes.Buffer, v any) {
	out, _ := json.Marshal(v)
	b.Write(out)
}
