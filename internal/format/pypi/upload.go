package pypi

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// UploadPath resolves a legacy upload using the format's existing inspection,
// so admission and indexing reuse the same archive read. Form metadata may
// confirm the distribution's identity but cannot choose where it is stored.
func UploadPath(inspected any, repository repo.Config, name, version string) (string, error) {
	in, ok := inspected.(*inspection)
	if !ok || in.dist == nil {
		return "", format.Rejectf(
			http.StatusBadRequest,
			"The uploaded file has no valid distribution metadata",
		)
	}
	d := in.dist
	if name != "" && Normalize(name) != Normalize(d.Name) {
		return "", format.Rejectf(
			http.StatusBadRequest,
			"The form name does not match the distribution metadata",
		)
	}
	if version != "" && types.ComparePEP440(version, d.Version) != 0 {
		return "", format.Rejectf(
			http.StatusBadRequest,
			"The form version does not match the distribution metadata",
		)
	}
	destination := CanonicalPath(d.Name, d.Version, in.name)
	if err := checkReserved(repository, format.Write{Path: destination}); err != nil {
		return "", err
	}
	return destination, nil
}
