package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

func TestOpenBlobRetriesTransientSharingViolation(t *testing.T) {
	s := newStore(t)
	want := []byte("ready")
	u := begin(t, s, want)
	info, err := u.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	swap(t, &osOpenPublished, func(p string) (*os.File, error) {
		attempts++
		if attempts <= 3 {
			return nil, &os.PathError{Op: "open", Path: p, Err: windows.ERROR_SHARING_VIOLATION}
		}
		return os.Open(p)
	})
	f, _, err := s.Open(t.Context(), info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil || string(got) != string(want) {
		t.Fatalf("published blob = %q, error %v", got, err)
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4", attempts)
	}
}

func TestOpenBlobDoesNotRetryOtherErrors(t *testing.T) {
	attempts := 0
	swap(t, &osOpenPublished, func(p string) (*os.File, error) {
		attempts++
		return nil, &os.PathError{Op: "open", Path: p, Err: os.ErrPermission}
	})
	_, err := openBlobFile(context.Background(), "denied")
	if !errors.Is(err, os.ErrPermission) || attempts != 1 {
		t.Fatalf(
			"open = %v after %d attempts, want permission error on first attempt",
			err,
			attempts,
		)
	}
}
