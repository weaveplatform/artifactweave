package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestAccListingParserBoundsAndUnsupportedDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, body, base string
		bad              bool
	}{
		{"empty", "<h1>Index of /</h1>", "https://up.example/f/", false},
		{"unsupported", "<form>login</form>", "https://up.example/f/", true},
		{"invalid-base", "<h1>Index of /</h1>", "file:///tmp/", true},
		{"too-many", strings.Repeat(`<a href="a">a</a>`, 10001), "https://up.example/f/", true},
		{"too-large", "<h1>Index of /</h1>" + strings.Repeat(" ", listingBytes), "https://up.example/f/", true},
		{"next-path", `<a href="child/" rel="next">next</a>`, "https://up.example/f/", true},
		{"next-foreign", `<a href="https://foreign.example/f/?page=2" rel="next">next</a>`, "https://up.example/f/", true},
		{"next-credentials", `<a href="https://secret@up.example/f/?page=2" rel="next">next</a>`, "https://up.example/f/", true},
		{"next-traversal", `<a href="../f/?page=2" rel="next">next</a>`, "https://up.example/f/", true},
		{"conflicting-next", `<link rel="next" href="?page=2"><link rel="next" href="?page=3">`, "https://up.example/f/", true},
		{"relative-next", `<h1>Index of /</h1><link rel="next" href="?page=2">`, "https://up.example/f/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHTMLListing(t.Context(), strings.NewReader(tc.body), tc.base, "f")
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := parseHTMLListing(
		ctx,
		strings.NewReader(""),
		"https://up.example/",
		"",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if _, err := parseHTMLListing(
		t.Context(),
		failingListingReader{},
		"https://up.example/",
		"",
	); err == nil {
		t.Fatal("read error hidden")
	}
}

type failingListingReader struct{}

func (failingListingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAccSmartListingRejectsMalformedOrWrongFolderDocuments(t *testing.T) {
	for _, body := range []string{
		`{}`, `[]`, `null`, `{"repo":"other","path":"/","children":[]}`,
		`{"repo":"up","path":"/elsewhere","children":[]}`,
		`{"repo":"up","path":"/","children":null}`,
		`{"repo":"up","path":"/","children":[{"uri":"/x"}]}`,
		`{"repo":"up","path":"/","children":[{"uri":"/x","folder":"true"}]}`,
		`{"repo":"up","path":"/","children":[{"uri":3,"folder":true}]}`,
		`{"repo":"up","path":"/","children":[]}{}`,
		`{"repo":"up","path":"/","children":[` + strings.TrimSuffix(strings.Repeat(`{"uri":"/a","folder":false},`, 10001), ",") + `]}`,
	} {
		if _, err := parseSmartListing(t.Context(), strings.NewReader(body), "up", ""); err == nil {
			t.Fatalf("accepted %.80s", body)
		}
	}
	for _, key := range []string{"up", "up-cache"} {
		body := fmt.Sprintf(`{"repo":%q,"path":"/","children":[]}`, key)
		out, err := parseSmartListing(t.Context(), strings.NewReader(body), "up", "")
		if err != nil || len(out.entries) != 0 {
			t.Fatal(out, err)
		}
	}
	unsafe := `{"repo":"up","path":"/","children":[{"uri":"/..","folder":true},{"uri":"/a/b","folder":true},{"uri":"//host","folder":true},{"uri":"relative","folder":false},{"uri":"/a\\b","folder":false},{"uri":"/a\u0000b","folder":false},{"uri":"/","folder":true}]}`
	if out, err := parseSmartListing(
		t.Context(),
		strings.NewReader(unsafe),
		"up",
		"",
	); err != nil ||
		len(out.entries) != 0 {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := parseSmartListing(
		ctx,
		strings.NewReader(`{"repo":"up","path":"/","children":[{"uri":"/a","folder":true}]}`),
		"up",
		"",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if _, err := parseSmartListing(t.Context(), failingListingReader{}, "up", ""); err == nil {
		t.Fatal("reader error hidden")
	}
}
