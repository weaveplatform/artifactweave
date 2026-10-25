package mirror_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/api/option"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/azure"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/gcs"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/blob/s3"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Part sizes are the smallest each backend accepts from outside its package
// (S3's 5 MiB floor, GCS's 1 MiB), so a blob of largeSize is several parts
// on every cloud: 3 S3 parts, 11 GCS parts and 41 Azure blocks.
const (
	s3Part    = 5 << 20
	gcsPart   = 1 << 20
	azurePart = 256 << 10
	largeSize = 2*s3Part + 1234
)

// The zones the members sit in; this instance runs in GCP's, so GCS is
// written and read first.
const (
	zoneAWS   = "aws-us-east-1"
	zoneGCP   = "gcp-europe-west4"
	zoneAzure = "azure-westeurope"
)

// TestAccCrossCloud runs a mirror over the three clouds' emulators: S3 on MinIO,
// GCS on fake-gcs-server and Azure on Azurite. The fakes in the package's
// own tests prove the mirror's logic; this proves it against the real
// backends' wiring, where a member's part sizes, sessions and error types
// differ. It needs Docker and skips without it; CI runs it on Linux.
func TestAccCrossCloud(t *testing.T) {
	c := startClouds(t)
	t.Run("contract", func(t *testing.T) {
		blobtest.Run(t, func(t *testing.T) blob.Store { return c.mirror(t).m },
			blobtest.Options{LargeSize: largeSize})
	})
	t.Run("outages", func(t *testing.T) { outages(t, c) })
}

// outages takes one cloud down at a time:
//
//	(a) Azure down: an upload meets the quorum on the other two and Azure's
//	    copy is pending;
//	(b) Azure back: a repair pass copies the blob to Azure;
//	(c) GCS, first in read order, down: reads come from another cloud,
//	    including one that GCS fails part-way through;
//	(d) GCS back: a delete removes the blob from every cloud and the index.
func outages(t *testing.T, c *clouds) {
	ctx := context.Background()
	r := c.mirror(t)
	body := blobtest.Content(42, largeSize)
	sha := blobtest.Digest(body).SHA256

	r.sw["az"].down.Store(true)
	info := blobtest.Put(ctx, t, r.m, body)
	r.idle(t)
	if info.Digests.SHA256 != sha || info.Size != int64(len(body)) {
		t.Fatalf("(a) committed %+v, want %s of %d bytes", info, sha, len(body))
	}
	r.wantStates(t, sha, map[string]types.ReplicaState{
		"aws": types.ReplicaPresent, "gcp": types.ReplicaPresent, "az": types.ReplicaPending,
	})
	if ok, err := r.raw["az"].Exists(ctx, sha); err != nil || ok {
		t.Fatalf("(a) Azure holds the blob it was down for: %v %v", ok, err)
	}

	r.sw["az"].down.Store(false)
	rep, err := r.m.Repair(ctx)
	if err != nil || rep.Copied != 1 || rep.Failed != 0 {
		t.Fatalf("(b) repair: %+v %v", rep, err)
	}
	r.wantStates(t, sha, map[string]types.ReplicaState{
		"aws": types.ReplicaPresent, "gcp": types.ReplicaPresent, "az": types.ReplicaPresent,
	})
	if got := readAll(t, r.raw["az"], sha); !bytes.Equal(got, body) {
		t.Fatalf("(b) Azure's copy differs: %d bytes, want %d", len(got), len(body))
	}

	r.sw["gcp"].down.Store(true)
	if got := readAll(t, r.m, sha); !bytes.Equal(got, body) {
		t.Fatalf("(c) read with GCS down: %d bytes, want %d", len(got), len(body))
	}
	r.sw["gcp"].down.Store(false)
	gcpOpens, otherOpens := r.sw["gcp"].opens.Load(), r.sw["aws"].opens.Load()+r.sw["az"].opens.Load()
	rc, _, err := r.m.Open(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	head := make([]byte, 3*gcsPart+17)
	if _, err := io.ReadFull(rc, head); err != nil {
		t.Fatal(err)
	}
	if r.sw["gcp"].opens.Load() != gcpOpens+1 {
		t.Fatal("(c) GCS is this instance's zone, so it serves the read")
	}
	r.sw["gcp"].down.Store(true)
	tail, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("(c) read after GCS failed part-way: %v", err)
	}
	if !bytes.Equal(append(head, tail...), body) {
		t.Fatalf("(c) failover read %d bytes that differ from the blob", len(head)+len(tail))
	}
	if r.sw["aws"].opens.Load()+r.sw["az"].opens.Load() != otherOpens+1 {
		t.Fatal("(c) the read did not continue on another cloud")
	}

	r.sw["gcp"].down.Store(false)
	if err := r.m.Delete(ctx, sha); err != nil {
		t.Fatalf("(d) delete: %v", err)
	}
	for name, st := range r.raw {
		if ok, err := st.Exists(ctx, sha); err != nil || ok {
			t.Fatalf("(d) %s still holds the blob: %v %v", name, ok, err)
		}
	}
	if recs, err := r.db.Replicas().ForBlob(ctx, sha); err != nil || len(recs) != 0 {
		t.Fatalf("(d) the index still holds replicas: %+v %v", recs, err)
	}
}

// clouds are the three emulators and the admin clients that make a fresh
// bucket or container on each for every mirror.
type clouds struct {
	s3      *awss3.Client
	presign *awss3.PresignClient
	gcsURL  string
	gcs     *storage.Client
	gcsKey  []byte
	az      containertest.AzuriteEndpoint
	azCred  *container.SharedKeyCredential
	n       atomic.Int64
}

func startClouds(t *testing.T) *clouds {
	t.Helper()
	ctx := context.Background()
	c := &clouds{}
	minio := containertest.MinIO(t)
	c.gcsURL = containertest.FakeGCS(t)
	c.az = containertest.Azurite(t)
	var err error
	c.s3, c.presign, err = s3.NewClient(ctx, s3.ClientConfig{
		Endpoint: minio.URL, Region: "us-east-1", PathStyle: true,
		AccessKey: minio.User, SecretKey: minio.Password,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.gcs, err = storage.NewClient(
		ctx,
		option.WithoutAuthentication(),
		option.WithEndpoint(c.gcsURL),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.gcs.Close() })
	c.gcsKey = signingKey(t)
	c.azCred, err = container.NewSharedKeyCredential(c.az.Account, c.az.Key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// rig is one three-cloud mirror: its member stores, the switch in front of
// each, and the metadata store behind it.
type rig struct {
	m  *mirror.Store
	db *inmem.Store
	// raw are the members' own stores, to check what each cloud holds
	// without going through the mirror or the switch.
	raw map[string]blob.Store
	sw  map[string]*outage
}

// mirror builds a mirror on a fresh bucket or container in every cloud, with
// every session, replica record, blob row and lease in one in-memory
// metadata store, as one server's database would hold them.
func (c *clouds) mirror(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	name := "mirror-" + strconv.FormatInt(c.n.Add(1), 10)
	r := &rig{db: inmem.New(), raw: map[string]blob.Store{}, sw: map[string]*outage{}}

	if _, err := c.s3.CreateBucket(
		ctx,
		&awss3.CreateBucketInput{Bucket: aws.String(name)},
	); err != nil {
		t.Fatal(err)
	}
	s3st, err := s3.New(c.s3, s3.Options{
		Bucket: name, Prefix: "art", PartSize: s3Part, Presign: c.presign,
		Sessions: session.InUploads(r.db.Uploads(), "s3:aws"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.gcs.Bucket(name).Create(ctx, "artifactweave", nil); err != nil {
		t.Fatal(err)
	}
	gapi, err := gcs.NewAPI(ctx, name, gcs.ClientConfig{
		Endpoint: c.gcsURL, WithoutAuthentication: true,
		GoogleAccessID: "signer@project.iam.gserviceaccount.com", PrivateKey: c.gcsKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	gcsst, err := gcs.New(gapi, gcs.Options{
		Prefix: "art", PartSize: gcsPart, Presign: true,
		Sessions: session.InUploads(r.db.Uploads(), "gcs:gcp"),
	})
	if err != nil {
		t.Fatal(err)
	}

	cc, err := container.NewClientWithSharedKeyCredential(c.az.ServiceURL+name, c.azCred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.Create(ctx, nil); err != nil {
		t.Fatal(err)
	}
	aapi, err := azure.NewAPI(azure.ClientConfig{
		ServiceURL: c.az.ServiceURL, Container: name,
		Account: c.az.Account, AccountKey: c.az.Key,
	})
	if err != nil {
		t.Fatal(err)
	}
	azst, err := azure.New(aapi, azure.Options{
		Prefix: "art", PartSize: azurePart, CopyRangeSize: 300 << 10, Concurrency: 4,
		Sessions: session.InUploads(r.db.Uploads(), "azure:az"), Presign: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var members []mirror.Member
	for _, m := range []struct {
		name, zone string
		st         blob.Store
	}{{"aws", zoneAWS, s3st}, {"gcp", zoneGCP, gcsst}, {"az", zoneAzure, azst}} {
		r.raw[m.name] = m.st
		r.sw[m.name] = &outage{inner: m.st}
		members = append(members, mirror.Member{
			Name: m.name, Zone: m.zone, Store: r.sw[m.name], WriteEnabled: true,
		})
	}
	// Smaller than the 10 MiB the mirror would derive from these part sizes,
	// so the large blob spans several chunks; New checks it is a multiple of
	// every member's part size, which outage passes through.
	r.m, err = mirror.New(mirror.Options{
		Members: members, Redundancy: 3, WriteQuorum: 2, Zone: zoneGCP,
		ChunkSize: s3Part,
		Sessions:  session.InUploads(r.db.Uploads(), "mirror:crosscloud"),
		Replicas:  r.db.Replicas(), Blobs: r.db.Blobs(), Leases: r.db.Leases(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A member still committing after the quorum must not outlive the test
	// that started it.
	t.Cleanup(func() {
		if err := r.m.WaitIdle(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return r
}

// idle waits for the members still committing after the quorum, so their
// late records cannot race the test's checks.
func (r *rig) idle(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.m.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) wantStates(t *testing.T, sha string, want map[string]types.ReplicaState) {
	t.Helper()
	recs, err := r.db.Replicas().ForBlob(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]types.ReplicaState{}
	for _, rec := range recs {
		got[rec.Member] = rec.State
	}
	if len(got) != len(want) {
		t.Fatalf("replicas %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("replicas %v, want %v", got, want)
		}
	}
}

func readAll(t *testing.T, s blob.Store, sha string) []byte {
	t.Helper()
	rc, _, err := s.Open(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// signingKey is a throwaway service-account key: fake-gcs-server does not
// check signatures, but the GCS store needs a key to sign at all.
func signingKey(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// errDown is what a member answers while its switch is down. It is neither
// a not-found nor a cancellation, so the mirror counts it against the
// member's health, as it would a cloud outage.
var errDown = errors.New("member down")

// outage puts a switch in front of a member store. While down, every call
// fails with errDown, including reads and uploads opened before it went
// down, which is how a read failing part-way through is made.
type outage struct {
	inner blob.Store
	down  atomic.Bool
	// opens counts successful opens, to tell which member served a read.
	opens atomic.Int64
}

func (o *outage) check() error {
	if o.down.Load() {
		return errDown
	}
	return nil
}

func (o *outage) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	u, err := o.inner.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &outageUpload{Upload: u, o: o}, nil
}

func (o *outage) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	u, err := o.inner.ResumeUpload(ctx, id)
	if err != nil {
		return nil, err
	}
	return &outageUpload{Upload: u, o: o}, nil
}

func (o *outage) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if err := o.check(); err != nil {
		return nil, blob.Info{}, err
	}
	rc, info, err := o.inner.Open(ctx, sha)
	if err != nil {
		return nil, blob.Info{}, err
	}
	o.opens.Add(1)
	return &outageReader{ReadSeekCloser: rc, o: o}, info, nil
}

func (o *outage) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := o.check(); err != nil {
		return blob.Info{}, err
	}
	return o.inner.Stat(ctx, sha)
}

func (o *outage) Exists(ctx context.Context, sha string) (bool, error) {
	if err := o.check(); err != nil {
		return false, err
	}
	return o.inner.Exists(ctx, sha)
}

func (o *outage) Delete(ctx context.Context, sha string) error {
	if err := o.check(); err != nil {
		return err
	}
	return o.inner.Delete(ctx, sha)
}

func (o *outage) Walk(ctx context.Context, fn func(blob.Info) error) error {
	if err := o.check(); err != nil {
		return err
	}
	return o.inner.Walk(ctx, fn)
}

// PartSize passes through, so New checks the chunk against the real stores.
func (o *outage) PartSize() int64 { return o.inner.(blob.PartSizer).PartSize() }

// Signs and SignURL pass through: every backend here signs, and without
// them the mirror would report that no member can.
func (o *outage) Signs(via blob.Via) bool { return o.inner.(blob.Signer).Signs(via) }

func (o *outage) SignURL(ctx context.Context, sha string, so blob.SignOptions) (string, error) {
	if err := o.check(); err != nil {
		return "", err
	}
	return o.inner.(blob.Signer).SignURL(ctx, sha, so) //nolint:wrapcheck // a pass-through fake
}

type outageUpload struct {
	blob.Upload
	o *outage
}

func (u *outageUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if err := u.o.check(); err != nil {
		return 0, err
	}
	return u.Upload.Append(ctx, r)
}

func (u *outageUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := u.o.check(); err != nil {
		return blob.Info{}, err
	}
	return u.Upload.Commit(ctx, want)
}

func (u *outageUpload) Abort(ctx context.Context) error {
	if err := u.o.check(); err != nil {
		return err
	}
	return u.Upload.Abort(ctx)
}

type outageReader struct {
	io.ReadSeekCloser
	o *outage
}

func (r *outageReader) Read(p []byte) (int, error) {
	if err := r.o.check(); err != nil {
		return 0, err
	}
	return r.ReadSeekCloser.Read(p)
}

func (r *outageReader) Seek(off int64, whence int) (int64, error) {
	if err := r.o.check(); err != nil {
		return 0, err
	}
	return r.ReadSeekCloser.Seek(off, whence)
}
