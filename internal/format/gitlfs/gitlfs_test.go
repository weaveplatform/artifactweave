package gitlfs

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitObjectPathRequiresCanonicalSHA256(t *testing.T) {
	valid := strings.Repeat("a", 64)
	path, ok := ObjectPath(valid)
	if !ok || path != "objects/aa/aa/"+valid {
		t.Fatalf("canonical object path = %q, %t", path, ok)
	}
	for _, oid := range []string{valid[:63], strings.ToUpper(valid), valid[:63] + "g"} {
		if path, ok := ObjectPath(oid); ok || path != "" {
			t.Fatalf("malformed object ID %q admitted as %q", oid, path)
		}
	}
}

func TestUnitAdmitRejectsNonObjectWrites(t *testing.T) {
	oid := strings.Repeat("a", 64)
	path, _ := ObjectPath(oid)
	for _, tc := range []struct {
		name  string
		write format.Write
	}{
		{name: "checksum sidecar", write: format.Write{Op: format.OpChecksum, Path: path}},
		{name: "noncanonical path", write: format.Write{
			Op: format.OpDeploy, Path: "objects/ff/ff/" + oid,
			Checksums: types.Checksums{SHA256: oid},
		}},
		{name: "mismatched digest", write: format.Write{
			Op: format.OpDeploy, Path: path,
			Checksums: types.Checksums{SHA256: strings.Repeat("b", 64)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (Handler{}).Admit(t.Context(), format.Env{}, repo.Config{}, tc.write)
			var rejection *format.RejectionError
			if !errors.As(err, &rejection) || rejection.Status != http.StatusConflict {
				t.Fatalf("unsafe Git LFS write: %v", err)
			}
		})
	}
}
