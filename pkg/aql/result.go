package aql

import (
	"bytes"
	"encoding/json"
	"slices"
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
// properties.
type Row struct {
	fields []Field
	hit    Hit
	props  bool
}

// Response builds the body for hits, which Select or a store returned for
// q.
func (q *Query) Response(hits []Hit) Response {
	fields, props := q.Fields(), q.Properties()
	rows := make([]Row, len(hits))
	for i, h := range hits {
		rows[i] = Row{fields: fields, hit: h, props: props}
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
// Artifactory leaves out null fields; numbers are always present.
func (r Row) write(b *bytes.Buffer) {
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
	if r.props && len(r.hit.Properties) > 0 {
		type kv struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		var list []kv
		keys := make([]string, 0, len(r.hit.Properties))
		for k := range r.hit.Properties {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			for _, v := range r.hit.Properties[k] {
				list = append(list, kv{k, v})
			}
		}
		put("properties", list)
	}
	b.WriteByte('}')
}

// writeJSON appends v's JSON. Every value here (strings, integers, the
// property list) marshals without error.
func writeJSON(b *bytes.Buffer, v any) {
	out, _ := json.Marshal(v)
	b.Write(out)
}
