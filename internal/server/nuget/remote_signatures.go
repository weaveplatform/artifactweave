package nuget

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (s *remoteSession) signatureDocument(version string) (remoteDoc, error) {
	if !signatureVersion(version) {
		return nil, notFound(s.r)
	}
	target, err := s.endpoint("repository-signatures/" + version + ".json")
	if err != nil {
		return nil, err
	}
	d, _, err := s.document(
		target,
		".nuget/repository-signatures/"+version+".json",
		"",
		"signatures",
		version,
	)
	return d, err
}

func (s *remoteSession) signatures(w http.ResponseWriter, version string) {
	d, err := s.signatureDocument(version)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	certs := arrayField(d, "signingCertificates")
	for _, cert := range certs {
		setField(
			cert,
			"contentUrl",
			v3Base(s.r)+"/repository-certificates/"+version+"/"+certificateHash(cert)+".der",
		)
	}
	setField(d, "signingCertificates", certs)
	s.json(w, d)
}

func (s *remoteSession) certificate(w http.ResponseWriter, version, hash string) {
	d, err := s.signatureDocument(version)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	for _, cert := range arrayField(d, "signingCertificates") {
		if certificateHash(cert) != hash {
			continue
		}
		// Certificates are public protocol metadata; use the repository metadata
		// grant, not an invented artifact permission on an internal cache filename.
		result, readErr := s.h.k.Read(s.r, source.Resource{
			Kind:             source.Metadata,
			Path:             ".nuget/certificates/" + hash + ".der",
			PermissionFolder: true,
			Method:           http.MethodGet,
			ExternalURL:      stringField(cert, "contentUrl"),
			Checksums:        types.Checksums{SHA256: hash},
			Variant:          "nuget-certificate-v1",
			Validate: func(_ context.Context, r io.ReadSeeker, size int64) error {
				if size > 1<<20 {
					return remoteFailure()
				}
				raw, e := io.ReadAll(io.LimitReader(r, 1<<20+1))
				if e != nil {
					return remoteFailure()
				}
				if _, e = x509.ParseCertificate(raw); e != nil {
					return remoteFailure()
				}
				sum := sha256.Sum256(raw)
				if hex.EncodeToString(sum[:]) != hash {
					return remoteFailure()
				}
				return nil
			},
		})
		if readErr != nil {
			s.h.k.Error(w, s.r, readErr)
			return
		}
		current := result.Revalidate
		result.Revalidate = func(ctx context.Context) error {
			if err := s.fence(ctx); err != nil {
				return err
			}
			if current != nil {
				return current(ctx)
			}
			return nil
		}
		if result.Stream != nil {
			result.Stream.MediaType = "application/pkix-cert"
		}
		s.h.k.ServeResult(w, s.r, result)
		return
	}
	s.h.k.Error(w, s.r, notFound(s.r))
}
