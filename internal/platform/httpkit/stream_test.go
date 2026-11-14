package httpkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// item is a list element with the fields the collected answers have: an
// omitted pointer, a string json.Encoder escapes and a number.
type item struct {
	Name  string  `json:"name"`
	URI   *string `json:"uri,omitempty"`
	Order int     `json:"order"`
}

// envelope is a collected document whose list sits between two members.
type envelope struct {
	After  string  `json:"after"`
	Items  *[]item `json:"items,omitempty"`
	Before string  `json:"before"`
}

// rows is a list of n rows, read size at a time by its PageFunc; fail, when
// set, fails the read of that page (one-based).
type rows struct {
	n, fail int
	reads   int
}

func (r *rows) page(_ context.Context, p kernel.Page) ([]int, string, error) {
	r.reads++
	if r.reads == r.fail {
		return nil, "", errBoom
	}
	from := 0
	if p.After != "" {
		parts, _ := p.Key(1)
		from, _ = strconv.Atoi(parts[0])
	}
	var out []int
	for i := from; i < r.n && len(out) < p.Limit+1; i++ {
		out = append(out, i)
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(i int) []string { return []string{strconv.Itoa(i + 1)} },
	)
	return page, next, nil
}

var errBoom = errors.New("boom")

// render keeps the rows keep accepts, as a caller's visibility filter.
func render(keep func(int) bool) func(int) (item, bool, error) {
	return func(i int) (item, bool, error) {
		uri := "/a<b>&" + strconv.Itoa(i)
		v := item{Name: "n" + strconv.Itoa(i), Order: i}
		if i%2 == 0 {
			v.URI = &uri
		}
		return v, keep(i), nil
	}
}

// collected is the answer main's handlers wrote for the same list:
// json.Encoder over the whole document, in one write.
func collected(t *testing.T, doc any) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func items(n int, keep func(int) bool) []item {
	out := []item{}
	r := render(keep)
	for i := range n {
		if v, ok, _ := r(i); ok {
			out = append(out, v)
		}
	}
	return out
}

func all(int) bool { return true }

func stream(
	t *testing.T,
	src *rows,
	limit int,
	keep func(int) bool,
	opts httpkit.ListOptions,
) *httpkit.ListStream {
	t.Helper()
	l, err := httpkit.StreamList(
		context.Background(),
		kernel.NewPages(src.page, limit),
		render(keep),
		opts,
	)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestUnitStreamListBytesEqualTheCollectedAnswer: the streamed body is the
// collected body byte for byte, as an array and inside an envelope, empty,
// filtered and over several pages.
func TestUnitStreamListBytesEqualTheCollectedAnswer(t *testing.T) {
	odd := func(i int) bool { return i%2 == 1 }
	env, err := httpkit.EnvelopeOf(envelope{After: "z<", Items: &[]item{}, Before: "a"}, "items")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		n, limit int
		keep     func(int) bool
	}{
		{"empty", 0, 3, all},
		{"one", 1, 3, all},
		{"one page", 3, 3, all},
		{"several pages", 10, 3, all},
		{"filtered", 10, 3, odd},
		{"filtered to nothing", 10, 3, func(int) bool { return false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			l := stream(t, &rows{n: tc.n}, tc.limit, tc.keep, httpkit.ListOptions{})
			if err := l.Write(rec); err != nil {
				t.Fatal(err)
			}
			want := collected(t, items(tc.n, tc.keep))
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Fatalf("array:\n got %s\nwant %s", rec.Body, want)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type %q", ct)
			}
			rec = httptest.NewRecorder()
			l = stream(t, &rows{n: tc.n}, tc.limit, tc.keep,
				httpkit.ListOptions{Envelope: env, ContentType: "application/vnd.x+json"})
			_ = l.Write(rec)
			list := items(tc.n, tc.keep)
			want = collected(t, envelope{After: "z<", Items: &list, Before: "a"})
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Fatalf("envelope:\n got %s\nwant %s", rec.Body, want)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.x+json" {
				t.Fatalf("Content-Type %q", ct)
			}
		})
	}
}

// TestUnitStreamListFramesAsTheCollectedAnswer: through a real server, an
// answer that fits net/http's buffer keeps its Content-Length and a larger
// one is chunked, exactly as the collected answer is framed.
func TestUnitStreamListFramesAsTheCollectedAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		chunked bool
	}{{"small", 5, false}, {"large", 400, true}} {
		t.Run(tc.name, func(t *testing.T) {
			list := items(tc.n, all)
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/collected" {
						body := collected(t, list)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(body)
						return
					}
					l, err := httpkit.StreamList(
						r.Context(),
						kernel.NewPages((&rows{n: tc.n}).page, 7),
						render(all),
						httpkit.ListOptions{},
					)
					if err != nil {
						t.Error(err)
						return
					}
					_ = l.Write(w)
				}),
			)
			defer srv.Close()
			get := func(path string) (*http.Response, []byte) {
				res, err := srv.Client().Get(srv.URL + path) //nolint:noctx // a test server
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = res.Body.Close() }()
				body, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				return res, body
			}
			got, gotBody := get("/streamed")
			want, wantBody := get("/collected")
			if !bytes.Equal(gotBody, wantBody) {
				t.Fatalf("body:\n got %s\nwant %s", gotBody, wantBody)
			}
			chunked := len(got.TransferEncoding) > 0
			if chunked != tc.chunked || got.ContentLength != want.ContentLength ||
				strings.Join(
					got.TransferEncoding,
					",",
				) != strings.Join(
					want.TransferEncoding,
					",",
				) {
				t.Fatalf("framing: streamed %v %d, collected %v %d", got.TransferEncoding,
					got.ContentLength, want.TransferEncoding, want.ContentLength)
			}
		})
	}
}

// TestUnitStreamListReadsAheadBeforeWriting: StreamList reads past pages
// render keeps nothing of, so Empty and Seen tell an empty list from a
// hidden one, and a failure in any of those reads is its error.
func TestUnitStreamListReadsAheadBeforeWriting(t *testing.T) {
	hidden := func(i int) bool { return i >= 7 }
	src := &rows{n: 10}
	l := stream(t, src, 2, hidden, httpkit.ListOptions{})
	if l.Empty() || !l.Seen() || src.reads != 4 {
		t.Fatalf("empty %v seen %v after %d reads, want a kept element on the fourth", l.Empty(),
			l.Seen(), src.reads)
	}
	l = stream(t, &rows{n: 10}, 2, func(int) bool { return false }, httpkit.ListOptions{})
	if !l.Empty() || !l.Seen() {
		t.Fatalf("hidden list: empty %v seen %v", l.Empty(), l.Seen())
	}
	l = stream(t, &rows{n: 0}, 2, all, httpkit.ListOptions{})
	if !l.Empty() || l.Seen() {
		t.Fatalf("empty list: empty %v seen %v", l.Empty(), l.Seen())
	}
	for _, fail := range []int{1, 3} {
		_, err := httpkit.StreamList(
			context.Background(),
			kernel.NewPages(
				(&rows{n: 10, fail: fail}).page,
				2,
			),
			render(hidden),
			httpkit.ListOptions{},
		)
		if !errors.Is(err, errBoom) {
			t.Fatalf("failed read %d before the first byte: %v", fail, err)
		}
	}
	_, err := httpkit.StreamList(context.Background(), kernel.NewPages((&rows{n: 3}).page, 2),
		func(int) (item, bool, error) { return item{}, false, errBoom }, httpkit.ListOptions{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("failed render: %v", err)
	}
	_, err = httpkit.StreamList(context.Background(), kernel.NewPages((&rows{n: 3}).page, 2),
		func(int) (any, bool, error) { return make(chan int), true, nil }, httpkit.ListOptions{})
	if err == nil {
		t.Fatal("an element json cannot encode was accepted")
	}
}

// abortWrite writes l, which must abort, and returns what it wrote.
func abortWrite(t *testing.T, l *httpkit.ListStream) (body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != http.ErrAbortHandler { //nolint:errorlint // the very value net/http compares
			t.Fatalf("recovered %v, want http.ErrAbortHandler", r)
		}
		body = rec.Body.String()
	}()
	_ = l.Write(rec)
	t.Fatal("the stream finished")
	return ""
}

// TestUnitStreamListAbortsAfterTheFirstByte: a failed read, a failed render
// or an expired deadline after the status is out aborts the handler; the
// body never closes, so no JSON parser accepts it, and the cause is noted.
func TestUnitStreamListAbortsAfterTheFirstByte(t *testing.T) {
	var cause error
	ctx := httpkit.WithCause(context.Background(), &cause)
	l, err := httpkit.StreamList(ctx, kernel.NewPages((&rows{n: 10, fail: 2}).page, 3), render(all),
		httpkit.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body := abortWrite(t, l)
	var v any
	if json.Unmarshal([]byte(body), &v) == nil || !strings.HasPrefix(body, "[") ||
		!errors.Is(cause, errBoom) {
		t.Fatalf("failed read: body %q, cause %v", body, cause)
	}
	calls := 0
	l, _ = httpkit.StreamList(context.Background(), kernel.NewPages((&rows{n: 10}).page, 3),
		func(i int) (item, bool, error) {
			calls++
			if calls > 3 {
				return item{}, false, errBoom
			}
			return item{Order: i}, true, nil
		}, httpkit.ListOptions{})
	if body := abortWrite(t, l); strings.HasSuffix(body, "]\n") {
		t.Fatalf("failed render closed the list: %q", body)
	}
	expiring, cancel := context.WithCancel(context.Background())
	l, _ = httpkit.StreamList(expiring, kernel.NewPages((&rows{n: 10}).page, 3), render(all),
		httpkit.ListOptions{})
	cancel()
	if body := abortWrite(t, l); strings.HasSuffix(body, "]\n") {
		t.Fatalf("expired deadline closed the list: %q", body)
	}
}

// failingWriter is a client that has gone away.
type failingWriter struct {
	http.ResponseWriter
}

func (failingWriter) Write([]byte) (int, error) { return 0, errBoom }

// TestUnitStreamListStopsWhenTheClientGoes: a failed write ends the answer
// without reading the rest of the list.
func TestUnitStreamListStopsWhenTheClientGoes(t *testing.T) {
	src := &rows{n: 10}
	l := stream(t, src, 3, all, httpkit.ListOptions{})
	if err := l.Write(failingWriter{httptest.NewRecorder()}); err != nil || src.reads != 1 {
		t.Fatalf("write error: %v after %d reads", err, src.reads)
	}
}

// TestUnitEnvelopeOf: the document must be an object holding the member as
// an empty array.
func TestUnitEnvelopeOf(t *testing.T) {
	for name, tc := range map[string]struct {
		doc any
		key string
	}{
		"not json":       {make(chan int), "items"},
		"not an object":  {[]int{}, "items"},
		"no such member": {envelope{Items: &[]item{}}, "other"},
		"omitted member": {envelope{}, "items"},
		"non-empty":      {envelope{Items: &[]item{{}}}, "items"},
		"not an array":   {map[string]any{"items": "x"}, "items"},
		"invalid raw":    {json.RawMessage(`{"a":`), "items"},
	} {
		if _, err := httpkit.EnvelopeOf(tc.doc, tc.key); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
