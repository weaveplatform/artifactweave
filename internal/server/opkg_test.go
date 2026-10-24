package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/opkg"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The index and IPK are published OpenWrt 24.10 content, independent of our
// index encoder and archive fixture builder. See docs/testing/opkg.md.
func opkgUpstreamFixture(t *testing.T) (index, ipk []byte) {
	t.Helper()
	var err error
	index, err = os.ReadFile(filepath.Join("..", "format", "opkg", "testdata", "openwrt-Packages"))
	if err != nil {
		t.Fatal(err)
	}
	ipk, err = os.ReadFile(
		filepath.Join("..", "format", "opkg", "testdata", "464xlat_13_x86_64.ipk"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return index, ipk
}

func TestAccOpkgSourceValidatesIndependentUpstreamContent(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(index); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"feed/Packages", index},
		{"feed/Packages.gz", compressed.Bytes()},
		{"feed/Release", []byte(fmt.Sprintf("SHA256: %x %d Packages\nSHA256: %x %d Packages.gz\n", sha256.Sum256(index), len(index), sha256.Sum256(compressed.Bytes()), compressed.Len()))},
		{"feed/Packages.sig", []byte("-----BEGIN PGP SIGNATURE-----\ntest\n")},
	} {
		t.Run(tc.path, func(t *testing.T) {
			q, err := opkgSourceRequest(source.Request{Resource: source.Resource{Path: tc.path}})
			if err != nil || q.Resource.Kind != source.Metadata || !q.Resource.PermissionFolder ||
				q.Resource.ValidateMetadata == nil {
				t.Fatalf("metadata validation missing: %+v, %v", q.Resource, err)
			}
			validate := q.Resource.ValidateMetadata
			if err := validate(
				t.Context(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
				source.MetadataResponse{},
			); err != nil {
				t.Fatalf("valid upstream metadata rejected: %v", err)
			}
			for _, bad := range []struct {
				name string
				body []byte
				size int64
			}{
				{"zero size", tc.body, 0},
				{"truncated body", tc.body, int64(len(tc.body) + 1)},
				{"corrupt body", []byte("invalid metadata"), int64(len("invalid metadata"))},
			} {
				t.Run(bad.name, func(t *testing.T) {
					var failure *source.HTTPError
					err := validate(
						t.Context(),
						bytes.NewReader(bad.body),
						bad.size,
						source.MetadataResponse{},
					)
					if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
						failure.Kind != source.IntegrityFailure {
						t.Fatalf("unsafe metadata accepted: %v", err)
					}
				})
			}
			q.Resource.Query = "download=1"
			if _, err := opkgSourceRequest(q); !errors.Is(err, source.ErrInvalidRequest) {
				t.Errorf("metadata query accepted: %v", err)
			}
		})
	}
	packagePath := "feed/464xlat_13_x86_64.ipk"
	q, err := opkgSourceRequest(source.Request{Resource: source.Resource{Path: packagePath}})
	if err != nil || q.Resource.Validate == nil || q.Resource.Variant != "opkg-package-v1" {
		t.Fatalf("package validation missing: %+v, %v", q.Resource, err)
	}
	if err := q.Resource.Validate(t.Context(), bytes.NewReader(ipk), int64(len(ipk))); err != nil {
		t.Fatalf("authentic OpenWrt IPK rejected: %v", err)
	}
	corrupt := bytes.Clone(ipk)
	corrupt[len(corrupt)-1] ^= 0xff
	if err := q.Resource.Validate(
		t.Context(),
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
	); err == nil {
		t.Fatal("corrupt IPK accepted")
	}
	q.Resource.Query = "download=1"
	if _, err := opkgSourceRequest(q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Errorf("package query accepted: %v", err)
	}
	if _, err := opkgSourceRequest(
		source.Request{Resource: source.Resource{Path: "../wrong.ipk"}},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("invalid IPK path accepted: %v", err)
	}
	if _, err := opkgSourceRequest(
		source.Request{Resource: source.Resource{Path: "../Packages"}},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("unsafe index path accepted: %v", err)
	}
}

func TestAccOpkgVirtualGraphAndRouteBoundaries(t *testing.T) {
	w := newItemWorld(t, false)
	hiddenFeed := "feed/Packages"
	for _, c := range []repo.Config{
		{Key: "opkg-wrong", Class: repo.Local, PackageType: repo.Maven},
		{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg},
		{Key: "opkg-hidden-local", Class: repo.Local, PackageType: repo.Opkg, ExcludesPattern: &hiddenFeed},
		{Key: "opkg-remote", Class: repo.Remote, PackageType: repo.Opkg, URL: "https://up.example"},
		{Key: "opkg-empty", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{"absent"}},
		{Key: "opkg-hidden", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{"opkg-hidden-local"}},
		{Key: "opkg-cycle", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{"opkg-cycle"}},
		{Key: "opkg-mixed", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{"absent", "opkg-wrong"}},
		{Key: "opkg-offline", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{"opkg-remote"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs}}
	for _, tc := range []struct {
		key  string
		want error
	}{
		{"opkg-empty", nil},
		{"opkg-hidden", nil},
		{"opkg-cycle", errOpkgVirtualCycle},
		{"opkg-mixed", errOpkgMemberType},
		{"opkg-offline", source.ErrUnavailable},
	} {
		c, err := w.db.Repos().Get(t.Context(), tc.key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.opkgCandidates(
			t.Context(),
			w.db,
			c,
			"feed/Packages",
			p,
			acc,
			map[string]bool{},
		)
		if !errors.Is(err, tc.want) || tc.want == nil && len(got) != 0 {
			t.Errorf("%s: candidates=%v error=%v, want %v", tc.key, got, err, tc.want)
		}
	}
	if _, err := svc.opkgCandidates(
		t.Context(),
		w.db,
		repo.Config{Key: "opkg-cycle"},
		"feed/Packages",
		p,
		acc,
		map[string]bool{"opkg-cycle": true},
	); !errors.Is(
		err,
		errOpkgVirtualCycle,
	) {
		t.Errorf("previsited member accepted: %v", err)
	}
	if _, err := svc.opkgMemberIndex(
		t.Context(),
		w.db,
		repo.Config{Key: "opkg-local", Class: repo.Local},
		true,
		"feed/Packages",
		p,
		acc,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("cached local feed accepted: %v", err)
	}
	if _, err := svc.opkgMemberIndex(
		t.Context(),
		w.db,
		repo.Config{Key: "opkg-local", Class: repo.Local},
		false,
		"feed/Packages",
		p,
		acc,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("missing local feed accepted: %v", err)
	}
	if _, err := svc.readOpkgBlob(t.Context(), strings.Repeat("0", 64)); err == nil {
		t.Error("missing metadata blob accepted")
	}
	for _, tc := range []struct {
		url    string
		as     string
		status int
	}{
		{"/artifactory/opkg-empty/feed/Packages", "admin", http.StatusOK},
		{"/artifactory/opkg-empty/feed/Packages.gz", "admin", http.StatusOK},
		{"/artifactory/opkg-empty/feed/Release", "admin", http.StatusOK},
		{"/artifactory/opkg-empty/feed/Packages.sig", "admin", http.StatusNotFound},
		{"/artifactory/opkg-empty/feed/Packages?download=1", "admin", http.StatusNotFound},
		{"/artifactory/opkg-empty/feed/Packages", "user", http.StatusForbidden},
		{"/artifactory/opkg-empty/feed/alpha_1.0_all.ipk", "admin", http.StatusNotFound},
		{"/artifactory/opkg-empty/feed/alpha_1.0_all.ipk?download=1", "admin", http.StatusNotFound},
		{"/artifactory/opkg-empty/feed/alpha_1.0_all.ipk", "user", http.StatusForbidden},
		{"/artifactory/opkg-cycle/feed/Packages", "admin", http.StatusInternalServerError},
		{"/artifactory/opkg-mixed/feed/Packages", "admin", http.StatusInternalServerError},
	} {
		res := w.do(http.MethodGet, tc.url, tc.as, "")
		if res.Code != tc.status {
			t.Errorf("%s as %s: got %d, want %d: %s", tc.url, tc.as, res.Code, tc.status, res.Body)
		}
	}
	allowed, err := opkgPackageReadable(
		t.Context(),
		repo.Config{Class: repo.Local},
		"feed/alpha_1.0_all.ipk",
		acc,
		func(context.Context, string) error { return source.ErrDenied },
	)
	if allowed || err != nil {
		t.Errorf("revoked source read accepted: allowed=%t err=%v", allowed, err)
	}
	allowed, err = opkgPackageReadable(
		t.Context(),
		repo.Config{Class: repo.Local},
		"feed/alpha_1.0_all.ipk",
		acc,
		func(context.Context, string) error { return source.ErrUnavailable },
	)
	if allowed || !errors.Is(err, source.ErrUnavailable) {
		t.Errorf("unavailable source read accepted: allowed=%t err=%v", allowed, err)
	}
}

func TestAccOpkgVirtualFeedSignsOnlyWithConfiguredGPGKey(t *testing.T) {
	w := newItemWorld(t, false)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "opkg-gpg", Type: types.KeypairGPG,
		PrivateKey: key.Private, PublicKey: key.Public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "opkg-signed", Class: repo.Virtual, PackageType: repo.Opkg, KeyPair: "opkg-gpg"},
		{Key: "opkg-no-key", Class: repo.Virtual, PackageType: repo.Opkg},
		{Key: "opkg-missing-key", Class: repo.Virtual, PackageType: repo.Opkg, KeyPair: "absent"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		key, name string
		status    int
	}{
		{"opkg-signed", "Packages", http.StatusOK},
		{"opkg-signed", "Packages.sig", http.StatusOK},
		{"opkg-signed", "Release", http.StatusOK},
		{"opkg-no-key", "Packages.sig", http.StatusNotFound},
		{"opkg-missing-key", "Packages", http.StatusInternalServerError},
		{"opkg-missing-key", "Packages.sig", http.StatusInternalServerError},
	} {
		url := "/artifactory/" + tc.key + "/feed/" + tc.name
		res := w.do(http.MethodGet, url, "admin", "")
		if res.Code != tc.status {
			t.Errorf("%s returned %d, want %d: %s", url, res.Code, tc.status, res.Body)
		}
	}
	packages := w.do(http.MethodGet, "/artifactory/opkg-signed/feed/Packages", "admin", "")
	signature := w.do(http.MethodGet, "/artifactory/opkg-signed/feed/Packages.sig", "admin", "")
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(packages.Body.Bytes()), bytes.NewReader(signature.Body.Bytes()), nil,
	); err != nil {
		t.Fatalf("virtual Packages signature does not bind feed: %v", err)
	}
}

func TestAccOpkgDirectRoutingRejectsUnsafeAndWrongClassRequests(t *testing.T) {
	w := newItemWorld(t, false)
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs}}
	local := repo.Config{Key: "opkg-direct", Class: repo.Local, PackageType: repo.Opkg}
	for _, tc := range []struct {
		path, url string
		class     repo.Class
		handled   bool
		status    int
	}{
		{"feed/Release", "/feed/Release", repo.Local, false, 0},
		{"../unsafe.ipk", "/unsafe.ipk", repo.Local, true, http.StatusNotFound},
		{"feed/alpha_1_all.ipk", "/feed/alpha_1_all.ipk", repo.Local, false, 0},
		{"feed/Packages", "/feed/Packages", repo.Local, true, http.StatusNotFound},
		{"feed/Packages", "/feed/Packages", repo.Virtual, true, http.StatusOK},
	} {
		c := local
		c.Class = tc.class
		r := httptest.NewRequest(http.MethodGet, tc.url, nil)
		response := httptest.NewRecorder()
		got := svc.tryOpkgItem(response, r, w.db, c, false, itemRequest{path: tc.path}, p, acc)
		if got != tc.handled || got && response.Code != tc.status {
			t.Errorf(
				"%s class %s: handled=%t status=%d, want %t/%d",
				tc.path,
				tc.class,
				got,
				response.Code,
				tc.handled,
				tc.status,
			)
		}
	}
	remote := repo.Config{Key: "opkg-direct-remote", Class: repo.Remote, PackageType: repo.Opkg}
	if _, err := svc.opkgMemberIndex(
		t.Context(), w.db, remote, false, "../Packages", p, acc,
	); !errors.Is(err, source.ErrUnavailable) {
		t.Errorf("remote feed without source: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/feed/alpha_1_all.ipk", nil)
	response := httptest.NewRecorder()
	svc.serveOpkgPackage(response, r, w.db, remote, false,
		itemRequest{path: "feed/alpha_1_all.ipk"}, p, acc)
	if response.Code == http.StatusOK {
		t.Error("remote package was delivered without an authoritative feed source")
	}
	localVirtual := repo.Config{
		Key: "opkg-missing-local", Class: repo.Virtual, PackageType: repo.Opkg,
		Repositories: []string{local.Key},
	}
	if err := w.db.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	if candidates, err := svc.opkgCandidates(t.Context(), w.db, localVirtual,
		"feed/Packages", p, acc, map[string]bool{}); err != nil || len(candidates) != 0 {
		t.Errorf("unpublished member index exposed candidates: %v, %v", candidates, err)
	}
}

func TestAccOpkgVirtualLocalPackageRejectsMissingAndDriftedBytes(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t, false)
	member := *repo.Defaults(&repo.Config{Key: "opkg-member", Class: repo.Local, PackageType: repo.Opkg})
	virtual := *repo.Defaults(&repo.Config{Key: "opkg-virtual", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{member.Key}})
	for _, c := range []repo.Config{member, virtual} {
		if err := w.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(path string, body []byte) {
		t.Helper()
		upload, err := w.blobs.BeginUpload(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := upload.Append(t.Context(), bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		info, err := upload.Commit(t.Context(), blob.Digests{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Items().
			Put(t.Context(), types.Item{RepoKey: member.Key, Name: path, Actual: types.Checksums{SHA256: info.Digests.SHA256}, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
	}
	seed("feed/Packages", index)
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs}}
	view, err := svc.opkgMemberIndex(t.Context(), w.db, member, false, "feed/Packages", p, acc)
	if err != nil || len(view.packages) != 2 {
		t.Fatalf(
			"published independent OpenWrt index rejected: entries=%d err=%v",
			len(view.packages),
			err,
		)
	}
	url := "/artifactory/opkg-virtual/feed/464xlat_13_x86_64.ipk"
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code != http.StatusNotFound {
		t.Errorf("missing indexed IPK returned %d", res.Code)
	}
	corrupt := bytes.Clone(ipk)
	corrupt[len(corrupt)-1] ^= 0xff
	seed("feed/464xlat_13_x86_64.ipk", corrupt)
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code != http.StatusNotFound {
		t.Errorf("drifted indexed IPK returned %d", res.Code)
	}
	seed("feed/464xlat_13_x86_64.ipk", ipk)
	if res := w.do(
		http.MethodGet,
		url,
		"admin",
		"",
	); res.Code != http.StatusOK ||
		!bytes.Equal(res.Body.Bytes(), ipk) {
		t.Errorf("genuine indexed IPK rejected: status=%d size=%d", res.Code, res.Body.Len())
	}
	seed("feed/Packages", []byte("invalid index"))
	if _, err := svc.opkgMemberIndex(
		t.Context(),
		w.db,
		member,
		false,
		"feed/Packages",
		p,
		acc,
	); err == nil {
		t.Error("malformed local Packages metadata accepted")
	}
	if _, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: member.Key, Path: "feed", Name: "Packages",
		Actual: types.Checksums{SHA256: strings.Repeat("0", 64)}, Size: int64(len(index)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.opkgMemberIndex(t.Context(), w.db, member, false,
		"feed/Packages", p, acc); err == nil {
		t.Error("local Packages item whose blob vanished was accepted")
	}
}

type opkgReadError struct{}

func (opkgReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type opkgRepeatingReader struct{}

func (opkgRepeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestAccOpkgRemoteIndexRejectsFaultsAndRevokedAuthority(t *testing.T) {
	index, _ := opkgUpstreamFixture(t)
	w := newItemWorld(t, false)
	remote := *repo.Defaults(&repo.Config{Key: "opkg-source", Class: repo.Remote, PackageType: repo.Opkg, URL: "https://up.example"})
	virtual := *repo.Defaults(&repo.Config{Key: "opkg-source-virtual", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{remote.Key}})
	for _, c := range []repo.Config{remote, virtual} {
		if err := w.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	var backendError error
	var backendResult source.Result
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.Path != "feed/Packages" || q.Resource.Kind != source.Metadata {
				t.Errorf("unexpected source request: %+v", q.Resource)
			}
			return backendResult, backendError
		},
	)}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs, RemoteSources: sources}}
	read := func() (opkgIndexView, error) {
		t.Helper()
		return svc.opkgMemberIndex(t.Context(), w.db, remote, false, "feed/Packages", p, acc)
	}
	if _, err := svc.opkgMemberIndex(t.Context(), w.db, remote, false,
		"../Packages", p, acc); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unsafe remote index path was accepted: %v", err)
	}
	stream := func(body io.Reader, size int64) *sourceBody {
		t.Helper()
		tracked := &sourceBody{Reader: body}
		backendResult = source.Result{
			Stream: &source.Stream{
				Body:           tracked,
				Status:         http.StatusOK,
				Representation: source.Representation{Size: size},
			},
		}
		return tracked
	}
	tracked := stream(bytes.NewReader(index), int64(len(index)))
	view, err := read()
	if err != nil || len(view.packages) != 2 || tracked.closes.Load() != 1 {
		t.Fatalf(
			"genuine upstream index rejected: entries=%d err=%v closes=%d",
			len(view.packages),
			err,
			tracked.closes.Load(),
		)
	}
	backendError = source.ErrUnavailable
	if _, err := read(); err == nil {
		t.Error("upstream outage was accepted")
	}
	backendError = nil
	backendResult = source.Result{}
	if _, err := read(); err == nil {
		t.Error("empty source result accepted")
	}
	tracked = stream(bytes.NewReader([]byte("malformed index")), int64(len("malformed index")))
	if _, err := read(); err == nil || tracked.closes.Load() != 1 {
		t.Errorf(
			"malformed source metadata accepted or body leaked: err=%v closes=%d",
			err,
			tracked.closes.Load(),
		)
	}
	tracked = stream(io.LimitReader(opkgRepeatingReader{}, 36<<20+1), 36<<20+1)
	if _, err := read(); !errors.Is(err, errOpkgIndexLarge) || tracked.closes.Load() != 1 {
		t.Errorf(
			"oversized source metadata accepted or body leaked: err=%v closes=%d",
			err,
			tracked.closes.Load(),
		)
	}
	tracked = stream(opkgReadError{}, 100)
	if _, err := read(); err == nil || tracked.closes.Load() != 1 {
		t.Errorf(
			"failed source reader accepted or body leaked: err=%v closes=%d",
			err,
			tracked.closes.Load(),
		)
	}
	backendResult = source.Result{
		Item: &types.Item{
			RepoKey: remote.Key,
			Cached:  true,
			Path:    "feed",
			Name:    "Packages",
			Actual:  types.Checksums{SHA256: strings.Repeat("0", 64)},
		},
	}
	if _, err := read(); err == nil {
		t.Error("cached metadata with a missing blob was accepted")
	}

	used := 0
	backendResult = source.Result{Used: func(context.Context) error { used++; return nil }}
	freshStream := func() {
		backendResult.Stream = &source.Stream{
			Body:           io.NopCloser(bytes.NewReader(index)),
			Status:         http.StatusOK,
			Representation: source.Representation{Size: int64(len(index))},
		}
	}
	request := itemRequest{path: "feed/Packages"}
	serve := func() int {
		t.Helper()
		freshStream()
		r := httptest.NewRequest(http.MethodGet, "/artifactory/opkg-source/feed/Packages", nil)
		response := httptest.NewRecorder()
		svc.serveOpkgIndex(response, r, w.db, remote, false, request, p, acc)
		return response.Code
	}
	if status := serve(); status != http.StatusOK || used != 1 {
		t.Errorf("valid feed was not served/accounted: status=%d used=%d", status, used)
	}
	backendResult.Revalidate = func(context.Context) error { return source.ErrDenied }
	if status := serve(); status == http.StatusOK || used != 1 {
		t.Errorf("revoked source still served/accounted: status=%d used=%d", status, used)
	}
	backendResult.Revalidate = nil
	// The source boundary replaces backend-supplied CheckRead with its own
	// current-path authority check. Revoke only the second indexed package.
	sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if strings.Contains(q.Resource.Path, "6in4") {
			return source.ErrDenied
		}
		return nil
	}
	if status := serve(); status == http.StatusOK || used != 1 {
		t.Errorf("direct feed leaked unreadable package: status=%d used=%d", status, used)
	}
	freshStream()
	candidates, err := svc.opkgCandidates(
		t.Context(),
		w.db,
		virtual,
		"feed/Packages",
		p,
		acc,
		map[string]bool{},
	)
	if err != nil || len(candidates) != 1 || candidates[0].pkg.Name != "464xlat" {
		t.Errorf(
			"virtual feed did not filter unreadable package: candidates=%v err=%v",
			candidates,
			err,
		)
	}
	sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if strings.Contains(q.Resource.Path, "6in4") {
			return source.ErrUnavailable
		}
		return nil
	}
	if status := serve(); status == http.StatusOK || used != 1 {
		t.Errorf("unavailable descendant authority exposed feed: status=%d used=%d", status, used)
	}
	freshStream()
	if _, err := svc.opkgCandidates(
		t.Context(), w.db, virtual, "feed/Packages", p, acc, map[string]bool{},
	); !errors.Is(err, source.ErrUnavailable) {
		t.Errorf("virtual member hid source authority failure: %v", err)
	}
	feedChecks := 0
	sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if q.Resource.Path == "feed/Packages" {
			feedChecks++
			if feedChecks >= 3 {
				return source.ErrDenied
			}
		}
		return nil
	}
	if status := serve(); status == http.StatusOK || used != 1 {
		t.Errorf("authority revoked after fetch exposed feed: status=%d used=%d", status, used)
	}
}

func TestAccOpkgRemotePackagePinsIndependentIndexAndCachedBytes(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	packages, err := opkg.DecodeIndex(index, "feed/Packages")
	if err != nil || len(packages) != 2 {
		t.Fatalf("independent OpenWrt index invalid: %v", err)
	}
	want := packages[0]
	if want.Name != "464xlat" || want.SHA256 != fmt.Sprintf("%x", sha256.Sum256(ipk)) {
		t.Fatal("OpenWrt index does not bind the authentic IPK")
	}
	remote := *repo.Defaults(&repo.Config{Key: "opkg-download", Class: repo.Remote, PackageType: repo.Opkg, URL: "https://up.example"})
	mode := "stream"
	packageReads := 0
	used := 0
	var cached *types.Item
	backend := sourceBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			switch q.Resource.Path {
			case "feed/Packages":
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(index)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(index))},
				}}, nil
			case want.Path:
				packageReads++
				if q.Resource.Checksums.SHA256 != want.SHA256 || q.Resource.Validate == nil {
					t.Errorf(
						"indexed SHA or IPK parser missing from source request: %+v",
						q.Resource,
					)
				}
				if err := q.Resource.Validate(
					ctx,
					bytes.NewReader(ipk),
					int64(len(ipk)),
				); err != nil {
					t.Errorf("authentic IPK failed source validation: %v", err)
				}
				switch mode {
				case "stream":
					return source.Result{Stream: &source.Stream{
						Body: io.NopCloser(bytes.NewReader(ipk)), Status: http.StatusOK,
						Representation: source.Representation{Size: int64(len(ipk))},
					}, Used: func(context.Context) error { used++; return nil }}, nil
				case "cached":
					return source.Result{
						Item: cached,
						Used: func(context.Context) error { used++; return nil },
					}, nil
				case "outage":
					return source.Result{}, source.ErrUnavailable
				case "revoked":
					return source.Result{Stream: &source.Stream{
						Body: io.NopCloser(bytes.NewReader(ipk)), Status: http.StatusOK,
						Representation: source.Representation{Size: int64(len(ipk))},
					}, Revalidate: func(context.Context) error { return source.ErrDenied }}, nil
				}
			}
			t.Errorf("unexpected source path %q", q.Resource.Path)
			return source.Result{}, source.ErrInvalidRequest
		},
	)
	sources := &source.Service{Resolve: sourceIdentity, HTTP: backend}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	if err := w.db.Repos().Create(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	url := "/artifactory/" + remote.Key + "/" + want.Path
	if res := w.do(
		http.MethodGet,
		url,
		"admin",
		"",
	); res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), ipk) ||
		used != 1 {
		t.Fatalf(
			"authentic indexed upstream IPK not delivered: status=%d size=%d used=%d",
			res.Code,
			res.Body.Len(),
			used,
		)
	}
	if packageReads != 1 {
		t.Fatalf("package read count %d, want 1", packageReads)
	}
	if res := w.do(
		http.MethodGet,
		url+"?download=1",
		"admin",
		"",
	); res.Code != http.StatusNotFound {
		t.Errorf("package query was accepted: %d", res.Code)
	}
	if res := w.do(http.MethodGet, url, "user", ""); res.Code != http.StatusForbidden {
		t.Errorf("unreadable remote IPK was exposed: %d", res.Code)
	}
	mode = "outage"
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code == http.StatusOK || used != 1 {
		t.Errorf("upstream outage published success: status=%d used=%d", res.Code, used)
	}
	mode = "revoked"
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code == http.StatusOK || used != 1 {
		t.Errorf("revoked upstream authority published success: status=%d used=%d", res.Code, used)
	}
	mode = "cached"
	cached = &types.Item{
		RepoKey: remote.Key,
		Cached:  true,
		Path:    "feed",
		Name:    filepath.Base(want.Path),
		Size:    want.Size,
		Actual:  types.Checksums{SHA256: strings.Repeat("0", 64)},
	}
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code == http.StatusOK || used != 1 {
		t.Errorf("wrong cached SHA published success: status=%d used=%d", res.Code, used)
	}
	cached.Actual.SHA256 = want.SHA256
	cached.Size--
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code == http.StatusOK || used != 1 {
		t.Errorf("wrong cached size published success: status=%d used=%d", res.Code, used)
	}
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs, RemoteSources: sources}}
	r := httptest.NewRequest(http.MethodGet, "/artifactory/opkg-download/../bad.ipk", nil)
	response := httptest.NewRecorder()
	svc.serveRemoteOpkgPackage(
		response,
		r,
		remote,
		false,
		itemRequest{path: "../bad.ipk"},
		want,
		p,
		acc,
	)
	if response.Code == http.StatusOK {
		t.Fatal("unsafe direct package path was delivered")
	}
}
