package opkghttp

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
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// The index and IPK are published OpenWrt 24.10 content, independent of our
// index encoder and archive fixture builder. See docs/testing/opkg.md.
func opkgUpstreamFixture(t *testing.T) (index, ipk []byte) {
	t.Helper()
	index = repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/openwrt-Packages")
	ipk = repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/464xlat_13_x86_64.ipk")
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
			q, err := itemkit.OpkgSourceRequest(
				source.Request{Resource: source.Resource{Path: tc.path}},
			)
			if err != nil || q.Resource.Kind != source.ResourceKindMetadata ||
				!q.Resource.PermissionFolder ||
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
						failure.Kind != source.FailureKindIntegrity {
						t.Fatalf("unsafe metadata accepted: %v", err)
					}
				})
			}
			q.Resource.Query = "download=1"
			if _, err := itemkit.OpkgSourceRequest(q); !errors.Is(err, source.ErrInvalidRequest) {
				t.Errorf("metadata query accepted: %v", err)
			}
		})
	}
	packagePath := "feed/464xlat_13_x86_64.ipk"
	q, err := itemkit.OpkgSourceRequest(
		source.Request{Resource: source.Resource{Path: packagePath}},
	)
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
	if _, err := itemkit.OpkgSourceRequest(q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Errorf("package query accepted: %v", err)
	}
	if _, err := itemkit.OpkgSourceRequest(
		source.Request{Resource: source.Resource{Path: "../wrong.ipk"}},
	); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Errorf("invalid IPK path accepted: %v", err)
	}
	if _, err := itemkit.OpkgSourceRequest(
		source.Request{Resource: source.Resource{Path: "../Packages"}},
	); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Errorf("unsafe index path accepted: %v", err)
	}
}

func TestAccOpkgVirtualGraphAndRouteBoundaries(t *testing.T) {
	w := newItemWorld(t)
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
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(t, w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := opkgItems{newKit(Config{Store: w.db, Blobs: w.blobs})}
	for _, tc := range []struct {
		key      string
		expected error
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
			false,
			map[string]bool{},
		)
		if !errors.Is(err, tc.expected) || tc.expected == nil && len(got) != 0 {
			t.Errorf("%s: candidates=%v error=%v, want %v", tc.key, got, err, tc.expected)
		}
	}
	if _, err := svc.opkgCandidates(
		t.Context(),
		w.db,
		repo.Config{Key: "opkg-cycle"},
		"feed/Packages",
		p,
		acc,
		false,
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
		kernel.ErrNotFound,
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
		kernel.ErrNotFound,
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

func TestAccOpkgDirectRoutingRejectsUnsafeAndWrongClassRequests(t *testing.T) {
	w := newItemWorld(t)
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(t, w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := opkgItems{newKit(Config{Store: w.db, Blobs: w.blobs})}
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
		got := TryOpkgItem(
			svc.Kit,
			response,
			r,
			w.db,
			c,
			false,
			itemkit.ItemRequest{Path: tc.path},
			p,
			acc,
		)
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
		itemkit.ItemRequest{Path: "feed/alpha_1_all.ipk"}, p, acc)
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
		"feed/Packages", p, acc, false, map[string]bool{}); err != nil || len(candidates) != 0 {
		t.Errorf("unpublished member index exposed candidates: %v, %v", candidates, err)
	}
}

func TestAccOpkgVirtualLocalPackageRejectsMissingAndDriftedBytes(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t)
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
			Put(t.Context(), model.Item{RepoKey: member.Key, Name: path, Actual: kernel.Checksums{SHA256: info.Digests.SHA256}, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
	}
	seed("feed/Packages", index)
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(t, w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := opkgItems{newKit(Config{Store: w.db, Blobs: w.blobs})}
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
	if _, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: member.Key, Path: "feed", Name: "Packages",
		Actual: kernel.Checksums{SHA256: strings.Repeat("0", 64)}, Size: int64(len(index)),
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
	w := newItemWorld(t)
	remote := *repo.Defaults(&repo.Config{Key: "opkg-source", Class: repo.Remote, PackageType: repo.Opkg, URL: "https://up.example"})
	virtual := *repo.Defaults(&repo.Config{Key: "opkg-source-virtual", Class: repo.Virtual, PackageType: repo.Opkg, Repositories: []string{remote.Key}})
	for _, c := range []repo.Config{remote, virtual} {
		if err := w.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(t, w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	var backendError error
	var backendResult source.Result
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.Path != "feed/Packages" ||
				q.Resource.Kind != source.ResourceKindMetadata {
				t.Errorf("unexpected source request: %+v", q.Resource)
			}
			return backendResult, backendError
		},
	)}
	svc := opkgItems{newKit(Config{Store: w.db, Blobs: w.blobs, RemoteSources: sources})}
	read := func() (opkgIndexView, error) {
		t.Helper()
		return svc.opkgMemberIndex(t.Context(), w.db, remote, false, "feed/Packages", p, acc)
	}
	if _, err := svc.opkgMemberIndex(t.Context(), w.db, remote, false,
		"../Packages", p, acc); !errors.Is(err, kernel.ErrNotFound) {
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
		Item: &model.Item{
			RepoKey: remote.Key,
			Cached:  true,
			Path:    "feed",
			Name:    "Packages",
			Actual:  kernel.Checksums{SHA256: strings.Repeat("0", 64)},
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
	request := itemkit.ItemRequest{Path: "feed/Packages"}
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
		false,
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
		t.Context(), w.db, virtual, "feed/Packages", p, acc, false, map[string]bool{},
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
	var cached *model.Item
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
	w := newItemWorld(t, func(c *Config) { c.RemoteSources = sources })
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
	cached = &model.Item{
		RepoKey: remote.Key,
		Cached:  true,
		Path:    "feed",
		Name:    filepath.Base(want.Path),
		Size:    want.Size,
		Actual:  kernel.Checksums{SHA256: strings.Repeat("0", 64)},
	}
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code == http.StatusOK || used != 1 {
		t.Errorf("wrong cached SHA published success: status=%d used=%d", res.Code, used)
	}
	cached.Actual.SHA256 = want.SHA256
	cached.Size--
	if res := w.do(http.MethodGet, url, "admin", ""); res.Code == http.StatusOK || used != 1 {
		t.Errorf("wrong cached size published success: status=%d used=%d", res.Code, used)
	}
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(t, w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := opkgItems{newKit(Config{Store: w.db, Blobs: w.blobs, RemoteSources: sources})}
	r := httptest.NewRequest(http.MethodGet, "/artifactory/opkg-download/../bad.ipk", nil)
	response := httptest.NewRecorder()
	svc.serveRemoteOpkgPackage(
		response,
		r,
		remote,
		false,
		itemkit.ItemRequest{Path: "../bad.ipk"},
		want,
		p,
		acc,
	)
	if response.Code == http.StatusOK {
		t.Fatal("unsafe direct package path was delivered")
	}
}

// TestUnitOpkgRemoteReadsWithoutRemoteSources: on a server built without a
// remote source service, a remote IPK read answers as a remote feed read
// does, 500 for the missing service, rather than calling through it. An
// IPK request, to the remote repository or to a virtual one over it, reads
// the remote's feed before any IPK and fails there, so only a direct call
// reaches the IPK read's own refusal.
func TestUnitOpkgRemoteReadsWithoutRemoteSources(t *testing.T) {
	w := newItemWorld(t)
	remote := *repo.Defaults(&repo.Config{
		Key: "opkg-remote", Class: repo.Remote, PackageType: repo.Opkg,
		URL: "https://up.example",
	})
	virtual := *repo.Defaults(&repo.Config{
		Key: "opkg-virtual", Class: repo.Virtual, PackageType: repo.Opkg,
		Repositories: []string{remote.Key},
	})
	for _, c := range []repo.Config{remote, virtual} {
		if err := w.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	const ipk = "feed/alpha_1.0_all.ipk"
	for _, tc := range []struct {
		name  string
		serve func() *httptest.ResponseRecorder
	}{
		{"remote feed", func() *httptest.ResponseRecorder {
			return w.do(http.MethodGet, "/artifactory/opkg-remote/feed/Packages", "admin", "")
		}},
		{"remote IPK", func() *httptest.ResponseRecorder {
			return w.do(http.MethodGet, "/artifactory/opkg-remote/"+ipk, "admin", "")
		}},
		{"virtual IPK", func() *httptest.ResponseRecorder {
			return w.do(http.MethodGet, "/artifactory/opkg-virtual/"+ipk, "admin", "")
		}},
		{"direct remote IPK read", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r := kittest.Request(t, http.MethodGet, "/artifactory/opkg-remote/"+ipk)
			req := itemkit.ItemRequest{Key: remote.Key, Path: ipk}
			opkgItems{w.kit}.serveRemoteOpkgPackage(rec, r, remote, false, req,
				opkg.Package{Path: ipk}, kittest.Admin, kittest.AdminAccess())
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, tc.serve(), http.StatusInternalServerError, "internal error")
		})
	}
}

// The fixture feed's path, and the IPK of its 464xlat package, whose bytes
// opkgUpstreamFixture returns.
const (
	opkgFeed  = "feed/Packages"
	opkgXlat  = "feed/464xlat_13_x86_64.ipk"
	opkgOther = "feed/6in4_29_all.ipk"
)

// opkgRepos stores each of cs as an Opkg repository with its defaults.
func (w *itemWorld) opkgRepos(cs ...repo.Config) {
	w.t.Helper()
	for _, c := range cs {
		c.PackageType = repo.Opkg
		if c.Class == repo.Remote {
			c.URL = "https://up.example"
		}
		if err := w.db.Repos().Create(w.t.Context(), *repo.Defaults(&c)); err != nil {
			w.t.Fatal(err)
		}
	}
}

// put stores body as the file at path in the repository key, blob first,
// as a deploy leaves it.
func (w *itemWorld) put(key, path string, body []byte) {
	w.t.Helper()
	upload, err := w.blobs.BeginUpload(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := upload.Append(w.t.Context(), bytes.NewReader(body)); err != nil {
		w.t.Fatal(err)
	}
	info, err := upload.Commit(w.t.Context(), blob.Digests{})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.db.Items().Put(w.t.Context(), model.Item{
		RepoKey: key, Name: path, Size: int64(len(body)),
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256},
	}); err != nil {
		w.t.Fatal(err)
	}
}

// withSource sets the world's remote source service over backend.
func withSource(backend sourceBackend) func(*Config) {
	return func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend}
	}
}

// feedPaths returns the paths of the packages a served feed lists, failing
// the test when it was not served or does not decode.
func feedPaths(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("feed answered %d %s, want 200", rec.Code, rec.Body)
	}
	packages, err := opkg.DecodeIndex(rec.Body.Bytes(), opkgFeed)
	if err != nil {
		t.Fatalf("served feed does not decode: %v", err)
	}
	paths := make([]string, 0, len(packages))
	for _, pkg := range packages {
		paths = append(paths, pkg.Path)
	}
	return paths
}

// feedOf serves index as the upstream's feed and ipk as its 464xlat IPK,
// and fails the test when the source is asked for an IPK whose remote's
// patterns reject it.
func feedOf(t *testing.T, index, ipk []byte) sourceBackend {
	return func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
		body := index
		if q.Resource.Path == opkgXlat {
			if q.Repo.ExcludesPattern != nil &&
				strings.Contains(*q.Repo.ExcludesPattern, "464xlat") {
				t.Errorf("the source of %s was asked for an IPK its patterns reject", q.Repo.Key)
			}
			body = ipk
		} else if q.Resource.Path != opkgFeed {
			return source.Result{}, kernel.ErrNotFound
		}
		return source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
			Representation: source.Representation{Size: int64(len(body))},
		}}, nil
	}
}

// TestAccOpkgRefusalsAnswerTheDownloadPolicy: a feed, and a remote or
// virtual IPK, is refused to a caller who may not read it before anything
// else (DV-057). Past that, a blacked-out local or remote repository, its
// -cache alias, or patterns that reject the requested path answer the
// download policy's 404 and message before the source is asked, and a
// virtual repository whose own patterns reject the path answers that no
// repository serves it.
func TestAccOpkgRefusalsAnswerTheDownloadPolicy(t *testing.T) {
	index, _ := opkgUpstreamFixture(t)
	w := newItemWorld(t)
	w.opkgRepos(
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{Key: "opkg-dark", Class: repo.Local, BlackedOut: new(true)},
		repo.Config{Key: "opkg-hidden", Class: repo.Local, ExcludesPattern: new("**/Packages*")},
		repo.Config{Key: "opkg-dark-remote", Class: repo.Remote, BlackedOut: new(true)},
		repo.Config{Key: "opkg-picky-remote", Class: repo.Remote, ExcludesPattern: new("**/*.ipk")},
		repo.Config{
			Key: "opkg-closed", Class: repo.Virtual, Repositories: []string{"opkg-local"},
			ExcludesPattern: new("feed/**"),
		},
	)
	for _, key := range []string{"opkg-local", "opkg-dark", "opkg-hidden"} {
		w.put(key, opkgFeed, index)
	}
	blackedOut := func(key, path string) string {
		return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
			key + ":" + path + "'."
	}
	rejected := func(key, path string) string {
		return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
			"' due to its include/exclude pattern settings."
	}
	for _, tc := range []struct {
		name, key, path, as string
		status              int
		message             string
	}{
		{"user reads a feed", "opkg-local", opkgFeed, "user", http.StatusForbidden, "Forbidden"},
		{
			"user reads a blacked-out feed", "opkg-dark", opkgFeed, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"user reads an IPK a remote's patterns reject", "opkg-picky-remote", opkgXlat, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"blacked-out feed", "opkg-dark", opkgFeed, "admin",
			http.StatusNotFound, blackedOut("opkg-dark", opkgFeed),
		},
		{
			"blacked-out compressed feed", "opkg-dark", opkgFeed + ".gz", "admin",
			http.StatusNotFound, blackedOut("opkg-dark", opkgFeed+".gz"),
		},
		{
			"feed the patterns reject", "opkg-hidden", opkgFeed, "admin",
			http.StatusNotFound, rejected("opkg-hidden", opkgFeed),
		},
		{
			"blacked-out remote feed", "opkg-dark-remote", opkgFeed, "admin",
			http.StatusNotFound, blackedOut("opkg-dark-remote", opkgFeed),
		},
		{
			"blacked-out remote's cache", "opkg-dark-remote-cache", opkgFeed, "admin",
			http.StatusNotFound, blackedOut("opkg-dark-remote", opkgFeed),
		},
		{
			"blacked-out remote's IPK", "opkg-dark-remote", opkgXlat, "admin",
			http.StatusNotFound, blackedOut("opkg-dark-remote", opkgXlat),
		},
		{
			"remote IPK the patterns reject", "opkg-picky-remote", opkgXlat, "admin",
			http.StatusNotFound, rejected("opkg-picky-remote", opkgXlat),
		},
		{
			"virtual feed its patterns reject", "opkg-closed", opkgFeed, "admin",
			http.StatusNotFound, "No repository found to serve the request for opkg-closed:" + opkgFeed,
		},
		{
			"virtual IPK its patterns reject", "opkg-closed", opkgXlat, "admin",
			http.StatusNotFound, "No repository found to serve the request for opkg-closed:" + opkgXlat,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.do(http.MethodGet, "/artifactory/"+tc.key+"/"+tc.path, tc.as, "")
			wantError(t, rec, tc.status, tc.message)
		})
	}
}

// TestAccOpkgFeedKeepsAPackageThePatternsReject: a local or remote feed is
// served as it is, listing a package whose IPK the repository's patterns
// reject, as 7.x keeps such a package in its indexes (RTFACT-30830,
// RTFACT-30656); only that IPK's GET answers the policy's 404. A remote
// whose patterns reject only the feed's path still serves the IPK, which a
// download checks at its own path: the feed read that pins its SHA-256
// checks the remote's blackout alone.
func TestAccOpkgFeedKeepsAPackageThePatternsReject(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t, withSource(feedOf(t, index, ipk)))
	w.opkgRepos(
		repo.Config{Key: "opkg-local", Class: repo.Local, ExcludesPattern: new("**/464xlat*")},
		repo.Config{Key: "opkg-remote", Class: repo.Remote, ExcludesPattern: new("**/464xlat*")},
		repo.Config{
			Key: "opkg-feedless", Class: repo.Remote, ExcludesPattern: new("**/Packages*"),
		},
	)
	w.put("opkg-local", opkgFeed, index)
	w.put("opkg-local", opkgXlat, ipk)
	for _, key := range []string{"opkg-local", "opkg-remote"} {
		t.Run(key, func(t *testing.T) {
			rec := w.do(http.MethodGet, "/artifactory/"+key+"/"+opkgFeed, "admin", "")
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
				t.Errorf("feed answered %d with %d bytes, want the feed as it is",
					rec.Code, rec.Body.Len())
			}
			wantError(t, w.do(http.MethodGet, "/artifactory/"+key+"/"+opkgXlat, "admin", ""),
				http.StatusNotFound, "The repository '"+key+"' rejected the artifact '"+key+":"+
					opkgXlat+"' due to its include/exclude pattern settings.")
		})
	}
	rec := w.do(http.MethodGet, "/artifactory/opkg-feedless/"+opkgXlat, "admin", "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), ipk) {
		t.Errorf("IPK behind a rejected feed answered %d with %d bytes, want the IPK",
			rec.Code, rec.Body.Len())
	}
}

// TestAccOpkgVirtualKeepsPackagesThePatternsReject: a virtual feed keeps a
// package whose IPK a local or remote member's patterns, or a virtual
// repository's own, reject, nested ones included. The IPK's GET passes
// over the refusing member to the next that lists it, without asking a
// refusing remote's source, answers not found when none serves it, and
// answers that no repository serves it when the virtual repository's own
// patterns reject it.
func TestAccOpkgVirtualKeepsPackagesThePatternsReject(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t, withSource(feedOf(t, index, ipk)))
	w.opkgRepos(
		repo.Config{Key: "opkg-hiding", Class: repo.Local, ExcludesPattern: new("**/464xlat*")},
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{Key: "opkg-remote", Class: repo.Remote, ExcludesPattern: new("**/464xlat*")},
		repo.Config{
			Key: "opkg-virtual", Class: repo.Virtual,
			Repositories: []string{"opkg-hiding", "opkg-local"},
		},
		repo.Config{
			Key:          "opkg-only-hiding",
			Class:        repo.Virtual,
			Repositories: []string{"opkg-hiding"},
		},
		repo.Config{
			Key: "opkg-picky", Class: repo.Virtual, Repositories: []string{"opkg-local"},
			ExcludesPattern: new("**/464xlat*"),
		},
		repo.Config{Key: "opkg-nested", Class: repo.Virtual, Repositories: []string{"opkg-picky"}},
		repo.Config{
			Key:          "opkg-over-remote",
			Class:        repo.Virtual,
			Repositories: []string{"opkg-remote"},
		},
	)
	for _, key := range []string{"opkg-hiding", "opkg-local"} {
		w.put(key, opkgFeed, index)
		w.put(key, opkgXlat, ipk)
	}
	for _, key := range []string{
		"opkg-virtual", "opkg-only-hiding", "opkg-picky", "opkg-nested", "opkg-over-remote",
	} {
		got := feedPaths(t, w.do(http.MethodGet, "/artifactory/"+key+"/"+opkgFeed, "admin", ""))
		if !slices.Equal(got, []string{opkgXlat, opkgOther}) {
			t.Errorf("%s feed lists %v, want both packages", key, got)
		}
	}
	rec := w.do(http.MethodGet, "/artifactory/opkg-virtual/"+opkgXlat, "admin", "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), ipk) {
		t.Errorf("IPK answered %d with %d bytes, want opkg-local's", rec.Code, rec.Body.Len())
	}
	for _, key := range []string{"opkg-only-hiding", "opkg-nested", "opkg-over-remote"} {
		wantError(t, w.do(http.MethodGet, "/artifactory/"+key+"/"+opkgXlat, "admin", ""),
			http.StatusNotFound, "Could not find resource")
	}
	wantError(t, w.do(http.MethodGet, "/artifactory/opkg-picky/"+opkgXlat, "admin", ""),
		http.StatusNotFound, "No repository found to serve the request for opkg-picky:"+opkgXlat)
}

// TestAccOpkgVirtualIPKChecksAMemberAtTheIPKsPath: a virtual IPK GET checks
// each member at the IPK's own path, never at the feed it reads to find the
// IPK. A local or remote member, or a nested virtual one, whose patterns
// reject only its feed serves the IPK, while the virtual feed still leaves
// that member out; a blacked-out member, which refuses every file, takes no
// part.
func TestAccOpkgVirtualIPKChecksAMemberAtTheIPKsPath(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t, withSource(feedOf(t, index, ipk)))
	w.opkgRepos(
		repo.Config{Key: "opkg-feedless", Class: repo.Local, ExcludesPattern: new("**/Packages*")},
		repo.Config{Key: "opkg-remote", Class: repo.Remote, ExcludesPattern: new("**/Packages*")},
		repo.Config{Key: "opkg-dark", Class: repo.Local, BlackedOut: new(true)},
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{
			Key: "opkg-closed", Class: repo.Virtual, Repositories: []string{"opkg-local"},
			ExcludesPattern: new("**/Packages*"),
		},
		repo.Config{
			Key: "opkg-over-feedless", Class: repo.Virtual, Repositories: []string{"opkg-feedless"},
		},
		repo.Config{
			Key:          "opkg-over-remote",
			Class:        repo.Virtual,
			Repositories: []string{"opkg-remote"},
		},
		repo.Config{
			Key:          "opkg-over-dark",
			Class:        repo.Virtual,
			Repositories: []string{"opkg-dark"},
		},
		repo.Config{
			Key:          "opkg-over-closed",
			Class:        repo.Virtual,
			Repositories: []string{"opkg-closed"},
		},
	)
	for _, key := range []string{"opkg-feedless", "opkg-dark", "opkg-local"} {
		w.put(key, opkgFeed, index)
		w.put(key, opkgXlat, ipk)
	}
	for _, key := range []string{
		"opkg-over-feedless", "opkg-over-remote", "opkg-over-dark", "opkg-over-closed",
	} {
		rec := w.do(http.MethodGet, "/artifactory/"+key+"/"+opkgFeed, "admin", "")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "464xlat") {
			t.Errorf("%s feed answered %d %s, want the refusing member left out",
				key, rec.Code, rec.Body)
		}
	}
	for _, key := range []string{"opkg-over-feedless", "opkg-over-remote", "opkg-over-closed"} {
		rec := w.do(http.MethodGet, "/artifactory/"+key+"/"+opkgXlat, "admin", "")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), ipk) {
			t.Errorf("%s IPK answered %d with %d bytes, want the member's IPK",
				key, rec.Code, rec.Body.Len())
		}
	}
	wantError(t, w.do(http.MethodGet, "/artifactory/opkg-over-dark/"+opkgXlat, "admin", ""),
		http.StatusNotFound, "Could not find resource")
}
