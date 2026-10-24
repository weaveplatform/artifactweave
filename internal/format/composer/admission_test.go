package composer_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/composer"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func zipPackage(t *testing.T, manifest string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("composer.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, manifest)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAccHostedAdmissionAndReindex(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &item.Service{Store: db, Blobs: blobs}
	c := *repo.Defaults(&repo.Config{Key: "php", Class: repo.Local, PackageType: repo.Composer})
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	data := zipPackage(t, `{"name":"acme/widget","version":"1.0.0"}`)
	deploy := func(path string, body []byte, props types.Properties) error {
		_, err := service.Deploy(ctx, item.Deploy{Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(body), Properties: props, By: "admin"})
		return err
	}
	if err := deploy("acme/widget/1.0.0.zip", data, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		body  []byte
		props types.Properties
	}{
		{"other/widget/1.0.0.zip", data, nil},
		{"acme/widget/1.0.0.tar", data, nil},
		{"acme/widget/2.0.0.zip", data, types.Properties{"composer.version": {"2.0.0"}}},
		{"acme/widget/bad.zip", []byte("not a zip"), nil},
		{"acme/widget/two-props.zip", data, types.Properties{"composer.version": {"1.0.0", "1.0.1"}}},
	} {
		if err := deploy(tc.path, tc.body, tc.props); err == nil {
			t.Errorf("accepted %s", tc.path)
		}
		if _, err := db.Items().Get(ctx, c.Key, false, tc.path); err == nil {
			t.Errorf("published %s", tc.path)
		}
	}
	if err := (composer.Handler{}).Reindex(ctx, format.Env{Store: db, Blobs: blobs}, c); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	it, err := db.Items().Get(ctx, c.Key, false, "acme/widget/1.0.0.zip")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 501 {
		copy := it
		copy.ID = 0
		copy.Name = fmt.Sprintf("copy-%03d.zip", i)
		if _, err := db.Items().Put(ctx, copy); err != nil {
			t.Fatal(err)
		}
	}
	if err := (composer.Handler{}).Reindex(ctx, format.Env{Store: db, Blobs: blobs}, c); err != nil {
		t.Fatalf("paged reindex: %v", err)
	}
	it.Actual.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := db.Items().Put(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err := (composer.Handler{}).Reindex(ctx, format.Env{Store: db, Blobs: blobs}, c); err == nil {
		t.Fatal("corrupt stored archive passed reindex")
	}
	c.Class = repo.Remote
	if err := (composer.Handler{}).Reindex(ctx, format.Env{Store: db, Blobs: blobs}, c); err == nil {
		t.Fatal("remote reindex accepted")
	}
	if _, err := (composer.Handler{}).Inspect(ctx, format.Env{}, c, format.Write{Cached: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := (composer.Handler{}).Inspect(ctx, format.Env{Blobs: blobs}, c, format.Write{Path: "acme/widget/no-content.zip"}); err == nil {
		t.Fatal("contentless archive accepted")
	}
}
