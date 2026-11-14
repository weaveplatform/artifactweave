package containertest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/azure/azurite"
	"github.com/testcontainers/testcontainers-go/modules/fakegcsserver"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The emulator images are pinned by digest so an emulator release cannot
// change behaviour under the suites; bump them deliberately, here only.
const (
	minioDigest   = "sha256:dc455cdd4db6e390b1b26514f15ff37681a777bf8dab41aac445a0259f751e56"
	fakeGCSDigest = "sha256:797ce226d62f947c009dc40246b30cfb456b8473d8241407f9d6f2c04e4d69ef"
	azuriteDigest = "sha256:830430c1da1a2d537e08f3e6764dd1f5ae00cf0346bcaf625b968ec3f0971fd5"
)

const (
	// MinIOImage is Chainguard's build of MinIO: MinIO stopped publishing
	// minio/minio on Docker Hub and its quay.io images need credentials.
	// This digest carries RELEASE.2026-09-22T19-25-18Z of
	// github.com/chainguard-forks/minio (`minio --version`), the release
	// RunMinIO's readiness wait was checked against; recheck that wait on a
	// bump.
	MinIOImage = "cgr.dev/chainguard/minio@" + minioDigest
	// FakeGCSImage is fake-gcs-server 1.56.1.
	FakeGCSImage = "fsouza/fake-gcs-server:1.56.1@" + fakeGCSDigest
	// AzuriteImage is Azurite 3.37.0, the first release with Put Block From
	// URL. Its baseline API version is older than the one the Azure SDK
	// sends, so it runs with --skipApiVersionCheck (research 18).
	AzuriteImage = "mcr.microsoft.com/azure-storage/azurite@" + azuriteDigest
)

// azuriteCopy is the project's own copy of AzuriteImage, tagged with its
// Azurite release. .github/workflows/emulator-copy.yml makes it with crane
// copy, which keeps the index digest. The workflow reads these lines,
// AzuriteImage and azuriteDigest, and runs whenever this file changes, so a
// bump copies the new digest in the same change. It fails when the tag
// already serves another digest: move the tag with the digest.
const (
	azuriteCopy    = "ghcr.io/weaveplatform/artifactweave-emulators/azurite"
	azuriteCopyTag = "3.37.0"
)

// Each emulator's sources, in the order they are tried: its image, then a
// second registry serving the same digest, so one registry that does not
// answer does not fail the run (#1321, #1448). A pull by digest is checked
// against that digest, so every source gives the same bytes. On a bump,
// check that MinIO's and fake-gcs-server's second sources serve the new
// digest, for example with `docker buildx imagetools inspect <source>`;
// Azurite's is copied by a workflow (azuriteCopy).
var (
	// Chainguard publishes the same image on Docker Hub.
	minioSources = []string{MinIOImage, "docker.io/chainguard/minio@" + minioDigest}
	// mirror.gcr.io is Google's cache of Docker Hub; it serves only the
	// images it holds, which is why a bump must check it.
	fakeGCSSources = []string{
		FakeGCSImage, "mirror.gcr.io/fsouza/fake-gcs-server@" + fakeGCSDigest,
	}
	// Microsoft publishes Azurite only on MCR, and Docker Hub, mirror.gcr.io
	// and ghcr.io held no copy (checked 2026-11-13), so the second source is
	// the project's own copy (#1463).
	azuriteSources = []string{
		AzuriteImage, azuriteCopy + ":" + azuriteCopyTag + "@" + azuriteDigest,
	}
)

// Each helper starts its own container for the calling test and removes it
// when the test ends. Sharing one container per test binary would tie its
// lifetime to whichever test started it first; every suite uses each
// emulator from a single test, so nothing is gained by sharing.

// MinIOEndpoint addresses a MinIO server.
type MinIOEndpoint struct {
	// URL is the S3 endpoint, for example "http://127.0.0.1:32768"; MinIO
	// needs path-style addressing.
	URL      string
	User     string
	Password string
}

// MinIO starts MinIO for t, or skips t where Docker cannot run Linux
// containers.
func MinIO(t *testing.T) MinIOEndpoint {
	t.Helper()
	RequireLinux(t)
	ep, stop, err := RunMinIO(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ep
}

// RunMinIO starts MinIO and returns its endpoint and a function that
// removes it, for callers without a *testing.T such as acceptance steps;
// check CheckLinux first. It returns once MinIO serves S3 requests.
func RunMinIO(ctx context.Context) (MinIOEndpoint, func(), error) {
	image, err := requireImage(ctx, minioSources...)
	if err != nil {
		return MinIOEndpoint{}, nil, fmt.Errorf("start minio: %w", err)
	}
	// The module waits only for /minio/health/live, which answers 200 as soon
	// as MinIO serves HTTP; /minio/health/ready does the same. Until the
	// object layer exists, S3 calls answer 503 XMinioServerNotInitialized,
	// and a CreateBucket in that window failed CI twice (#1307).
	// /minio/health/cluster answers 503 until the object layer, bucket
	// metadata and IAM are all initialised (cmd/healthcheck-handler.go,
	// checkHealth, at the release MinIOImage carries).
	ctr, err := tcminio.Run(ctx, image, testcontainers.WithAdditionalWaitStrategy(
		wait.ForHTTP("/minio/health/cluster").WithPort("9000"),
	))
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return MinIOEndpoint{}, nil, fmt.Errorf("start minio: %w", err)
	}
	endpoint, err := ctr.ConnectionString(ctx)
	if err != nil {
		stop()
		return MinIOEndpoint{}, nil, fmt.Errorf("minio endpoint: %w", err)
	}
	return MinIOEndpoint{URL: "http://" + endpoint, User: ctr.Username, Password: ctr.Password},
		stop, nil
}

// FakeGCS starts fake-gcs-server for t and returns its JSON API endpoint,
// ending in "/storage/v1/" as the Cloud Storage client's option.WithEndpoint
// needs. It skips t where Docker cannot run Linux containers.
func FakeGCS(t *testing.T) string {
	t.Helper()
	RequireLinux(t)
	endpoint, stop, err := RunFakeGCS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return endpoint
}

// RunFakeGCS starts fake-gcs-server and returns its JSON API endpoint, as
// FakeGCS does, and a function that removes it, for callers without a
// *testing.T such as acceptance steps; check CheckLinux first.
func RunFakeGCS(ctx context.Context) (string, func(), error) {
	image, err := requireImage(ctx, fakeGCSSources...)
	if err != nil {
		return "", nil, fmt.Errorf("start fake-gcs-server: %w", err)
	}
	ctr, err := fakegcsserver.Run(ctx, image)
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("start fake-gcs-server: %w", err)
	}
	endpoint, err := ctr.StorageURL(ctx)
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("fake-gcs-server endpoint: %w", err)
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	if !strings.HasSuffix(endpoint, "/storage/v1") {
		endpoint += "/storage/v1"
	}
	return endpoint + "/", stop, nil
}

// AzuriteEndpoint addresses Azurite's blob service.
type AzuriteEndpoint struct {
	// ServiceURL is the account URL with a trailing slash. Its host is an IP
	// address: the Azure SDK treats a non-IP host as virtual-hosted style,
	// and Azurite is path style.
	ServiceURL string
	Account    string
	Key        string
}

// Azurite starts Azurite's blob service for t, or skips t where Docker
// cannot run Linux containers.
func Azurite(t *testing.T) AzuriteEndpoint {
	t.Helper()
	RequireLinux(t)
	ep, stop, err := RunAzurite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ep
}

// RunAzurite starts Azurite's blob service and returns its endpoint and a
// function that removes it, for callers without a *testing.T such as
// acceptance steps; check CheckLinux first.
func RunAzurite(ctx context.Context) (AzuriteEndpoint, func(), error) {
	return runAzurite(ctx, azuriteSources)
}

// runAzurite is RunAzurite from the given sources, so a test can start
// Azurite from its second source alone.
func runAzurite(ctx context.Context, sources []string) (AzuriteEndpoint, func(), error) {
	image, err := requireImage(ctx, sources...)
	if err != nil {
		return AzuriteEndpoint{}, nil, fmt.Errorf("start azurite: %w", err)
	}
	ctr, err := azurite.Run(ctx, image,
		azurite.WithEnabledServices(azurite.BlobService),
		testcontainers.WithCmdArgs("--skipApiVersionCheck"),
	)
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return AzuriteEndpoint{}, nil, fmt.Errorf("start azurite: %w", err)
	}
	endpoint, err := ctr.BlobServiceURL(ctx)
	if err != nil {
		stop()
		return AzuriteEndpoint{}, nil, fmt.Errorf("azurite endpoint: %w", err)
	}
	return AzuriteEndpoint{
		ServiceURL: strings.Replace(endpoint, "localhost", "127.0.0.1", 1) +
			"/" + azurite.AccountName + "/",
		Account: azurite.AccountName,
		Key:     azurite.AccountKey,
	}, stop, nil
}
