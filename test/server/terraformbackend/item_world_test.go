package terraformbackend_test

import (
	"crypto/sha1" //nolint:gosec // SHA-1 is a wire checksum
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

func newItemWorld(t *testing.T, _ bool, tune ...func(*Config)) *itemWorld {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
	} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		BaseURL:       "https://reg.example",
		CompatVersion: "7.155.0",
		Store:         db,
		Blobs:         blobs,
		Auth:          a,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return &itemWorld{t: t, h: server.New(cfg), db: db, blobs: cfg.Blobs}
}

func (w *itemWorld) do(method, path, as, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	var headers map[string]string
	if as != "" {
		headers = basic(as, "pw")
	}
	return do(w.t, w.h, method, path, headers, body)
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
