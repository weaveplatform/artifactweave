package platform

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The helpers that describe an edge's answer and name the readiness probes
// are pure, so these run without a scenario (TestMain).

func TestUnitDescribeNamesWhoAnswered(t *testing.T) {
	header := http.Header{}
	header.Set("X-Ms-Error-Code", "BlobNotFound")
	header.Set("X-Azure-OriginStatusCode", "404")
	header.Set("X-Cache", "TCP_MISS")
	header.Set("X-Azure-Ref", "20261111T100617Z-abc")
	header.Set("Content-Type", "application/xml")
	resp := &http.Response{Status: "404 Not Found", Header: header}
	body := []byte("<?xml version=\"1.0\"?>\n<Error>\n  <Code>BlobNotFound</Code>\n</Error>")

	got := describe(resp, body)
	// answerHeaders' order, whatever order the answer set them in, and only
	// the headers that say who answered.
	want := `404 Not Found [X-Azure-Ref: 20261111T100617Z-abc, X-Cache: TCP_MISS, ` +
		`X-Azure-OriginStatusCode: 404, X-Ms-Error-Code: BlobNotFound] ` +
		`body: "<?xml version=\"1.0\"?> <Error> <Code>BlobNotFound</Code> </Error>"`
	if got != want {
		t.Fatalf("describe:\n got %s\nwant %s", got, want)
	}
}

func TestUnitDescribeWithoutProviderHeadersOrBody(t *testing.T) {
	resp := &http.Response{Status: "502 Bad Gateway", Header: http.Header{}}
	want := "502 Bad Gateway [no provider headers]"
	if got := describe(resp, nil); got != want {
		t.Fatalf("describe(nil body) = %q, want %q", got, want)
	}
	if got := describe(resp, []byte{}); got != want+" body: empty" {
		t.Fatalf("describe(empty body) = %q, want %q", got, want+" body: empty")
	}
}

func TestUnitBodyStart(t *testing.T) {
	long := strings.Repeat("a", answerBodyBytes+100)
	// A two-byte character across the cut: its first byte is dropped too.
	split := strings.Repeat("a", answerBodyBytes-1) + "é" + "tail"
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"empty", []byte{}, "empty"},
		{"whitespace collapsed", []byte("Not\n\t found  here"), `"Not found here"`},
		{"long text cut", []byte(long), `"` + long[:answerBodyBytes] + `" …`},
		{"character split by the cut", []byte(split), `"` + split[:answerBodyBytes-1] + `" …`},
		{"binary", []byte{0xff, 0xfe, 0x00, 0x01}, "not text"},
		{"binary past the limit", append([]byte{0xff}, []byte(long)...), "not text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bodyStart(c.body); got != c.want {
				t.Fatalf("bodyStart = %q, want %q", got, c.want)
			}
		})
	}
}

func TestUnitWithoutURLKeepsTheSignatureOut(t *testing.T) {
	err := error(&url.Error{
		Op:  "Get",
		URL: "https://edge.example/c/sha256/00/blob?sv=2025-01-05&sig=secret",
		Err: io.ErrUnexpectedEOF,
	})
	got := withoutURL(err)
	if !errors.Is(got, io.ErrUnexpectedEOF) || strings.Contains(got.Error(), "sig=") {
		t.Fatalf("withoutURL = %v, want the transport error without the URL", got)
	}
	plain := errors.New("stream error")
	if got := withoutURL(plain); !errors.Is(got, plain) {
		t.Fatalf("withoutURL(%v) = %v, want it unchanged", plain, got)
	}
}

func TestUnitProbeDispositionNamesEachProbe(t *testing.T) {
	seen := map[string]bool{}
	for n := 1; n <= 3; n++ {
		d := probeDisposition(n)
		kind, params, err := mime.ParseMediaType(d)
		if err != nil || kind != "attachment" {
			t.Fatalf("probeDisposition(%d) = %q: %q, %v; want an attachment", n, d, kind, err)
		}
		name := params["filename"]
		if name == "" || seen[name] {
			t.Fatalf("probeDisposition(%d) names %q, want a filename no other probe has", n, name)
		}
		seen[name] = true
	}
}
