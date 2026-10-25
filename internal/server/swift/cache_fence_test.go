package swift

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitSwiftRegistryInputAndCancellationBoundaries(t *testing.T) {
	for _, raw := range []string{
		"acme/%2FWidget", "acme/Widget\\name", "acme/Widget/1.0.0/extra",
		"acme/Widget/not-a-version", "-bad/Widget", "acme/Widget%00",
	} {
		if _, err := parse(raw); err == nil {
			t.Fatalf("unsafe Swift registry path accepted: %q", raw)
		}
	}
	for _, version := range []string{"", "6..0", "6.x", "6.0.0.0", "12345678901234567"} {
		if validToolsVersion(version) {
			t.Fatalf("unsafe Swift tools version accepted: %q", version)
		}
	}
	check := validateJSON(100, func(r io.Reader) error {
		_, err := parseReleases(r)
		return err
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := check(
		ctx,
		bytes.NewReader([]byte(`{"releases":{}}`)),
		15,
		source.MetadataResponse{},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled remote metadata validation continued: %v", err)
	}
	for _, body := range [][]byte{nil, []byte(`{"releases":`)} {
		if err := check(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			source.MetadataResponse{},
		); err == nil {
			t.Fatalf("invalid remote metadata accepted: %q", body)
		}
	}
	if _, err := (virtualSource{}).Item(t.Context(), nil, repo.Config{}, ""); err == nil {
		t.Fatal("virtual source exposed an unsupported generic item")
	}
}

func TestUnitSwiftCachedMetadataBlobAndPermissionFence(t *testing.T) {
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"releases":{"1.0.0":{}}}`)
	upload, err := blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	k := pkgroute.Mount(chi.NewRouter(), pkgroute.Options{
		Items: func() *item.Service { return &item.Service{Blobs: blobs} },
	}, pkgroute.Protocol{Name: "swift"})
	h := handler{k: k}
	r := httptest.NewRequest(http.MethodGet, "/swift/acme/Widget", nil)
	result, err := h.metadataStream(r, source.Result{Item: &types.Item{
		Size: info.Size, Actual: types.Checksums{SHA256: info.Digests.SHA256},
		MimeType: "application/json",
	}}, 1<<20)
	if err != nil || result.Stream == nil {
		t.Fatalf("cached metadata was unavailable: %v", err)
	}
	got, err := io.ReadAll(result.Stream.Body)
	_ = result.Stream.Body.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("cached metadata changed: %q %v", got, err)
	}
	for _, it := range []types.Item{
		{Size: info.Size + 1, Actual: types.Checksums{SHA256: info.Digests.SHA256}},
		{Size: info.Size, Actual: types.Checksums{SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	} {
		if _, err := h.metadataStream(r, source.Result{Item: &it}, info.Size); err == nil {
			t.Fatalf("invalid cached metadata was opened: %+v", it)
		}
	}
	if _, err := h.metadataStream(r, source.Result{}, 1<<20); err == nil {
		t.Fatal("missing cached metadata was accepted")
	}
	var order []string
	result = source.Result{Revalidate: func(context.Context) error {
		order = append(order, "archive")
		return nil
	}}
	metadata := source.Result{
		Revalidate: func(context.Context) error {
			order = append(order, "metadata")
			return nil
		},
		CheckRead: func(_ context.Context, path string) error {
			order = append(order, path)
			return nil
		},
	}
	fence(&result, metadata, []string{"acme/Widget/Widget-1.0.0.zip"})
	if err := result.Revalidate(t.Context()); err != nil || !reflect.DeepEqual(order,
		[]string{"archive", "metadata", "acme/Widget/Widget-1.0.0.zip"}) {
		t.Fatalf("archive delivery fence: order=%v err=%v", order, err)
	}
	result = source.Result{}
	fence(&result, source.Result{}, []string{"acme/Widget/Widget-1.0.0.zip"})
	if err := result.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("missing metadata permission check was accepted: %v", err)
	}
	result = source.Result{}
	fence(&result, source.Result{CheckRead: func(context.Context, string) error {
		return source.ErrDenied
	}}, []string{"acme/Widget/Widget-1.0.0.zip"})
	if err := result.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("revoked archive permission was accepted: %v", err)
	}
	for _, tc := range []struct {
		name     string
		archive  func(context.Context) error
		metadata func(context.Context) error
	}{
		{"archive credentials revoked", func(context.Context) error { return source.ErrDenied }, nil},
		{"metadata credentials revoked", nil, func(context.Context) error { return source.ErrDenied }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := source.Result{Revalidate: tc.archive}
			fence(&result, source.Result{
				Revalidate: tc.metadata,
				CheckRead:  func(context.Context, string) error { return nil },
			}, []string{"acme/Widget/Widget-1.0.0.zip"})
			if err := result.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
				t.Fatalf("revoked source credentials were accepted: %v", err)
			}
		})
	}
}
