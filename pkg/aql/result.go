package aql

import (
	"bytes"
	"encoding/json"
	"slices"

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
	fields  []Field
	hit     Hit
	build   *BuildHit
	virtual bool
	props   bool
	keys    []string
	stats   []StatField
}

// Response builds the body for hits, which Select or a store returned for
// q and the caller gave their virtual repositories when q includes them.
func (q *Query) Response(hits []Hit) Response {
	row := Row{
		fields: q.Fields(), virtual: q.VirtualRepos(), props: q.Properties(),
		keys: q.PropertyKeys(), stats: q.Stats(),
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
	if r.virtual {
		put(FieldVirtualRepos, append([]string{}, r.hit.VirtualRepos...))
	}
	if r.props {
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
		r.writeStats(&sb, st)
		put("stats", []json.RawMessage{sb.Bytes()})
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
	for _, f := range r.fields {
		var v string
		if k, _ := found.rel.fieldKind(string(f)); k == kindTime {
			if t := found.time(string(f)); !t.IsZero() {
				v = t.UTC().Format(timeFormat)
			}
		} else {
			v = found.text(string(f))
		}
		if v == "" {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeJSON(b, prefix+string(f))
		b.WriteByte(':')
		writeJSON(b, v)
	}
	b.WriteByte('}')
}

// writeStats renders the included statistics as one object, the shape
// jfrog-client-go reads ("stats":[{...}]).
func (r Row) writeStats(b *bytes.Buffer, st *types.ItemStats) {
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
	for _, f := range r.stats {
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
