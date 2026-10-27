package gitlfs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestUnitRemoteLockPagesRejectUntrustedMetadata(t *testing.T) {
	const valid = `{"locks":[{"id":"one","path":"models/one.bin","locked_at":"2026-10-27T00:00:00Z","owner":{"name":"alice"}}]}`
	for _, tc := range []struct {
		name     string
		body     string
		previous string
		limit    int
		size     int64
	}{
		{name: "wrong size", body: valid, limit: 1, size: int64(len(valid) + 1)},
		{name: "missing locks", body: `{}`, limit: 1},
		{name: "null locks", body: `{"locks":null}`, limit: 1},
		{name: "invalid json", body: `{"locks":[`, limit: 1},
		{name: "too many locks", body: valid, limit: 0},
		{name: "duplicate id", body: `{"locks":[{"id":"one","path":"a","locked_at":"2026-10-27T00:00:00Z"},{"id":"one","path":"b","locked_at":"2026-10-27T00:00:00Z"}]}`, limit: 2},
		{name: "duplicate path", body: `{"locks":[{"id":"one","path":"a","locked_at":"2026-10-27T00:00:00Z"},{"id":"two","path":"a","locked_at":"2026-10-27T00:00:00Z"}]}`, limit: 2},
		{name: "unsafe path", body: `{"locks":[{"id":"one","path":"../secret","locked_at":"2026-10-27T00:00:00Z"}]}`, limit: 1},
		{name: "invalid timestamp", body: `{"locks":[{"id":"one","path":"a","locked_at":"tomorrow"}]}`, limit: 1},
		{name: "repeated cursor", body: `{"locks":[],"next_cursor":"same"}`, previous: "same", limit: 1},
		{name: "empty cursor page", body: `{"locks":[],"next_cursor":"more"}`, limit: 1},
		{name: "overlong cursor", body: `{"locks":[],"next_cursor":"` + strings.Repeat("x", 2049) + `"}`, limit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			if size == 0 {
				size = int64(len(tc.body))
			}
			page, err := parseRemoteLockPage(
				strings.NewReader(tc.body),
				size,
				tc.previous,
				tc.limit,
			)
			if !errors.Is(err, source.ErrInvalidResult) || len(page.Locks) != 0 {
				t.Fatalf("untrusted lock page accepted: %+v, err=%v", page, err)
			}
		})
	}
	page, err := parseRemoteLockPage(strings.NewReader(valid), int64(len(valid)), "", 1)
	if err != nil || len(page.Locks) != 1 || page.Locks[0].Owner.Name != "alice" {
		t.Fatalf("valid lock page rejected: %+v, err=%v", page, err)
	}
}

func TestUnitLockCursorCannotChangeRepositoryOrFilter(t *testing.T) {
	filter := lockFilter{Path: "assets/public.bin"}
	valid := encodeLockCursor(
		lockCursor{Repo: "lfs", Mode: "list", Path: filter.Path, Member: 0, After: "next"},
	)
	if _, err := parseLockCursor(valid, "lfs", "list", filter, 2); err != nil {
		t.Fatalf("valid lock cursor rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		cursor string
		repo   string
		mode   string
		filter lockFilter
	}{
		{name: "wrong repository", cursor: valid, repo: "other", mode: "list", filter: filter},
		{name: "wrong operation", cursor: valid, repo: "lfs", mode: "verify", filter: filter},
		{name: "wrong path", cursor: valid, repo: "lfs", mode: "list", filter: lockFilter{Path: "assets/private.bin"}},
		{name: "invalid base64", cursor: "%", repo: "lfs", mode: "list", filter: filter},
		{name: "oversized cursor", cursor: strings.Repeat("x", 4097), repo: "lfs", mode: "list", filter: filter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseLockCursor(
				tc.cursor,
				tc.repo,
				tc.mode,
				tc.filter,
				2,
			); !errors.Is(
				err,
				store.ErrInvalidCursor,
			) {
				t.Fatalf("unsafe cursor accepted: %v", err)
			}
		})
	}
	for _, malformed := range []lockCursor{
		{Repo: "lfs", Mode: "list", Path: filter.Path, Member: 2},
		{Repo: "lfs", Mode: "list", Path: filter.Path, Member: 0, After: strings.Repeat("x", 2049)},
	} {
		data, err := json.Marshal(malformed)
		if err != nil {
			t.Fatal(err)
		}
		cursor := base64.RawURLEncoding.EncodeToString(data)
		if _, err := parseLockCursor(
			cursor,
			"lfs",
			"list",
			filter,
			2,
		); !errors.Is(
			err,
			store.ErrInvalidCursor,
		) {
			t.Fatalf("malformed member cursor accepted: %+v, err=%v", malformed, err)
		}
	}
}

func TestUnitRemoteLockOriginNeverCrossesAuthority(t *testing.T) {
	for _, raw := range []string{"", "relative/path", "ftp://example.com/lfs", "http://%"} {
		if _, err := lockOriginGuard(raw); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("unsafe origin %q admitted: %v", raw, err)
		}
	}
	guard, err := lockOriginGuard("https://registry.example/lfs")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://elsewhere.example/locks", "http://registry.example/locks",
		"https://alice:secret@registry.example/locks", "https://registry.example/locks#secret",
	} {
		next, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if err := guard(context.Background(), next); !errors.Is(err, source.ErrDenied) {
			t.Fatalf("unsafe lock redirect %q admitted: %v", raw, err)
		}
	}
	next, _ := url.Parse("https://REGISTRY.example/locks")
	if err := guard(context.Background(), next); err != nil {
		t.Fatalf("same-origin lock redirect denied: %v", err)
	}
}
