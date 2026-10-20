package pypi

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/blake2b"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) upload(w http.ResponseWriter, r *http.Request) {
	if rest := pkgroute.RequestFrom(r).Rest; rest != "" && rest != "/" {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"Twine is allowed to deploy packages only to the base level",
			),
		)
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionWrite) {
		return
	}
	form, err := h.k.ReadForm(r, "content")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if form.Fields.Get(":action") != "file_upload" {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusBadRequest, "The upload action must be file_upload"),
		)
		return
	}
	declared, err := h.digests(r, form)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	// The complete uploaded content is known, even when a client omitted the
	// digest field. Declaring its computed SHA256 enables identical-content
	// reuploads without granting permission to replace different bytes.
	if declared.SHA256 == "" {
		declared.SHA256 = form.File.Digests.SHA256
	}
	_, err = h.k.StoreResolved(
		r,
		form.FileName,
		form.File,
		declared,
		func(inspected any) (string, error) {
			return pypifmt.UploadPath(
				inspected,
				pkgroute.RequestFrom(r).Repo,
				form.Fields.Get("name"),
				form.Fields.Get("version"),
			)
		},
	)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// put is REST deployment through the package API, including matrix properties.
// The item service applies the same format admission as ordinary item routes.
func (h handler) put(w http.ResponseWriter, r *http.Request) {
	h.k.DeployItem(w, r)
}

func (h handler) digests(r *http.Request, f pkgroute.Form) (types.Checksums, error) {
	var out types.Checksums
	for _, check := range []struct {
		name, want string
		dst        *string
		size       int
	}{
		{"sha256_digest", f.File.Digests.SHA256, &out.SHA256, 32},
		{"md5_digest", f.File.Digests.MD5, &out.MD5, 16},
	} {
		raw := f.Fields.Get(check.name)
		if raw == "" {
			continue
		}
		data, err := hex.DecodeString(raw)
		if check.name == "md5_digest" && (err != nil || len(data) != check.size) {
			data, err = base64.RawURLEncoding.DecodeString(raw)
		}
		if err != nil || len(data) != check.size || hex.EncodeToString(data) != check.want {
			return out, apierror.New(
				http.StatusBadRequest,
				"The "+check.name+" does not match the uploaded file",
			)
		}
		*check.dst = hex.EncodeToString(data)
	}
	if raw := f.Fields.Get("blake2_256_digest"); raw != "" {
		want, err := hex.DecodeString(raw)
		if err != nil || len(want) != 32 {
			return out, apierror.New(http.StatusBadRequest, "Invalid blake2_256_digest")
		}
		content, _, err := h.k.Blobs().Open(r.Context(), f.File.Digests.SHA256)
		if err != nil {
			return out, fmt.Errorf("pypi: read uploaded digest: %w", err)
		}
		defer content.Close()
		sum, _ := blake2b.New256(nil)
		if _, err = io.Copy(sum, content); err != nil {
			return out, fmt.Errorf("pypi: hash uploaded content: %w", err)
		}
		if !strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), raw) {
			return out, apierror.New(
				http.StatusBadRequest,
				"The blake2_256_digest does not match the uploaded file",
			)
		}
	}
	return out, nil
}
