package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const smartQueryResponse = `{"results":[],"range":{"start_pos":0,"end_pos":0,"total":0}}`

func smartQueryRequest(w *smartWorld) source.Request {
	q := w.smartRequest()
	peer, _ := ParseSmartPeer(q.Repo)
	q.Resource = source.Resource{
		Kind:                source.Metadata,
		Path:                ".artifactweave-aql-query",
		PermissionFolder:    true,
		Method:              "GET",
		RequireSmart:        true,
		SmartAQL:            `items.find({"repo":"source"}).limit(10)`,
		ExternalURL:         peer.BaseURL + "/api/search/aql",
		Variant:             "smart-aql-v1",
		ExpectedContentType: "application/json",
		ValidateMetadata: func(_ context.Context, r io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
			b, err := io.ReadAll(r)
			if err != nil || string(b) != smartQueryResponse {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	return q
}

func TestSmartAQLTransientAuthenticatedPOST(t *testing.T) {
	w := smartFixture(t)
	posts := 0
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/artifactory/api/search/aql" {
			return false
		}
		posts++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "text/plain" ||
			string(body) != `items.find({"repo":"source"}).limit(10)` {
			t.Errorf("wrong request %s %v %s", r.Method, r.Header, body)
		}
		_, _ = io.WriteString(rw, smartQueryResponse)
		return true
	}
	q := smartQueryRequest(w)
	for range 2 {
		result, err := w.service.Read(w.ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if result.Item != nil || result.Stream == nil {
			t.Fatalf("persistent result %+v", result)
		}
		body, err := io.ReadAll(result.Stream.Body)
		_ = result.Stream.Body.Close()
		if err != nil || string(body) != smartQueryResponse {
			t.Fatal(string(body), err)
		}
		if err = result.Revalidate(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	entries, _, err := w.db.RemoteCache().List(w.ctx, w.c.Key, "", store.Page{Limit: 100})
	if err != nil || len(entries) != 0 {
		t.Fatal("search persisted remote cache state", entries, err)
	}
	if posts != 2 || w.unauthorized != 0 {
		t.Fatal("query was cached or wrong credentials", posts, w.unauthorized)
	}
	result, err := w.service.Read(w.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	w.c.Password = "rotated"
	w.saveConfig()
	if err = result.Revalidate(w.ctx); err == nil {
		t.Fatal("credential rotation did not fence delivery")
	}
}

func TestSmartAQLAdmissionAndResponseFailures(t *testing.T) {
	for _, fault := range []string{"status", "redirect", "media", "body", "short", "large", "cancel", "permission", "source"} {
		t.Run(fault, func(t *testing.T) {
			w := smartFixture(t)
			q := smartQueryRequest(w)
			ctx, cancel := context.WithCancel(w.ctx)
			defer cancel()
			posts := 0
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/artifactory/api/search/aql" {
					return false
				}
				posts++
				switch fault {
				case "status":
					rw.WriteHeader(503)
				case "redirect":
					http.Redirect(rw, r, "/stolen", 307)
				case "media":
					rw.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(rw, smartQueryResponse)
				case "body":
					_, _ = io.WriteString(rw, "not-json")
				case "short":
					rw.Header().Set("Content-Length", "1000")
					_, _ = io.WriteString(rw, smartQueryResponse)
				case "large":
					_, _ = io.WriteString(rw, strings.Repeat(" ", 8<<20+1))
				case "cancel":
					cancel()
					_, _ = io.WriteString(rw, smartQueryResponse)
				case "permission":
					if err := w.db.Permissions().Delete(t.Context(), "read"); err != nil {
						t.Error(err)
					}
					_, _ = io.WriteString(rw, smartQueryResponse)
				case "source":
					w.c.URL += "-changed"
					w.saveConfig()
					_, _ = io.WriteString(rw, smartQueryResponse)
				}
				return true
			}
			if result, err := w.service.Read(ctx, q); err == nil {
				_ = result.Stream.Body.Close()
				t.Fatal("bad reply accepted", fault)
			}
			if posts != 1 {
				t.Fatal("query retried", posts)
			}
		})
	}
}

func TestSmartAQLNoOutboundForInvalidOrOffline(t *testing.T) {
	for _, fault := range []string{"action", "unbounded", "wrong-repo", "wrong-url", "offline", "cache-only", "denied"} {
		t.Run(fault, func(t *testing.T) {
			w := smartFixture(t)
			q := smartQueryRequest(w)
			switch fault {
			case "action":
				q.Resource.SmartAQL = `items.delete({"repo":"source"}).limit(10)`
			case "unbounded":
				q.Resource.SmartAQL = `items.find({}).limit(10)`
			case "wrong-repo":
				q.Resource.SmartAQL = `items.find({"repo":"another"}).limit(10)`
			case "wrong-url":
				q.Resource.ExternalURL += "?unexpected=1"
			case "offline":
				q.Repo.Offline = new(true)
			case "cache-only":
				q.CacheOnly = true
			case "denied":
				q.Access = nil
			}
			_, err := w.service.Read(w.ctx, q)
			if err == nil {
				t.Fatal("invalid request accepted")
			}
			w.mu.Lock()
			calls := w.calls
			w.mu.Unlock()
			if fault != "wrong-repo" && fault != "wrong-url" && calls != 0 {
				t.Fatal("request escaped preflight", fault, calls)
			}
		})
	}
}

func TestSmartAQLExactPOSTBoundary(t *testing.T) {
	w := smartFixture(t)
	q := smartQueryRequest(w)
	opts, err := w.p.Options(w.ctx, w.c)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{q.Resource.ExternalURL + "?q=1", strings.Replace(q.Resource.ExternalURL, "/api/search/aql", "/api/repositories/source", 1)} {
		if _, err = w.s.request(
			w.ctx,
			q,
			w.permit,
			opts,
			"POST",
			target,
			[]byte(q.Resource.SmartAQL),
		); !errors.Is(
			err,
			ErrRequest,
		) {
			t.Fatal(err)
		}
	}
	q.Resource.SmartAQL = ""
	if _, err = w.s.request(
		w.ctx,
		q,
		w.permit,
		opts,
		"POST",
		smartQueryRequest(w).Resource.ExternalURL,
		[]byte("{}"),
	); !errors.Is(
		err,
		ErrRequest,
	) {
		t.Fatal(err)
	}
	if _, err = w.s.request(
		w.ctx,
		q,
		w.permit,
		opts,
		"DELETE",
		smartQueryRequest(w).Resource.ExternalURL,
		nil,
	); !errors.Is(
		err,
		ErrRequest,
	) {
		t.Fatal(err)
	}
}
