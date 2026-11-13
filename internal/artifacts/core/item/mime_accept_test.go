package item

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitRemoteMIMEClientAccept: a file with no format hint is checked
// against the client's Accept types when it sends Accept, as 7.x's
// HttpRepo.getAcceptableHeaders reads them, and against the path's type
// when it sends none or only */* (#1377). A refusal names the requested
// types in the order 7.x's message lists them, a Java HashSet's.
func TestUnitRemoteMIMEClientAccept(t *testing.T) {
	const html = "<!DOCTYPE html><html>login</html>"
	for _, tc := range []struct {
		name      string
		accept    []string
		requested []string // nil when the file is cached
	}{
		{"no-accept", nil, []string{"application/java-archive"}},
		{"accept-label", []string{"text/html"}, nil},
		{"accept-label-parameters-case", []string{"TEXT/HTML ; q=1"}, nil},
		{"accept-other", []string{"application/json"}, []string{"application/json"}},
		{"wildcard", []string{"*/*"}, []string{"application/java-archive"}},
		{"wildcard-and-label", []string{"text/html;q=0.9, */*"}, nil},
		{"wildcard-and-other", []string{"application/json, */*;q=0.1"}, []string{"application/json"}},
		{"octet-stream", []string{"application/json, application/octet-stream"}, nil},
		{"hash-order", []string{"application/json, application/xml"}, []string{"application/xml", "application/json"}},
		{"hash-order-two-values", []string{"application/json", "application/xml"}, []string{"application/xml", "application/json"}},
		{"same-bucket-insertion-order", []string{"application/pdf, application/json"}, []string{"application/pdf", "application/json"}},
		{"same-bucket-insertion-order-reversed", []string{"application/json, application/pdf"}, []string{"application/json", "application/pdf"}},
		{"repeats", []string{"application/json, application/json", "application/json"}, []string{"application/json"}},
		{"empty-value", []string{""}, []string{""}},
		{"commas-only", []string{","}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Token.Key.Path = "lib/a.jar"
			u.ContentType = "text/html; charset=utf-8"
			u.ClientAccept = tc.accept
			u.Body, u.Length = strings.NewReader(html), int64(len(html))
			var err error
			u.Token, err = w.db.RemoteCache().Begin(t.Context(), u.Token.Key)
			if err != nil {
				t.Fatal(err)
			}
			result, err := w.svc.Cache(t.Context(), u)
			if tc.requested == nil {
				if err != nil {
					t.Fatalf("Accept %q refused: %v", tc.accept, err)
				}
				if result.Body != nil {
					_ = result.Body.Close()
				}
				return
			}
			var refused *MIMERefusal
			if !errors.As(err, &refused) || !errors.Is(err, ErrRemoteMIME) {
				t.Fatalf("Accept %q: %v, want a MIME refusal", tc.accept, err)
			}
			if !slices.Equal(refused.Requested, tc.requested) || refused.Label != "text/html" {
				t.Fatalf(
					"refusal %q of %q, want %q of text/html",
					refused.Requested,
					refused.Label,
					tc.requested,
				)
			}
		})
	}
}

// TestUnitMIMERefusalMessage: the refusal reads as 7.x's
// HttpRepo.blockMismatchingMimeTypes words it, with Java's "[a, b]" list.
func TestUnitMIMERefusalMessage(t *testing.T) {
	refused := &MIMERefusal{
		Requested: []string{"application/xml", "application/json"},
		Label:     "text/html",
	}
	want := "Error fetching http://up/files/lib/a b.jar: Mismatching mime types found in request: " +
		"[application/xml, application/json] and response: text/html, and this repo blocks text/html on mismatch"
	if got := refused.Message("http://up/files/lib/a b.jar"); got != want {
		t.Fatalf("message\n%s\nwant\n%s", got, want)
	}
	empty := (&MIMERefusal{Label: "text/html"}).Message("u")
	if !strings.Contains(empty, "request: [] and") {
		t.Fatalf("no requested types: %s", empty)
	}
}

// TestUnitRefuseLabel: an upstream HEAD's label is held to the same rule
// as a cached file's, and to none with blocking off.
func TestUnitRefuseLabel(t *testing.T) {
	c := repo.Config{Key: "r", Class: repo.Remote}
	var refused *MIMERefusal
	if err := RefuseLabel(c, "a.jar", "text/html", nil); !errors.As(err, &refused) ||
		!slices.Equal(refused.Requested, []string{"application/java-archive"}) {
		t.Fatalf("a.jar labelled text/html: %v", err)
	}
	if err := RefuseLabel(c, "a.jar", "text/html", []string{"text/html"}); err != nil {
		t.Fatalf("Accept text/html: %v", err)
	}
	if err := RefuseLabel(c, "a.jar", "", nil); err != nil {
		t.Fatalf("no label: %v", err)
	}
	off := false
	c.BlockMismatchingMimeTypes = &off
	if err := RefuseLabel(c, "a.jar", "text/html", nil); err != nil {
		t.Fatalf("blocking off: %v", err)
	}
}

// TestUnitMIMERequestsTheURLsType: without Accept, the requested type is
// the type of the URL the upstream was asked for, as 7.x's
// HttpRepo.getAcceptableHeaders reads NamingUtils.getMimeType of its
// download URL, not the path's: a NuGet v2 download's
// packages/<id>/<version> has no type, so its label is not refused, where
// its path's .nupkg would refuse it (#1392). Without a URL it is the path.
func TestUnitMIMERequestsTheURLsType(t *testing.T) {
	const html = "<!DOCTYPE html><html>login</html>"
	for _, tc := range []struct {
		url       string
		requested []string // nil when the file is cached
	}{
		{"http://up.example/packages/Acme/1.0.0", nil},
		{"http://up.example/api/v1/crates/acme/1.0.0/download", nil},
		{"http://up.example/files/acme-1.0.0.zip", []string{"application/zip"}},
		{"", []string{"application/x-nupkg"}},
	} {
		w, u := remoteWorld(t)
		u.Token.Key.Path = "acme/1.0.0/acme.1.0.0.nupkg"
		u.ContentType = "text/html"
		u.RequestURL = tc.url
		u.Body, u.Length = strings.NewReader(html), int64(len(html))
		var err error
		u.Token, err = w.db.RemoteCache().Begin(t.Context(), u.Token.Key)
		if err != nil {
			t.Fatal(err)
		}
		result, err := w.svc.Cache(t.Context(), u)
		if tc.requested == nil {
			if err != nil {
				t.Fatalf("%q refused: %v", tc.url, err)
			}
			if result.Body != nil {
				_ = result.Body.Close()
			}
			continue
		}
		var refused *MIMERefusal
		if !errors.As(err, &refused) || !slices.Equal(refused.Requested, tc.requested) {
			t.Fatalf("%q: %v, want a refusal requesting %q", tc.url, err, tc.requested)
		}
	}
	c := repo.Config{Key: "r", Class: repo.Remote}
	if err := RefuseLabel(c, "http://up.example/packages/Acme/1.0.0", "text/html", nil); err != nil {
		t.Fatalf("HEAD of a URL with no type: %v", err)
	}
}

// TestUnitJavaHashSetOrder: the order follows java.util.HashSet's buckets,
// including the table's doubling past twelve values.
func TestUnitJavaHashSetOrder(t *testing.T) {
	var twelve, thirteen []string
	for c := 'a'; c <= 'm'; c++ {
		thirteen = append(thirteen, "type/"+string(c))
	}
	twelve = thirteen[:12]
	for _, tc := range []struct {
		in, want []string
	}{
		{twelve, strings.Split("type/d type/c type/b type/a type/l type/k type/j type/i type/h type/g type/f type/e", " ")},
		{thirteen, strings.Split("type/d type/c type/b type/a type/m type/l type/k type/j type/i type/h type/g type/f type/e", " ")},
		{[]string{"text/html", "application/xhtml+xml", "application/xml"}, []string{"application/xml", "text/html", "application/xhtml+xml"}},
	} {
		if got := javaHashSetOrder(slices.Clone(tc.in)); !slices.Equal(got, tc.want) {
			t.Fatalf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}
