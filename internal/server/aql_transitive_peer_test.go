package server

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestAccTransitivePeerResponseValidation(t *testing.T) {
	good := transitiveJSON(t, []peerAQLRow{transitiveRow("fresh.txt")})
	for _, body := range []string{"null", "{}", `{"results":null,"range":{"start_pos":0,"end_pos":0,"total":0}}`, string(good) + " {}", strings.Replace(string(good), `"range":`, `"errors":[{"message":"partial"}],"range":`, 1), strings.Replace(string(good), `"total":1`, `"total":2`, 1), strings.Replace(string(good), `"start_pos":0`, `"start_pos":1`, 1), strings.Replace(string(good), `"total":1`, `"total":1,"notification":"partial"`, 1)} {
		if _, err := decodePeerAQL(
			strings.NewReader(body),
			int64(len(body)),
			"source",
			"local",
			10,
		); err == nil {
			t.Fatal("invalid document accepted", body)
		}
	}
	for _, change := range []func(*peerAQLRow){
		func(r *peerAQLRow) { r.Repo = "other" }, func(r *peerAQLRow) { r.Path = "../escape" }, func(r *peerAQLRow) { r.Path = "/absolute" }, func(r *peerAQLRow) { r.Path = "a//b" }, func(r *peerAQLRow) { r.Name = "a/b" }, func(r *peerAQLRow) { r.Name = "x%2fy" }, func(r *peerAQLRow) { r.Type = "unknown" }, func(r *peerAQLRow) { r.Size = -1 }, func(r *peerAQLRow) { r.Name = "." }, func(r *peerAQLRow) { r.ActualSHA1 = "not-a-digest" }, func(r *peerAQLRow) { r.ActualMD5 = "aa" }, func(r *peerAQLRow) { r.Name = strings.Repeat("x", 4097) },
		func(r *peerAQLRow) {
			r.Properties = append(r.Properties, struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}{"", "bad"})
		},
		func(r *peerAQLRow) {
			r.Properties = append(r.Properties, struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}{"key", strings.Repeat("x", 16385)})
		},
	} {
		row := transitiveRow("fresh.txt")
		change(&row)
		body := transitiveJSON(t, []peerAQLRow{row})
		if _, err := decodePeerAQL(
			bytes.NewReader(body),
			int64(len(body)),
			"source",
			"local",
			10,
		); err == nil {
			t.Fatalf("accepted bad row %+v", row)
		}
	}
	a := transitiveRow("same.txt")
	b := a
	b.Size++
	body := transitiveJSON(t, []peerAQLRow{a, b})
	if _, err := decodePeerAQL(
		bytes.NewReader(body),
		int64(len(body)),
		"source",
		"local",
		10,
	); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	for _, size := range []int64{-1, 8<<20 + 1} {
		if _, err := decodePeerAQL(bytes.NewReader(good), size, "source", "local", 10); err == nil {
			t.Fatal("invalid length", size)
		}
	}
	if _, err := decodePeerAQL(
		bytes.NewReader(good),
		int64(len(good)),
		"source",
		"local",
		0,
	); err == nil {
		t.Fatal("row bound not enforced")
	}
	body = transitiveJSON(t, []peerAQLRow{a, a})
	hits, err := decodePeerAQL(bytes.NewReader(body), int64(len(body)), "source", "local", 2)
	if err != nil || len(hits) != 2 || hits[0].Item.Cached || hits[0].Item.ID != 0 {
		t.Fatal("transient duplicate validation", hits, err)
	}
	root := transitiveRow(".")
	root.Type = "folder"
	body = transitiveJSON(t, []peerAQLRow{root})
	if _, err = decodePeerAQL(
		bytes.NewReader(body),
		int64(len(body)),
		"source",
		"local",
		1,
	); err != nil {
		t.Fatal("valid root rejected", err)
	}
	var doc map[string]any
	if err = json.Unmarshal(good, &doc); err != nil {
		t.Fatal(err)
	}
	doc["internal_upstream_url"] = "https://user:secret@upstream.example"
	body, _ = json.Marshal(doc)
	hits, err = decodePeerAQL(bytes.NewReader(body), int64(len(body)), "source", "local", 1)
	if err != nil || len(hits) != 1 {
		t.Fatal("unknown fields", err)
	}
}

type failedPeerReader struct{}

func (failedPeerReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestAccTransitivePeerReaderFailure(t *testing.T) {
	if _, err := decodePeerAQL(
		failedPeerReader{},
		10,
		"source",
		"local",
		10,
	); err != source.ErrInvalidResult {
		t.Fatal(err)
	}
}
