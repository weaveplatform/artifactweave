package swift

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	swiftfmt "github.com/weaveplatform/artifactweave/internal/format/swift"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxPublishMetadata = 64 << 10

func (h handler) localPublish(w http.ResponseWriter, r *http.Request, p swiftfmt.Package) {
	path := p.ArchivePath()
	if !h.k.Require(w, r, path, types.ActionWrite) {
		return
	}
	info, metadata, err := h.uploadRelease(w, r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), info.Digests.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	_, inspectErr := swiftfmt.InspectArchive(r.Context(), rc, info.Size)
	_ = rc.Close()
	if inspectErr != nil {
		h.k.Error(w, r, bad(http.StatusUnprocessableEntity, "invalid Swift source archive"))
		return
	}
	props := types.Properties{}
	if len(metadata) > 0 {
		props["swift.metadata"] = []string{string(metadata)}
	}
	_, err = h.k.StoreNew(r, path, info, props, types.Checksums{}, func() error {
		return bad(http.StatusConflict, "Swift release already exists")
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Version", "1")
	w.Header().Set("Location", releaseURL(r, p))
	w.WriteHeader(http.StatusCreated)
}

func (h handler) uploadRelease(w http.ResponseWriter, r *http.Request) (blob.Info, []byte, error) {
	if r.Header.Get("X-Swift-Package-Signature-Format") != "" {
		return blob.Info{}, nil, bad(http.StatusUnprocessableEntity,
			"signed Swift archives are not accepted without signature validation")
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return blob.Info{}, nil, bad(http.StatusUnsupportedMediaType,
			"Swift publishing requires multipart/form-data")
	}
	r.Body = http.MaxBytesReader(w, r.Body, swiftfmt.MaxArchiveBytes+maxPublishMetadata+(1<<20))
	mr := multipart.NewReader(r.Body, params["boundary"])
	var info blob.Info
	var metadata []byte
	gotArchive, gotMetadata := false, false
	for {
		// NextPart silently decodes quoted-printable and removes its transfer
		// header, which would bypass the explicit Swift publish encoding policy.
		part, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return blob.Info{}, nil, bad(http.StatusBadRequest, "malformed Swift publish form")
		}
		switch part.FormName() {
		case "source-archive":
			if gotArchive {
				return blob.Info{}, nil, bad(http.StatusBadRequest, "duplicate source archive")
			}
			gotArchive = true
			body := io.Reader(part)
			switch strings.ToLower(part.Header.Get("Content-Transfer-Encoding")) {
			case "", "binary", "8bit":
			case "base64":
				body = base64.NewDecoder(base64.StdEncoding, part)
			default:
				return blob.Info{}, nil, bad(
					http.StatusBadRequest,
					"unsupported archive transfer encoding",
				)
			}
			info, err = h.k.Upload(r, io.LimitReader(body, swiftfmt.MaxArchiveBytes+1))
			if err != nil {
				return blob.Info{}, nil, fmt.Errorf("swift: upload source archive: %w", err)
			}
			if info.Size == 0 || info.Size > swiftfmt.MaxArchiveBytes {
				return blob.Info{}, nil, bad(
					http.StatusRequestEntityTooLarge,
					"Swift source archive exceeds limit",
				)
			}
		case "metadata":
			if gotMetadata {
				return blob.Info{}, nil, bad(http.StatusBadRequest, "duplicate Swift metadata")
			}
			gotMetadata = true
			metadata, err = io.ReadAll(io.LimitReader(part, maxPublishMetadata+1))
			if err != nil || len(metadata) > maxPublishMetadata {
				return blob.Info{}, nil, bad(
					http.StatusRequestEntityTooLarge,
					"Swift metadata exceeds limit",
				)
			}
			if len(metadata) > 0 {
				var value map[string]any
				if err := json.Unmarshal(metadata, &value); err != nil || value == nil {
					return blob.Info{}, nil, bad(
						http.StatusBadRequest,
						"invalid Swift metadata JSON",
					)
				}
				metadata, err = json.Marshal(value)
				if err != nil {
					return blob.Info{}, nil, fmt.Errorf("swift: metadata normalization: %w", err)
				}
			}
		case "source-archive-signature", "metadata-signature":
			return blob.Info{}, nil, bad(http.StatusUnprocessableEntity,
				"signed Swift releases require signature validation")
		default:
			return blob.Info{}, nil, bad(http.StatusBadRequest, "unknown Swift publish part")
		}
		if err := part.Close(); err != nil {
			return blob.Info{}, nil, bad(http.StatusBadRequest, "malformed Swift publish form")
		}
	}
	if !gotArchive {
		return blob.Info{}, nil, bad(http.StatusBadRequest, "missing source-archive part")
	}
	return info, metadata, nil
}
