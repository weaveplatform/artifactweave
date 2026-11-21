package source

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ContentExpectation binds a selected transfer to known bytes. Unlike an
// adapter's representation Checksums, it does not create another cache variant.
// A caller must create a new expectation for each immutable selected intent.
type ContentExpectation struct {
	Size      int64            `json:"size"`
	Checksums kernel.Checksums `json:"checksums"`
}

// Matches checks saved, computed facts, including every supplied digest.
func (e ContentExpectation) Matches(size int64, actual kernel.Checksums) bool {
	return e.Size == size &&
		(e.Checksums.SHA256 == "" || strings.EqualFold(e.Checksums.SHA256, actual.SHA256)) &&
		(e.Checksums.SHA512 == "" || strings.EqualFold(e.Checksums.SHA512, actual.SHA512)) &&
		(e.Checksums.SHA1 == "" || strings.EqualFold(e.Checksums.SHA1, actual.SHA1)) &&
		(e.Checksums.MD5 == "" || strings.EqualFold(e.Checksums.MD5, actual.MD5))
}

func validateRetrievalContract(q Request) error {
	if q.Publication != nil && (q.Resource.SelectedContent == nil || q.Publication.Check == nil || q.Publication.Fence == nil) {
		return ErrInvalidRequest
	}
	if err := validateSelectedContent(q); err != nil {
		return err
	}
	return validateMetadataMethod(q)
}

func validateSelectedContent(q Request) error {
	r := q.Resource
	if r.SelectedContent == nil {
		return nil
	}
	e := *r.SelectedContent
	file := r.Kind == ResourceKindArtifact || r.Kind == ResourceKindMetadata && r.RepositoryFile
	if e.Size < 0 || e.Checksums == (kernel.Checksums{}) || !file || q.CacheOnly || r.Method != http.MethodGet ||
		r.PermissionFolder || r.PermissionRoot || r.NoCacheFile || r.Transient || r.ChecksumOnly || r.CocoaPodsGit != nil ||
		r.Range != "" || r.IfRange != "" || r.IfNoneMatch != "" || r.IfModifiedSince != "" {
		return ErrInvalidRequest
	}
	for _, digest := range []struct {
		selected, representation string
		length                   int
	}{
		{e.Checksums.SHA256, r.Checksums.SHA256, 64},
		{e.Checksums.SHA512, r.Checksums.SHA512, 128},
		{e.Checksums.SHA1, r.Checksums.SHA1, 40},
		{e.Checksums.MD5, r.Checksums.MD5, 32},
	} {
		if digest.selected == "" {
			continue
		}
		if len(digest.selected) != digest.length || digest.representation != "" && !strings.EqualFold(digest.selected, digest.representation) {
			return ErrInvalidRequest
		}
		if _, err := hex.DecodeString(digest.selected); err != nil {
			return ErrInvalidRequest
		}
	}
	return nil
}
