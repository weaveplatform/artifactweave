package npmhttp

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type virtualSeekBody struct{ *bytes.Reader }

func (virtualSeekBody) Close() error { return nil }

func TestUnitNPMVirtualWinningDigest(t *testing.T) {
	body := []byte("immutable winning bytes")
	sum := sha512.Sum384(body)
	integrity := "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
	h := handler{}
	r := httptest.NewRequest("GET", "/", nil)
	for _, tc := range []struct {
		name, manifest, member string
		body                   io.ReadCloser
		sums                   kernel.Checksums
		want, fail             bool
	}{
		{name: "no digest requires winning owner", manifest: `{"dist":{}}`, member: "owner", want: true},
		{name: "no digest refuses another member", manifest: `{"dist":{}}`, member: "other"},
		{name: "normalized checksum", manifest: `{"dist":{"shasum":"  ABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCD  "}}`, sums: kernel.Checksums{SHA1: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}, want: true},
		{name: "blank checksum keeps winning owner", manifest: `{"dist":{"shasum":"  "}}`, member: "owner", want: true},
		{name: "blank checksum does not verify a duplicate", manifest: `{"dist":{"shasum":"  "}}`, member: "other"},
		{name: "different checksum", manifest: `{"dist":{"shasum":"abcdefabcdefabcdefabcdefabcdefabcdefabcd"}}`, sums: kernel.Checksums{SHA1: strings.Repeat("0", 40)}},
		{name: "invalid cached manifest", manifest: `[`, fail: true},
		{name: "invalid shasum", manifest: `{"dist":{"shasum":"nothex"}}`, fail: true},
		{name: "invalid integrity", manifest: `{"dist":{"integrity":"sha512-invalid"}}`, fail: true},
		{name: "sha384 seekable transient", manifest: `{"dist":{"integrity":"` + integrity + `"}}`, body: virtualSeekBody{bytes.NewReader(body)}, want: true},
		{name: "sha384 different transient", manifest: `{"dist":{"integrity":"` + integrity + `"}}`, body: virtualSeekBody{bytes.NewReader([]byte("wrong"))}},
		{name: "sha384 unseekable", manifest: `{"dist":{"integrity":"` + integrity + `"}}`, body: io.NopCloser(bytes.NewBuffer(body)), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := npmfmt.VirtualVersion{Member: "owner", Manifest: json.RawMessage(tc.manifest)}
			result := source.Result{Stream: &source.Stream{Body: tc.body, Checksums: tc.sums}}
			got, err := h.matchesVirtual(r, &expected, tc.member, result)
			if got != tc.want || (err != nil) != tc.fail {
				t.Fatal(got, err)
			}
			if seek, ok := tc.body.(io.Seeker); ok {
				at, err := seek.Seek(0, io.SeekCurrent)
				if err != nil || at != 0 {
					t.Fatal("stream was consumed", at, err)
				}
			}
		})
	}
	if ok, err := h.matchesVirtual(r, nil, "any", source.Result{}); !ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestUnitNPMVirtualRenderingFaults(t *testing.T) {
	for _, manifest := range []string{`[`, `{"name":"pkg"}`, `{"dist":[]}`} {
		if _, err := virtualVersion("pkg", "1.0.0", npmfmt.VirtualVersion{Manifest: json.RawMessage(manifest)}, "/base", false); err == nil {
			t.Fatal(manifest)
		}
		if _, err := renderVirtual(npmfmt.VirtualPackage{Versions: map[string]npmfmt.VirtualVersion{"1.0.0": {Manifest: json.RawMessage(manifest)}}}, "/base", false); err == nil {
			t.Fatal(manifest)
		}
	}
	for _, doc := range []remoteDocument{{}, {"versions": json.RawMessage(`{}`), "dist-tags": json.RawMessage(`[]`)}, {"versions": json.RawMessage(`{"1.0.0":{"dist":[]}}`), "dist-tags": json.RawMessage(`{}`)}} {
		if _, err := virtualDocument("pkg", doc); err == nil {
			t.Fatal(doc)
		}
	}
}

func TestUnitNPMVirtualPassOverBoundary(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&source.HTTPError{Status: 401}, true},
		{&source.HTTPError{Status: 403}, true},
		{&apierror.Error{Status: 403, NoChallenge: true}, true},
		{apierror.New(403, "caller refusal"), false},
		{fmt.Errorf("copy failure: %w: %w", format.ErrStorageFault, &source.HTTPError{Status: 403}), false},
		{fmt.Errorf("storage failure: %w", format.ErrStorageFault), false},
	} {
		if got := virtualPassedOver(tc.err); got != tc.want {
			t.Fatal(tc.err, got)
		}
	}
}
