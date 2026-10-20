package terraform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errStore = errors.New("test store unavailable")

type failingStore struct {
	store.Store
	fail string
}

func (s failingStore) Packages() store.PackageStore {
	return failingPackages{s.Store.Packages(), s.fail}
}
func (s failingStore) Items() store.ItemStore { return failingItems{s.Store.Items(), s.fail} }
func (s failingStore) Users() store.UserStore { return failingUsers{s.Store.Users(), s.fail} }
func (s failingStore) Settings() store.SettingsStore {
	return failingSettings{s.Store.Settings(), s.fail}
}

func (s failingStore) Permissions() store.PermissionStore {
	return failingPermissions{s.Store.Permissions(), s.fail}
}

type failingPackages struct {
	store.PackageStore
	fail string
}

func (s failingPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if s.fail == "package" {
		return types.Package{}, errStore
	}
	return s.PackageStore.Find(ctx, key, name)
}

func (s failingPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if s.fail == "versions" {
		return nil, errStore
	}
	return s.PackageStore.Versions(ctx, id)
}

func (s failingPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if s.fail == "files" {
		return nil, errStore
	}
	return s.PackageStore.Files(ctx, id)
}

type failingItems struct {
	store.ItemStore
	fail string
}

func (s failingItems) GetByID(ctx context.Context, id int64) (types.Item, error) {
	if s.fail == "item" {
		return types.Item{}, errStore
	}
	if s.fail == "gone" {
		return types.Item{}, store.ErrNotFound
	}
	return s.ItemStore.GetByID(ctx, id)
}

func (s failingItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if s.fail == "download" {
		return types.Item{}, errStore
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

type failingUsers struct {
	store.UserStore
	fail string
}

func (s failingUsers) Get(ctx context.Context, name string) (types.User, error) {
	if s.fail == "user" {
		return types.User{}, errStore
	}
	return s.UserStore.Get(ctx, name)
}

type failingSettings struct {
	store.SettingsStore
	fail string
}

func (s failingSettings) Get(ctx context.Context, key string, into any) error {
	if s.fail == "settings" {
		return errStore
	}
	return s.SettingsStore.Get(ctx, key, into)
}

type failingPermissions struct {
	store.PermissionStore
	fail string
}

func (s failingPermissions) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]types.Permission, error) {
	if s.fail == "permissions" {
		return nil, errStore
	}
	return s.PermissionStore.ForPrincipal(ctx, user, groups)
}

func TestStoreFailures(t *testing.T) {
	for _, failure := range []string{"package", "versions", "files", "item", "gone", "download", "user", "settings", "permissions"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			user := "reader"
			if failure == "settings" {
				user = "anonymous"
			}
			w := f.do("GET", prefix+"v1/modules/mods__ns/name/aws/1.0.0/download", user)
			expect(t, w, 204)
			path := prefix + "v1/modules/mods__ns/name/aws/versions"
			if failure == "download" || failure == "user" || failure == "settings" ||
				failure == "permissions" {
				path = w.Header().Get("X-Terraform-Get")
			}
			f.read = failingStore{f.db, failure}
			f.h = f.router(f.keys)
			status := 500
			if failure == "gone" {
				status = 404
			}
			expect(t, f.do("GET", path, "reader"), status)
		})
	}
}

func TestMissingModulesAndSigningKey(t *testing.T) {
	f := newFixture(t)
	expect(t, f.do("GET", prefix+"v1/modules/mods__ns/absent/aws/versions", "admin"), 404)
	f.h = f.router(nil)
	expect(t, f.do("GET", prefix+"v1/modules/mods__ns/name/aws/1.0.0/download", "admin"), 501)
	expect(t, f.do("GET", prefix+"mods/v1/modules/ns/name/aws/1.0.0.zip?sig=x", ""), 403)
	p := Protocol(Options{})
	if len(p.Host) != 1 {
		t.Fatal(p.Host)
	}
}

func TestSignedTokenValidation(t *testing.T) {
	f := newFixture(t)
	h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
	sign := func(header, payload string) string {
		input := encoding.EncodeToString(
			[]byte(header),
		) + "." + encoding.EncodeToString(
			[]byte(payload),
		)
		m := hmac.New(sha256.New, f.keys.Derive(signingPurpose))
		_, _ = m.Write([]byte(input))
		return input + "." + encoding.EncodeToString(m.Sum(nil))
	}
	for _, raw := range []string{
		"x.y.z", "!.a.b", encoding.EncodeToString([]byte(`{"alg":"none"}`)) + ".a.b",
		encoding.EncodeToString([]byte(`{"alg":"HS256"}`)) + ".a.!",
		sign(`{"alg":"HS256"}`, "not-json"), sign(`{"alg":"HS256"}`, `{}`),
		strings.Repeat("a", 10),
	} {
		if _, ok := h.verify(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}
