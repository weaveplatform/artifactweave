package debianhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/iotest"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// refuseSources is a source transport for tests whose reads must be
// refused before any source is asked.
func refuseSources(t *testing.T) func(*overlayConfig) {
	return withSources(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		t.Error("the source was asked for a path the read should refuse first")
		return source.Result{}, source.ErrUnavailable
	})
}

// TestAccDebianVirtualMetadataRefusals: a virtual repository's metadata is
// refused to a request with a query or a caller who may not read it, and is
// not found when the virtual repository names no members, when no member
// publishes the distribution, and for an InRelease it cannot sign. When the
// virtual repository's own patterns reject the document's path, it answers
// that no repository serves it, once read permission is settled.
func TestAccDebianVirtualMetadataRefusals(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-unpublished", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
		repo.Config{Key: "deb-empty", Class: repo.Virtual},
		repo.Config{
			Key: "deb-bare", Class: repo.Virtual, Repositories: []string{"deb-unpublished"},
		},
		repo.Config{
			Key: "deb-closed", Class: repo.Virtual, Repositories: []string{"deb-local"},
			ExcludesPattern: new("dists/**"),
		},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	for _, tc := range []struct {
		name, key, path, as string
		status              int
		message             string
	}{
		{
			"query", "deb-virtual", debRelease + "?x=1", "admin",
			http.StatusForbidden, "Forbidden",
		},
		{
			"caller who may not read", "deb-virtual", debRelease, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"file outside the composed view that no member holds", "deb-virtual",
			"dists/stable/main/i18n/Translation-en", "admin",
			http.StatusNotFound, "Could not find resource",
		},
		{
			"InRelease without a signing key", "deb-virtual", "dists/stable/InRelease", "admin",
			http.StatusNotFound, "Could not find resource",
		},
		{
			"no members", "deb-empty", debRelease, "admin",
			http.StatusNotFound, "Could not find resource",
		},
		{
			"no member publishes the distribution", "deb-bare", debRelease, "admin",
			http.StatusNotFound, "Could not find resource",
		},
		{
			"Release the virtual repository's patterns reject", "deb-closed", debRelease, "admin",
			http.StatusNotFound,
			"No repository found to serve the request for deb-closed:" + debRelease,
		},
		{
			"Packages the virtual repository's patterns reject", "deb-closed", debPackages,
			"admin", http.StatusNotFound,
			"No repository found to serve the request for deb-closed:" + debPackages,
		},
		{
			"caller who may not read a rejected Release", "deb-closed", debRelease, "user",
			http.StatusForbidden, "Forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, w.get(tc.key, tc.path, tc.as), tc.status, tc.message)
		})
	}
}

// TestAccDebianVirtualKeepsPackagesThePatternsReject: a virtual Packages
// index keeps a package whose file the listing member's patterns, or the
// virtual repository's own, reject, with the listing member's own row, as
// 7.x merges the members' indexes as they are (RTFACT-30830, RTFACT-30656).
// The file's GET passes over the refusing member to the next that holds it,
// answers not found when no member serves it, and answers that no
// repository serves it when the virtual repository's patterns reject it.
func TestAccDebianVirtualKeepsPackagesThePatternsReject(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-hiding", Class: repo.Local, ExcludesPattern: new("pool/alpha*")},
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{
			Key: "deb-virtual", Class: repo.Virtual,
			Repositories: []string{"deb-hiding", "deb-local"},
		},
		repo.Config{
			Key: "deb-only-hiding", Class: repo.Virtual, Repositories: []string{"deb-hiding"},
		},
		repo.Config{
			Key: "deb-picky", Class: repo.Virtual, Repositories: []string{"deb-local"},
			ExcludesPattern: new("pool/beta*"),
		},
	)
	w.publish("deb-hiding", packagesIndex("alpha 1.0+hidden "+debAlpha))
	w.put("deb-hiding", debAlpha, false, []byte("hidden bytes"))
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha, "beta 1.0 "+debBeta))
	w.put("deb-local", debAlpha, false, debBody)
	w.put("deb-local", debBeta, false, debBody)

	rec := w.get("deb-virtual", debPackages, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("virtual Packages answered %d %s, want 200", rec.Code, rec.Body)
	}
	rows, err := debian.DecodePackagesIndex(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("virtual Packages does not decode: %v", err)
	}
	listed := map[string]string{}
	for _, row := range rows {
		listed[row.Filename] = row.Version
	}
	if len(listed) != 2 || listed[debAlpha] != "1.0+hidden" || listed[debBeta] != "1.0" {
		t.Errorf("virtual Packages lists %v, want deb-hiding's alpha row and beta", listed)
	}
	rec = w.get("deb-virtual", debAlpha, "admin")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), debBody) {
		t.Errorf("alpha answered %d %q, want deb-local's package", rec.Code, rec.Body)
	}
	wantError(t, w.get("deb-only-hiding", debAlpha, "admin"),
		http.StatusNotFound, "Could not find resource")

	rec = w.get("deb-picky", debPackages, "admin")
	if got := filenames(t, rec.Body.Bytes()); rec.Code != http.StatusOK ||
		!slices.Equal(got, []string{debAlpha, debBeta}) {
		t.Errorf("deb-picky Packages answered %d listing %v, want both packages", rec.Code, got)
	}
	wantError(t, w.get("deb-picky", debBeta, "admin"), http.StatusNotFound,
		"No repository found to serve the request for deb-picky:"+debBeta)
	wantError(t, w.get("deb-picky", debBeta, "user"), http.StatusForbidden, "Forbidden")
}

// TestUnitDebianVirtualMetadataNeedsADistribution: the virtual metadata
// read answers not found for a path that names no distribution file. The
// overlay passes it only Release files, Packages indexes and by-hash
// copies, so only a direct call reaches this refusal.
func TestUnitDebianVirtualMetadataNeedsADistribution(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(repo.Config{Key: "deb-virtual", Class: repo.Virtual})
	rec := httptest.NewRecorder()
	req := itemkit.ItemRequest{Key: "deb-virtual", Path: "dists/stable"}
	DebianAPI{w.kit}.serveVirtualDebianMetadata(rec, w.request("deb-virtual", req.Path, "admin"),
		w.db, w.debRepo("deb-virtual"), req, kittest.Admin, kittest.AdminAccess())
	wantError(t, rec, http.StatusNotFound, "Could not find resource")
}

// TestAccDebianVirtualMetadataMergesReadableMembers: a virtual Packages
// index merges, in a stable order, the rows of each member that publishes
// the distribution and that the caller may read. A member that is absent,
// blacked out, has a folder for its Release, publishes no Release, or
// declares a Packages file it does not hold is skipped, not an error.
func TestAccDebianVirtualMetadataMergesReadableMembers(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-dark", Class: repo.Local, BlackedOut: new(true)},
		repo.Config{Key: "deb-folder", Class: repo.Local},
		repo.Config{Key: "deb-unpublished", Class: repo.Local},
		repo.Config{Key: "deb-undeclared", Class: repo.Local},
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{
			"absent", "deb-dark", "deb-folder", "deb-unpublished", "deb-undeclared", "deb-local",
		}},
	)
	w.publish("deb-dark", packagesIndex("dark 1.0 pool/dark_1.0_amd64.deb"))
	w.folderAt("deb-folder", debRelease)
	w.put("deb-undeclared", debRelease, false,
		releaseFor(packagesIndex("gone 1.0 pool/gone_1.0_amd64.deb")))
	w.publish("deb-local", packagesIndex(
		"alpha 2.0 pool/alpha_2.0_amd64.deb",
		"alpha 1.0 pool/alpha_1.0_amd64.deb",
		"alpha 1.0 pool/alpha_1.0_all.deb",
	))
	rec := w.get("deb-virtual", debPackages, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("virtual Packages answered %d %s, want 200", rec.Code, rec.Body)
	}
	// Rows sort by name, then version, then filename, so the composed
	// index and its digest are the same whatever order members list them.
	want := []string{
		"pool/alpha_1.0_all.deb", "pool/alpha_1.0_amd64.deb", "pool/alpha_2.0_amd64.deb",
	}
	if got := filenames(t, rec.Body.Bytes()); !slices.Equal(got, want) {
		t.Errorf("virtual Packages lists %v, want %v", got, want)
	}
}

// TestAccDebianVirtualMetadataFailsClosed: a virtual distribution is never
// composed from part of its members, nor left unsigned when a key is
// named. A member that cannot be looked up, is of another type, is itself
// virtual, or whose Release or Packages is unreadable, malformed or
// disagrees with its Release, a remote member with no source, and a
// signing key that is not GPG or unusable each answer 500; a signing key
// that is absent answers 404, as the key's lookup reports it not found.
func TestAccDebianVirtualMetadataFailsClosed(t *testing.T) {
	index := packagesIndex("alpha 1.0 " + debAlpha)
	notAnIndex := []byte("not an index")
	for _, tc := range []struct {
		name    string
		tune    []func(*overlayConfig)
		members []string
		keyPair string
		seed    func(*overlayWorld)
		missing bool
	}{
		{
			name: "member lookup fails", members: []string{"deb-local"},
			tune: []func(*overlayConfig){withStore(func(s serverStore) serverStore {
				return faulty{serverStore: s, repo: "deb-local"}
			})},
		},
		{name: "member of another package type", members: []string{kittest.LocalKey}},
		{name: "virtual member", members: []string{"deb-inner"}},
		{
			name: "member Release does not parse", members: []string{"deb-other"},
			seed: func(w *overlayWorld) { w.put("deb-other", debRelease, false, notAnIndex) },
		},
		{
			name: "member Release unreadable", members: []string{"deb-other"},
			seed: func(w *overlayWorld) { w.lostAt("deb-other", debRelease) },
		},
		{
			name: "member Packages unreadable", members: []string{"deb-other"},
			seed: func(w *overlayWorld) {
				w.put("deb-other", debRelease, false, releaseFor(index))
				w.lostAt("deb-other", debPackages)
			},
		},
		{
			name: "member Packages differs from its Release", members: []string{"deb-other"},
			seed: func(w *overlayWorld) {
				w.put("deb-other", debRelease, false, releaseFor(index))
				w.put("deb-other", debPackages, false, packagesIndex("beta 1.0 "+debBeta))
			},
		},
		{
			name: "member Packages does not parse", members: []string{"deb-other"},
			seed: func(w *overlayWorld) { w.publish("deb-other", notAnIndex) },
		},
		{name: "remote member without a source", members: []string{"deb-remote"}},
		{
			name: "signing key absent", members: []string{"deb-local"}, keyPair: "absent",
			missing: true,
		},
		{name: "signing key not GPG", members: []string{"deb-local"}, keyPair: "deb-rsa"},
		{name: "signing key unusable", members: []string{"deb-local"}, keyPair: "deb-gpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOverlayWorld(t, tc.tune...)
			w.debRepos(
				repo.Config{Key: "deb-local", Class: repo.Local},
				repo.Config{Key: "deb-other", Class: repo.Local},
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{
					Key: "deb-inner", Class: repo.Virtual, Repositories: []string{"deb-local"},
				},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual, Repositories: tc.members,
					KeyPair: tc.keyPair,
				},
			)
			w.publish("deb-local", index)
			for _, pair := range []api.Keypair{
				{Name: "deb-rsa", Type: api.KeypairRSA},
				{Name: "deb-gpg", Type: api.KeypairGPG, PrivateKey: "invalid key"},
			} {
				if err := w.db.Keys().CreateKeypair(t.Context(), pair); err != nil {
					t.Fatal(err)
				}
			}
			if tc.seed != nil {
				tc.seed(w)
			}
			status, message := http.StatusInternalServerError, "internal error"
			if tc.missing {
				status, message = http.StatusNotFound, "Could not find resource"
			}
			wantError(t, w.get("deb-virtual", debRelease, "admin"), status, message)
		})
	}
}

// TestAccDebianVirtualMetadataFromARemoteMember: a remote member's Release
// and Packages come through its source. A Release the upstream lacks
// leaves the member out, one it cannot serve answers its 502, and a stream
// that fails is a server fault; a cached Release is read from the cache. A
// listed package the source refuses is left out of the index, and one it
// cannot check is a server fault.
func TestAccDebianVirtualMetadataFromARemoteMember(t *testing.T) {
	index := packagesIndex("alpha 1.0 "+debAlpha, "beta 1.0 "+debBeta)
	release := streamed(releaseFor(index), nil)
	checkFails := func(err error) func() func(context.Context) error {
		return func() func(context.Context) error { return failsOnCall(2, err) }
	}
	for _, tc := range []struct {
		name     string
		release  func(*overlayWorld) func() (source.Result, error)
		packages func() (source.Result, error)
		status   int
		message  string
		listed   []string
	}{
		{
			name: "Release not upstream",
			release: func(*overlayWorld) func() (source.Result, error) {
				return failing(kernel.ErrNotFound)
			},
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name: "Release unavailable upstream",
			release: func(*overlayWorld) func() (source.Result, error) {
				return failing(source.ErrUnavailable)
			},
			status: http.StatusBadGateway, message: "Bad Gateway",
		},
		{
			name: "Release stream fails",
			release: func(*overlayWorld) func() (source.Result, error) {
				return func() (source.Result, error) {
					return source.Result{Stream: &source.Stream{
						Body: io.NopCloser(iotest.ErrReader(errFault)), Status: http.StatusOK,
						Representation: source.Representation{Size: 1},
					}}, nil
				}
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "Release cached",
			release: func(w *overlayWorld) func() (source.Result, error) {
				return func() (source.Result, error) {
					it := w.put("deb-remote", debRelease, true, releaseFor(index))
					return source.Result{Item: &it}, nil
				}
			},
			packages: streamed(index, nil),
			status:   http.StatusOK, listed: []string{debAlpha, debBeta},
		},
		{
			name:     "listed package refused",
			release:  func(*overlayWorld) func() (source.Result, error) { return release },
			packages: streamed(index, checkFails(source.ErrDenied)),
			status:   http.StatusOK, listed: []string{debBeta},
		},
		{
			name:     "listed package cannot be checked",
			release:  func(*overlayWorld) func() (source.Result, error) { return release },
			packages: streamed(index, checkFails(source.ErrUnavailable)),
			status:   http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *overlayWorld
			w = newOverlayWorld(t, withSources(func(
				ctx context.Context, q source.Request, p source.Permit,
			) (source.Result, error) {
				answers := map[string]func() (source.Result, error){debRelease: tc.release(w)}
				if tc.packages != nil {
					answers[debPackages] = tc.packages
				}
				return upstream(answers)(ctx, q, p)
			}))
			w.debRepos(
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{
					Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-remote"},
				},
			)
			rec := w.get("deb-virtual", debPackages, "admin")
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("virtual Packages answered %d %s, want 200", rec.Code, rec.Body)
			}
			if got := filenames(t, rec.Body.Bytes()); !slices.Equal(got, tc.listed) {
				t.Errorf("virtual Packages lists %v, want %v", got, tc.listed)
			}
		})
	}
}

// TestAccDebianVirtualKeepsARemoteMembersRefusedPackage: a remote member
// whose patterns reject one listed file still lists it in the virtual
// Packages index, its rows checked for read permission alone, while the
// file's GET passes over that member without asking its source.
func TestAccDebianVirtualKeepsARemoteMembersRefusedPackage(t *testing.T) {
	index := packagesIndex("alpha 1.0 "+debAlpha, "beta 1.0 "+debBeta)
	w := newOverlayWorld(t, withSources(upstream(map[string]func() (source.Result, error){
		debRelease:  streamed(releaseFor(index), nil),
		debPackages: streamed(index, nil),
		debBeta: func() (source.Result, error) {
			t.Error("the source was asked for a file its remote's patterns reject")
			return streamed(debBody, nil)()
		},
	})))
	w.debRepos(
		repo.Config{Key: "deb-remote", Class: repo.Remote, ExcludesPattern: new("pool/beta*")},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-remote"}},
	)
	rec := w.get("deb-virtual", debPackages, "admin")
	if got := filenames(t, rec.Body.Bytes()); rec.Code != http.StatusOK ||
		!slices.Equal(got, []string{debAlpha, debBeta}) {
		t.Errorf("virtual Packages answered %d listing %v, want both packages", rec.Code, got)
	}
	wantError(t, w.get("deb-virtual", debBeta, "admin"),
		http.StatusNotFound, "Could not find resource")
}

// TestUnitDebianMemberBytesRefusesAPathTheSourceCannotName: a remote
// member's bytes are not found, without asking the source, at a path its
// Debian source request refuses. The virtual view asks only for Release
// and Packages paths, so only a direct call reaches this refusal.
func TestUnitDebianMemberBytesRefusesAPathTheSourceCannotName(t *testing.T) {
	w := newOverlayWorld(t, refuseSources(t))
	w.debRepos(repo.Config{Key: "deb-remote", Class: repo.Remote})
	_, _, err := DebianAPI{w.kit}.debianMemberBytes(t.Context(), w.db, w.debRepo("deb-remote"),
		"pool/../alpha.deb", kittest.Admin, kittest.AdminAccess())
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("member bytes returned %v, want not found", err)
	}
}

// TestAccDebianVirtualPackage: a virtual repository serves a package from
// the first member, in order, that may serve it and holds it as a file,
// skipping members that are absent, blacked out, hold a folder there or
// whose upstream lacks it. A query or a caller who may not read it is
// refused; a member that cannot be looked up or read, is of another type,
// or is remote on a server without remote sources is a server fault; a
// remote member's upstream fault answers 502, its stream is relayed and its
// revoked cached copy is refused.
func TestAccDebianVirtualPackage(t *testing.T) {
	noSuch := func(*overlayWorld) (source.Result, error) {
		return source.Result{}, kernel.ErrNotFound
	}
	for _, tc := range []struct {
		name    string
		tune    []func(*overlayConfig)
		members []string
		answer  func(*overlayWorld) (source.Result, error)
		target  string
		as      string
		status  int
		message string
	}{
		{
			name: "query", members: []string{"deb-local"}, target: debAlpha + "?x=1",
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name: "caller who may not read", members: []string{"deb-local"}, as: "user",
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name:    "first member that may serve it and holds it",
			members: []string{"absent", "deb-dark", "deb-folder", "deb-local"},
			status:  http.StatusOK,
		},
		{
			name: "member lookup fails", members: []string{"deb-local"},
			tune: []func(*overlayConfig){withStore(func(s serverStore) serverStore {
				return faulty{serverStore: s, repo: "deb-local"}
			})},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "member of another package type", members: []string{kittest.LocalKey},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "member item read fails", members: []string{"deb-local"},
			tune: []func(*overlayConfig){withStore(func(s serverStore) serverStore {
				return faulty{serverStore: s, items: "deb-local"}
			})},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "no member holds it", members: []string{"deb-folder"},
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name: "remote member without a source", members: []string{"deb-remote"},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "remote member's upstream lacks it", members: []string{"deb-remote", "deb-local"},
			answer: noSuch, status: http.StatusOK,
		},
		{
			name: "remote member's upstream unavailable", members: []string{"deb-remote"},
			answer: func(*overlayWorld) (source.Result, error) {
				return source.Result{}, source.ErrUnavailable
			},
			status: http.StatusBadGateway, message: "Bad Gateway",
		},
		{
			name: "remote member streams it", members: []string{"deb-remote"},
			answer: func(*overlayWorld) (source.Result, error) {
				return streamed(debBody, nil)()
			},
			status: http.StatusOK,
		},
		{
			name:    "remote member's cached copy revoked at delivery",
			members: []string{"deb-remote"},
			answer: func(w *overlayWorld) (source.Result, error) {
				it := w.put("deb-remote", debAlpha, true, debBody)
				return source.Result{Item: &it, Revalidate: failsOnCall(2, source.ErrDenied)}, nil
			},
			status: http.StatusForbidden, message: "Forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *overlayWorld
			tune := tc.tune
			if tc.answer != nil {
				tune = append(tune, withSources(upstream(map[string]func() (source.Result, error){
					debAlpha: func() (source.Result, error) { return tc.answer(w) },
				})))
			}
			w = newOverlayWorld(t, tune...)
			w.debRepos(
				repo.Config{Key: "deb-dark", Class: repo.Local, BlackedOut: new(true)},
				repo.Config{Key: "deb-folder", Class: repo.Local},
				repo.Config{Key: "deb-local", Class: repo.Local},
				repo.Config{Key: "deb-remote", Class: repo.Remote},
				repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: tc.members},
			)
			w.put("deb-dark", debAlpha, false, []byte("dark bytes"))
			w.folderAt("deb-folder", debAlpha)
			w.put("deb-local", debAlpha, false, debBody)
			target, as := tc.target, tc.as
			if target == "" {
				target = debAlpha
			}
			if as == "" {
				as = "admin"
			}
			rec := w.get("deb-virtual", target, as)
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), debBody) {
				t.Errorf("answered %d %q, want 200 and the member's package", rec.Code, rec.Body)
			}
		})
	}
}

// TestUnitDebianRemoteVirtualPackageRefusesAPathTheSourceCannotName: the
// remote member read answers not found, without asking the source, for a
// package path its Debian source request refuses. The virtual package
// route passes only valid binary paths, which the request admits by their
// grammar before any other check (#1273), so only a direct call reaches it.
func TestUnitDebianRemoteVirtualPackageRefusesAPathTheSourceCannotName(t *testing.T) {
	w := newOverlayWorld(t, refuseSources(t))
	w.debRepos(repo.Config{Key: "deb-remote", Class: repo.Remote})
	rec := httptest.NewRecorder()
	req := itemkit.ItemRequest{Key: "deb-virtual", Path: "pool/../alpha.deb"}
	if !(DebianAPI{w.kit}).serveRemoteVirtualDebianFile(rec,
		w.request("deb-virtual", "pool/alpha.deb", "admin"), w.debRepo("deb-remote"), req,
		kittest.Admin, kittest.AdminAccess(), "package") {
		t.Error("the read left a refused path to the next member")
	}
	wantError(t, rec, http.StatusNotFound, "Could not find resource")
}

// TestUnitDebianRemoteReadsWithoutRemoteSources: on a server built without
// a remote source service, a virtual package read over a remote member
// answers as a remote index read does, 500 for the missing service, not
// the 502 of an upstream fault. A virtual package request reaches the
// member's read with no index read first, so the route meets the read's
// own refusal, as a direct call does, and never tries the later member
// that holds the package.
func TestUnitDebianRemoteReadsWithoutRemoteSources(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-remote", Class: repo.Remote},
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{
			Key: "deb-virtual", Class: repo.Virtual,
			Repositories: []string{"deb-remote", "deb-local"},
		},
	)
	w.put("deb-local", debAlpha, false, debBody)
	for _, tc := range []struct {
		name  string
		serve func(*testing.T) *httptest.ResponseRecorder
	}{
		{"remote index", func(*testing.T) *httptest.ResponseRecorder {
			return w.get("deb-remote", debPackages, "admin")
		}},
		{"virtual package", func(*testing.T) *httptest.ResponseRecorder {
			return w.get("deb-virtual", debAlpha, "admin")
		}},
		{"remote member package read", func(t *testing.T) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			req := itemkit.ItemRequest{Key: "deb-virtual", Path: debAlpha}
			if !(DebianAPI{w.kit}).serveRemoteVirtualDebianFile(rec,
				w.request("deb-virtual", debAlpha, "admin"), w.debRepo("deb-remote"), req,
				kittest.Admin, kittest.AdminAccess(), "package") {
				t.Error("the read left the missing service to the next member")
			}
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, tc.serve(t), http.StatusInternalServerError, "internal error")
		})
	}
}
