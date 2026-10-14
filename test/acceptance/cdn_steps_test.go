package acceptance

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/pkg/blob/s3"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// cdnBucket and cdnPrefix place the blobs in MinIO, where the registry
// writes them and the edge reads them.
const (
	cdnBucket = "artifacts"
	cdnPrefix = "art"
	cdnKeyID  = "K2ACCEPTANCE"
)

func (w *world) registerCDN(sc *godog.ScenarioContext) {
	sc.Step(`^the registry is running on S3 behind a CloudFront edge$`, w.startBehindCloudFront)
	sc.Step(`^the CDN edge served at least (\d+) ranged requests?$`, w.edgeServedRanges)
	sc.Step(`^the CDN edge served (\d+) requests?$`, w.edgeServed)
	sc.Step(`^the CDN edge never received an Authorization header$`, w.edgeNoAuthorization)
	sc.Step(`^the CDN edge refuses the last path unsigned$`, w.edgeRefusesUnsigned)
}

// startBehindCloudFront starts MinIO, a fake CloudFront edge whose origin
// is the registry's bucket, and the registry on s3 with that CloudFront.
// The registry listens on 127.0.0.1 and the edge on localhost, so clients
// treat them as different hosts, as they would a real CDN.
func (w *world) startBehindCloudFront() error {
	ctx := context.Background()
	if err := containertest.CheckLinux(ctx); err != nil {
		w.t.Logf("skipping: %v", err)
		return godog.ErrSkip
	}
	minio, stop, err := containertest.RunMinIO(ctx)
	if err != nil {
		return err
	}
	w.closers = append(w.closers, stop)
	client, _, err := s3.NewClient(ctx, s3.ClientConfig{
		Endpoint: minio.URL, Region: "us-east-1", PathStyle: true,
		AccessKey: minio.User, SecretKey: minio.Password,
	})
	if err != nil {
		return err
	}
	if _, err := client.CreateBucket(
		ctx,
		&awss3.CreateBucketInput{Bucket: aws.String(cdnBucket)},
	); err != nil {
		return fmt.Errorf("create bucket: %w", err)
	}
	origin, err := s3.New(client, s3.Options{
		Bucket: cdnBucket, Prefix: cdnPrefix, Sessions: session.NewMemory(),
	})
	if err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	edge, err := cdntest.Start(cdntest.Options{
		Validator: cdntest.CloudFront{Keys: map[string]crypto.PublicKey{cdnKeyID: &key.PublicKey}},
		Origin:    origin,
	})
	if err != nil {
		return err
	}
	w.edge = edge
	w.closers = append(w.closers, func() { _ = edge.Close() })
	keyDir, err := os.MkdirTemp("", "cloudfront-key-")
	if err != nil {
		return err
	}
	w.closers = append(w.closers, func() { _ = os.RemoveAll(keyDir) })
	keyFile := filepath.Join(keyDir, "cloudfront.pem")
	pemKey := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(keyFile, pemKey, 0o600); err != nil {
		return err
	}
	w.env = []string{
		"ARTIFACTWEAVE_BLOB_BACKEND=s3",
		"ARTIFACTWEAVE_BLOB_S3_BUCKET=" + cdnBucket,
		"ARTIFACTWEAVE_BLOB_S3_PREFIX=" + cdnPrefix,
		"ARTIFACTWEAVE_BLOB_S3_ENDPOINT=" + minio.URL,
		"ARTIFACTWEAVE_BLOB_S3_REGION=us-east-1",
		"ARTIFACTWEAVE_BLOB_S3_PATH_STYLE=true",
		"ARTIFACTWEAVE_BLOB_S3_CDN_CLOUDFRONT_DOMAIN=" + edge.URL,
		"ARTIFACTWEAVE_BLOB_S3_CDN_CLOUDFRONT_KEY_PAIR_ID=" + cdnKeyID,
		"ARTIFACTWEAVE_BLOB_S3_CDN_CLOUDFRONT_PRIVATE_KEY_FILE=" + keyFile,
		"AWS_ACCESS_KEY_ID=" + minio.User,
		"AWS_SECRET_ACCESS_KEY=" + minio.Password,
		"AWS_EC2_METADATA_DISABLED=true",
	}
	return w.start()
}

// edgeRequests is what the edge answered, or an error before the edge runs.
func (w *world) edgeRequests() ([]cdntest.Request, error) {
	if w.edge == nil {
		return nil, fmt.Errorf("no CDN edge is running")
	}
	return w.edge.Requests(), nil
}

func (w *world) edgeServedRanges(n int) error {
	reqs, err := w.edgeRequests()
	if err != nil {
		return err
	}
	got := 0
	for _, r := range reqs {
		if r.Range != "" && r.Status == http.StatusPartialContent {
			got++
		}
	}
	if got < n {
		return fmt.Errorf("the edge served %d ranged requests, want at least %d: %+v", got, n, reqs)
	}
	return nil
}

func (w *world) edgeServed(n int) error {
	reqs, err := w.edgeRequests()
	if err != nil {
		return err
	}
	if len(reqs) != n {
		return fmt.Errorf("the edge served %d requests, want %d: %+v", len(reqs), n, reqs)
	}
	return nil
}

func (w *world) edgeNoAuthorization() error {
	reqs, err := w.edgeRequests()
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		return fmt.Errorf("the edge served nothing")
	}
	for _, r := range reqs {
		if r.Authorization {
			return fmt.Errorf(
				"the edge received an Authorization header on %s %s",
				r.Method,
				r.Path,
			)
		}
	}
	return nil
}

// edgeRefusesUnsigned asks the edge for the last blob it served, without a
// signature.
func (w *world) edgeRefusesUnsigned() error {
	reqs, err := w.edgeRequests()
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		return fmt.Errorf("the edge served nothing")
	}
	last := reqs[len(reqs)-1].Path
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		w.edge.URL+last,
		nil,
	)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		return fmt.Errorf("an unsigned GET of %s answered %d, want 403", last, resp.StatusCode)
	}
	return nil
}
