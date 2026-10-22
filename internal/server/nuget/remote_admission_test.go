package nuget_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestRemoteV3AdmissionRejectsInvalidWireDocuments(t *testing.T) {
	c := repo.Config{
		URL:       "https://upstream.example",
		V3FeedURL: "https://upstream.example/index.json",
	}
	cases := []struct{ name, kind, id, raw string }{
		{"index empty", "index", "", `{"version":"3.0.0","resources":[]}`},
		{
			"index version",
			"index",
			"",
			`{"version":"4.0.0","resources":[{"@id":"https://upstream.example/flat/","@type":"PackageBaseAddress/3.0.0"}]}`,
		},
		{
			"index missing type",
			"index",
			"",
			`{"version":"3.0.0","resources":[{"@id":"https://upstream.example/flat/"}]}`,
		},
		{"version invalid", "versions", "", `{"versions":["not-semver"]}`},
		{"version duplicate identity", "versions", "", `{"versions":["1.0","1.0.0"]}`},
		{"versions null", "versions", "", `{"versions":null}`},
		{
			"registration lower",
			"registration",
			"acme",
			`{"count":1,"items":[{"@id":"https://upstream.example/page","count":1,"lower":"bad","upper":"1.0.0"}]}`,
		},
		{
			"registration upper",
			"registration",
			"acme",
			`{"count":1,"items":[{"@id":"https://upstream.example/page","count":1,"lower":"1.0.0","upper":"bad"}]}`,
		},
		{
			"registration relative page",
			"registration",
			"acme",
			`{"count":1,"items":[{"@id":"../page","count":1,"lower":"1.0.0","upper":"1.0.0"}]}`,
		},
		{"registration count", "registration", "acme", `{"count":2,"items":[]}`},
		{
			"registration inline",
			"registration",
			"acme",
			`{"count":1,"items":[{"@id":"https://upstream.example/page","count":1,"lower":"1.0.0","upper":"1.0.0","items":[{}]}]}`,
		},
		{"page self URL", "page", "acme", `{"@id":"file:///secret","count":0,"items":[]}`},
		{"search null", "search", "", `{"data":null}`},
		{
			"search id",
			"search",
			"",
			`{"data":[{"id":"../secret","version":"1.0.0","versions":[]}]}`,
		},
		{"search version", "search", "", `{"data":[{"id":"acme","version":"bad","versions":[]}]}`},
		{"search missing versions", "search", "", `{"data":[{"id":"acme","version":"1.0.0"}]}`},
		{
			"search listed version",
			"search",
			"",
			`{"data":[{"id":"acme","version":"1.0.0","versions":[{"version":"bad"}]}]}`,
		},
		{"autocomplete null", "autocomplete-id", "", `{"data":null}`},
		{"autocomplete id", "autocomplete-id", "", `{"data":["../private"]}`},
		{"autocomplete version", "autocomplete-version", "", `{"data":["bad"]}`},
		{"signature all field", "signatures", "5.0.0", `{"signingCertificates":[]}`},
		{
			"signature null certificates",
			"signatures",
			"5.0.0",
			`{"allRepositorySigned":true,"signingCertificates":null}`,
		},
		{
			"signature old version",
			"signatures",
			"4.7.0",
			`{"allRepositorySigned":true,"signingCertificates":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := nuget.DecodeRemote(strings.NewReader(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if err = nuget.ValidateRemoteDocument(c, d, tc.kind, tc.id); err == nil {
				t.Fatal("invalid upstream document admitted")
			}
		})
	}
	// Each malformed leaf is validated even if the first caller cannot read it.
	base := map[string]any{
		"@id":            "https://upstream.example/leaf",
		"packageContent": "https://upstream.example/pkg",
		"catalogEntry": map[string]any{
			"@id":     "https://upstream.example/catalog",
			"id":      "acme",
			"version": "1.0.0",
			"listed":  true,
		},
	}
	mutations := map[string]func(map[string]any, map[string]any){
		"missing content":  func(d, cat map[string]any) { delete(d, "packageContent") },
		"unsafe content":   func(d, cat map[string]any) { d["packageContent"] = "https://user:secret@upstream.example/pkg" },
		"relative catalog": func(d, cat map[string]any) { cat["@id"] = "catalog" },
		"wrong id":         func(d, cat map[string]any) { cat["id"] = "other" },
		"wrong version":    func(d, cat map[string]any) { cat["version"] = "bad" },
		"listed type":      func(d, cat map[string]any) { cat["listed"] = "true" },
		"dependency id": func(d, cat map[string]any) {
			cat["dependencyGroups"] = []any{
				map[string]any{"dependencies": []any{map[string]string{"id": "../bad"}}},
			}
		},
		"hash algorithm": func(d, cat map[string]any) { cat["packageHashAlgorithm"] = "MD5"; cat["packageHash"] = "abc" },
		"hash encoding":  func(d, cat map[string]any) { cat["packageHashAlgorithm"] = "SHA512"; cat["packageHash"] = "!!!" },
		"hash size":      func(d, cat map[string]any) { cat["packageHashAlgorithm"] = "SHA512"; cat["packageHash"] = "YWJj" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var leaf map[string]any
			_ = json.Unmarshal(raw, &leaf)
			mutate(leaf, leaf["catalogEntry"].(map[string]any))
			raw, _ = json.Marshal(map[string]any{"count": 1, "items": []any{leaf}})
			d, _ := nuget.DecodeRemote(bytes.NewReader(raw))
			if nuget.ValidateRemoteDocument(c, d, "page", "acme") == nil {
				t.Fatal("malformed catalog admitted")
			}
		})
	}
}

func TestRemoteV3URLRulesAndBoundedDecoding(t *testing.T) {
	c := repo.Config{URL: "https://up.example", V3FeedURL: "https://up.example/index.json"}
	for _, raw := range []string{"http://up.example/pkg", "https://up.example/a/../b", "https://up.example/a/%2e%2e/b", "https://up.example/%00", "https://up.example\\evil/path", "https://up.example/path#fragment", "https://localhost/path", "https://127.0.0.1/path", "https://[::1]/path", "https://10.0.0.1/path", "ftp://up.example/a", "https://user:pass@up.example/a"} {
		if nuget.ValidateRemoteURL(c, raw, false) == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://up.example/ok?sig=kept", "https://cdn.example/pkg"} {
		if err := nuget.ValidateRemoteURL(c, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range [][]byte{[]byte("null"), []byte("{} junk"), {0x1f, 0x8b, 0}, bytes.Repeat([]byte("x"), (16<<20)+1)} {
		if _, err := nuget.DecodeRemote(bytes.NewReader(raw)); err == nil {
			t.Fatal("bad document accepted")
		}
	}
	var valid bytes.Buffer
	writer := gzip.NewWriter(&valid)
	_, _ = writer.Write([]byte(`{"versions":[]}`))
	_ = writer.Close()
	badTrailer := valid.Bytes()
	badTrailer[len(badTrailer)-1] ^= 0xff
	if _, err := nuget.DecodeRemote(bytes.NewReader(badTrailer)); err == nil {
		t.Fatal("gzip trailer corruption ignored")
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	_, _ = gz.Write(bytes.Repeat([]byte("x"), (16<<20)+1))
	_ = gz.Close()
	if _, err := nuget.DecodeRemote(&b); err == nil {
		t.Fatal("inflated metadata limit ignored")
	}
	if _, err := nuget.DecodeRemote(failedRemoteReader{}); err == nil {
		t.Fatal("read fault hidden")
	}
}

type failedRemoteReader struct{}

func (failedRemoteReader) Read([]byte) (int, error) { return 0, errors.New("test read fault") }

var _ io.Reader = failedRemoteReader{}
