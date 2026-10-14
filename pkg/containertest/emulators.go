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
)

// The emulator images are pinned by digest so an emulator release cannot
// change behaviour under the suites; bump them deliberately, here only.
const (
	// MinIOImage is Chainguard's build of MinIO: MinIO stopped publishing
	// minio/minio on Docker Hub and its quay.io images need credentials.
	MinIOImage = "cgr.dev/chainguard/minio@sha256:" +
		"dc455cdd4db6e390b1b26514f15ff37681a777bf8dab41aac445a0259f751e56"
	// FakeGCSImage is fake-gcs-server 1.56.1.
	FakeGCSImage = "fsouza/fake-gcs-server:1.56.1@sha256:" +
		"797ce226d62f947c009dc40246b30cfb456b8473d8241407f9d6f2c04e4d69ef"
	// AzuriteImage is Azurite 3.37.0, the first release with Put Block From
	// URL. Its baseline API version is older than the one the Azure SDK
	// sends, so it runs with --skipApiVersionCheck (research 18).
	AzuriteImage = "mcr.microsoft.com/azure-storage/azurite@sha256:" +
		"830430c1da1a2d537e08f3e6764dd1f5ae00cf0346bcaf625b968ec3f0971fd5"
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
// check CheckLinux first.
func RunMinIO(ctx context.Context) (MinIOEndpoint, func(), error) {
	ctr, err := tcminio.Run(ctx, MinIOImage)
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
	ctx := context.Background()
	ctr, err := fakegcsserver.Run(ctx, FakeGCSImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start fake-gcs-server: %v", err)
	}
	endpoint, err := ctr.StorageURL(ctx)
	if err != nil {
		t.Fatalf("fake-gcs-server endpoint: %v", err)
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	if !strings.HasSuffix(endpoint, "/storage/v1") {
		endpoint += "/storage/v1"
	}
	return endpoint + "/"
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
	ctx := context.Background()
	ctr, err := azurite.Run(ctx, AzuriteImage,
		azurite.WithEnabledServices(azurite.BlobService),
		testcontainers.WithCmdArgs("--skipApiVersionCheck"),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start azurite: %v", err)
	}
	endpoint, err := ctr.BlobServiceURL(ctx)
	if err != nil {
		t.Fatalf("azurite endpoint: %v", err)
	}
	return AzuriteEndpoint{
		ServiceURL: strings.Replace(endpoint, "localhost", "127.0.0.1", 1) +
			"/" + azurite.AccountName + "/",
		Account: azurite.AccountName,
		Key:     azurite.AccountKey,
	}
}
