package testfault_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/testfault"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
)

func TestDisconnect(t *testing.T) {
	got, err := io.ReadAll(testfault.Disconnect(strings.NewReader("hello world"), 5))
	if string(got) != "hello" || !errors.Is(err, testfault.ErrDisconnected) ||
		!errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read %q, %v", got, err)
	}
	// A body that ends first ends as it would.
	got, err = io.ReadAll(testfault.Disconnect(strings.NewReader("hi"), 5))
	if string(got) != "hi" || err != nil {
		t.Fatalf("short body: %q, %v", got, err)
	}
	if n, err := testfault.Disconnect(strings.NewReader("x"), 0).
		Read(make([]byte, 1)); n != 0 || !errors.Is(err, testfault.ErrDisconnected) {
		t.Fatalf("cut at once: %d, %v", n, err)
	}
}

func TestBlobs(t *testing.T) {
	ctx := context.Background()
	store, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var (
		errBegin  = errors.New("begin")
		errAppend = errors.New("append")
		errCommit = errors.New("commit")
	)
	if _, err := (testfault.Blobs{Store: store, Begin: errBegin}).
		BeginUpload(ctx); !errors.Is(err, errBegin) {
		t.Fatalf("begin: %v", err)
	}
	u, err := testfault.Blobs{Store: store, Append: errAppend}.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := u.Append(ctx, strings.NewReader("abc")); n != 3 || !errors.Is(err, errAppend) {
		t.Fatalf("append: %d, %v", n, err)
	}
	u, err = testfault.Blobs{Store: store, Commit: errCommit}.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errCommit) {
		t.Fatalf("commit: %v", err)
	}
	// Nothing failed is the store itself.
	u, err = testfault.Blobs{Store: store}.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil || info.Size != 3 {
		t.Fatalf("commit: %+v, %v", info, err)
	}
	// The store's own failure passes through.
	if _, err := (testfault.Blobs{Store: failingBegin{store}}).BeginUpload(ctx); err == nil {
		t.Fatal("the store's failure was lost")
	}
}

type failingBegin struct{ blob.Store }

func (failingBegin) BeginUpload(context.Context, ...blob.UploadOption) (blob.Upload, error) {
	return nil, errors.New("store down")
}
