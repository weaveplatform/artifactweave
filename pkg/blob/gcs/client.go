package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// ClientConfig mirrors the blob.gcs section of the application
// configuration.
type ClientConfig struct {
	// Endpoint overrides the Cloud Storage JSON API endpoint, for emulators,
	// e.g. "http://localhost:4443". "/storage/v1/" is appended when missing.
	Endpoint string
	// CredentialsFile is a service-account JSON key. Empty means Application
	// Default Credentials, which covers GKE Workload Identity.
	CredentialsFile string
	// GoogleAccessID and PrivateKey (PEM) sign URLs locally. Without them
	// signing finds the service account from the credentials and calls IAM
	// signBlob, which needs iam.serviceAccounts.signBlob.
	GoogleAccessID string
	PrivateKey     []byte
	// WithoutAuthentication sends no credentials, for emulators.
	WithoutAuthentication bool
}

// objectContentType is stored on every object. Blobs are opaque bytes; no
// content encoding is ever set, because GCS serves a gzip-encoded object
// whole whatever range is asked for.
const objectContentType = "application/octet-stream"

// NewAPI builds the API over the official Cloud Storage client for bucket,
// which must exist. It uses the JSON API for reads as well as writes.
func NewAPI(ctx context.Context, bucket string, cfg ClientConfig) (API, error) {
	if bucket == "" {
		return nil, fmt.Errorf("%w: bucket is required", ErrConfig)
	}
	opts := []option.ClientOption{storage.WithJSONReads()}
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(normaliseEndpoint(cfg.Endpoint)))
	}
	switch {
	case cfg.WithoutAuthentication:
		opts = append(opts, option.WithoutAuthentication())
	case cfg.CredentialsFile != "":
		opts = append(
			opts,
			option.WithAuthCredentialsFile(option.ServiceAccount, cfg.CredentialsFile),
		)
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("blob/gcs: client: %w", err)
	}
	return &sdkAPI{
		bucket:   client.Bucket(bucket),
		accessID: cfg.GoogleAccessID,
		key:      cfg.PrivateKey,
	}, nil
}

// normaliseEndpoint ends an endpoint in "/storage/v1/"; without the trailing
// slash the SDK's JSON calls are sent to the wrong path.
func normaliseEndpoint(e string) string {
	e = strings.TrimSuffix(e, "/")
	if !strings.HasSuffix(e, "/storage/v1") {
		e += "/storage/v1"
	}
	return e + "/"
}

// sdkAPI implements API over one bucket handle.
type sdkAPI struct {
	bucket   *storage.BucketHandle
	accessID string
	key      []byte
}

func toAttrs(a *storage.ObjectAttrs) Attrs {
	return Attrs{Name: a.Name, Size: a.Size, CRC32C: a.CRC32C, Generation: a.Generation}
}

// Write sends data in one request. The chunk size is set just above the
// length so the SDK buffers the whole object and can retry it; at or above
// the chunk size it would switch to a resumable upload. The must-not-exist
// precondition is what makes the write idempotent, and so retried.
func (a *sdkAPI) Write(ctx context.Context, name string, data []byte, crc uint32) (Attrs, error) {
	w := a.bucket.Object(name).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ChunkSize = (len(data)/partAlign + 1) * partAlign
	w.ContentType = objectContentType
	w.CRC32C, w.SendCRC32C = crc, true
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return Attrs{}, translate(err)
	}
	if err := w.Close(); err != nil {
		return Attrs{}, translate(err)
	}
	return toAttrs(w.Attrs()), nil
}

func (a *sdkAPI) Attrs(ctx context.Context, name string) (Attrs, error) {
	attrs, err := a.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return Attrs{}, translate(err)
	}
	return toAttrs(attrs), nil
}

// Read opens a ranged JSON read. ReadCompressed is deliberately not set: with
// JSON reads the SDK refuses it client-side ("custom Accept-Encoding headers
// not allowed"), and it is unneeded because Write never sets a content
// encoding, so GCS never transcodes these objects.
func (a *sdkAPI) Read(ctx context.Context, name string, offset int64) (io.ReadCloser, error) {
	r, err := a.bucket.Object(name).NewRangeReader(ctx, offset, -1)
	if err != nil {
		return nil, translate(err)
	}
	return r, nil
}

func (a *sdkAPI) Compose(
	ctx context.Context,
	dst string,
	srcs []Source,
	deleteSources bool,
) (Attrs, error) {
	handles := make([]*storage.ObjectHandle, len(srcs))
	for i, s := range srcs {
		handles[i] = a.bucket.Object(s.Name).Generation(s.Generation)
	}
	c := a.bucket.Object(dst).If(storage.Conditions{DoesNotExist: true}).ComposerFrom(handles...)
	c.ContentType = objectContentType
	c.DeleteSourceObjects = deleteSources
	attrs, err := c.Run(ctx)
	if err != nil {
		return Attrs{}, translate(err)
	}
	return toAttrs(attrs), nil
}

func (a *sdkAPI) Delete(ctx context.Context, name string, generation int64) error {
	obj := a.bucket.Object(name)
	if generation != 0 {
		obj = obj.If(storage.Conditions{GenerationMatch: generation})
	}
	return translate(obj.Delete(ctx))
}

func (a *sdkAPI) List(ctx context.Context, prefix string, fn func(Attrs) error) error {
	q := &storage.Query{Prefix: prefix}
	if err := q.SetAttrSelection([]string{"Name", "Size", "CRC32C", "Generation"}); err != nil {
		return fmt.Errorf("blob/gcs: list: %w", err)
	}
	it := a.bucket.Objects(ctx, q)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return translate(err)
		}
		if err := fn(toAttrs(attrs)); err != nil {
			return err
		}
	}
}

func (a *sdkAPI) SignedURL(name string, ttl time.Duration) (string, error) {
	url, err := a.bucket.SignedURL(name, &storage.SignedURLOptions{
		GoogleAccessID: a.accessID,
		PrivateKey:     a.key,
		Method:         http.MethodGet,
		Expires:        time.Now().Add(ttl),
		Scheme:         storage.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("blob/gcs: sign: %w", err)
	}
	return url, nil
}

// translate maps the SDK's errors onto the store's sentinels: a missing
// object is blob.ErrNotFound (the SDK reports it as ErrObjectNotExist on
// reads and as a bare 404 on compose) and a failed precondition is
// ErrPrecondition.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %w", blob.ErrNotFound, err)
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", blob.ErrNotFound, err)
		case http.StatusPreconditionFailed:
			return fmt.Errorf("%w: %w", ErrPrecondition, err)
		}
	}
	return fmt.Errorf("blob/gcs: %w", err)
}
