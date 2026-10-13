package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// isolateAWS points the AWS SDK at a shared configuration holding only the
// profile "other", so the developer's own ~/.aws cannot change the outcome.
func isolateAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(
		cfg,
		[]byte("[profile other]\nregion = eu-west-1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
}

// gcsKeyFile writes a service-account key with a real RSA key, which the
// Cloud Storage client parses when it is built.
func gcsKeyFile(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "p", "private_key_id": "k",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "aw@p.iam.gserviceaccount.com", "client_id": "1",
		"token_uri": "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenBackend(t *testing.T) {
	isolateAWS(t)
	// A real token file lets the workload identity credential build; it
	// makes no request until a token is needed.
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_TENANT_ID", "tenant")
	t.Setenv("AZURE_CLIENT_ID", "client")
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", token)
	az := func(cred string, set func(*config.BlobAzureConfig)) config.BlobBackendConfig {
		a := config.BlobAzureConfig{Account: "acct", Container: "c", Credential: cred}
		set(&a)
		return config.BlobBackendConfig{Backend: "azure", Azure: a}
	}
	none := func(*config.BlobAzureConfig) {}
	for name, b := range map[string]config.BlobBackendConfig{
		"fs": {Backend: "fs"},
		"s3": {Backend: "s3", S3: config.BlobS3Config{
			Bucket: "b", Region: "us-east-1", Endpoint: "http://127.0.0.1:9", PathStyle: true,
		}},
		"s3 profile": {Backend: "s3", S3: config.BlobS3Config{Bucket: "b", Profile: "other"}},
		"gcs": {Backend: "gcs", GCS: config.BlobGCSConfig{
			Bucket: "b", CredentialsFile: gcsKeyFile(t), PartSize: 2 << 20,
		}},
		"azure shared key": az(config.AzureSharedKey,
			func(a *config.BlobAzureConfig) { a.AccountKey = "AAAA" }),
		"azure sas": az(config.AzureSAS,
			func(a *config.BlobAzureConfig) { a.SASToken = "sv=2024&sig=x" }),
		"azure connection string": az(config.AzureConnectionString, func(a *config.BlobAzureConfig) {
			a.ConnectionString = "DefaultEndpointsProtocol=https;AccountName=acct;" +
				"AccountKey=AAAA;EndpointSuffix=core.windows.net"
		}),
		"azure default":           az(config.AzureDefault, none),
		"azure managed identity":  az(config.AzureManagedIdentity, func(a *config.BlobAzureConfig) { a.ManagedIdentityClientID = "id" }),
		"azure workload identity": az(config.AzureWorkloadIdentity, none),
	} {
		s, err := openBackend(context.Background(), "blob", b, session.NewMemory(), t.TempDir())
		if err != nil || s == nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOpenBackendErrors(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]config.BlobBackendConfig{
		"fs root under a file": {Backend: "fs"},
		"s3 unknown profile": {Backend: "s3", S3: config.BlobS3Config{
			Bucket: "b", Profile: "missing",
		}},
		"gcs missing key file": {Backend: "gcs", GCS: config.BlobGCSConfig{
			Bucket: "b", CredentialsFile: filepath.Join(t.TempDir(), "absent.json"),
		}},
		"gcs bad part size": {Backend: "gcs", GCS: config.BlobGCSConfig{
			Bucket: "b", CredentialsFile: gcsKeyFile(t), PartSize: 1000,
		}},
		"azure workload identity without token": {Backend: "azure", Azure: config.BlobAzureConfig{
			Account: "acct", Container: "c", Credential: config.AzureWorkloadIdentity,
		}},
		"azure no container": {Backend: "azure", Azure: config.BlobAzureConfig{
			Account: "acct", AccountKey: "AAAA",
		}},
		"unknown": {Backend: "tape"},
	} {
		_, err := openBackend(context.Background(), "blob", b, session.NewMemory(),
			filepath.Join(file, "blobs"))
		if !errors.Is(err, errBlobStore) || !strings.Contains(err.Error(), "blob ("+b.Backend+")") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// mirrorConfig declares three fs stores in three zones and mirrors over
// them; "drained" accepts no new copies.
func mirrorConfig() config.BlobConfig {
	off := false
	return config.BlobConfig{
		BlobBackendConfig: config.BlobBackendConfig{Backend: "mirror"},
		Stores: map[string]config.BlobStoreConfig{
			"near": {BlobBackendConfig: config.BlobBackendConfig{Backend: "fs"}, Zone: "home"},
			"far":  {BlobBackendConfig: config.BlobBackendConfig{Backend: "fs"}, Zone: "away"},
			"drained": {
				BlobBackendConfig: config.BlobBackendConfig{Backend: "fs"},
				WriteEnabled:      &off,
			},
		},
		Mirror: config.BlobMirrorConfig{Members: []string{"near", "far", "drained"}, Zone: "home"},
	}
}

func TestMirrorTier(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.Blob = mirrorConfig()
	db := inmem.New()
	hot, _ := tiers(cfg, db)
	opened, err := hot.open(ctx, db, slog.Default())
	if err != nil || opened.mirror == nil || opened.store != opened.mirror {
		t.Fatalf("open = %+v, %v", opened, err)
	}
	info := blobtest.Put(ctx, t, opened.store, []byte("mirrored"))
	if err := opened.mirror.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	// Each member writes its own directory under blob-stores; the drained
	// member gets no copy.
	for name, want := range map[string]bool{"near": true, "far": true, "drained": false} {
		dir, err := fs.New(filepath.Join(cfg.DataDir, "blob-stores", name))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := dir.Exists(ctx, info.Digests.SHA256); err != nil || got != want {
			t.Errorf("%s holds the blob: %v (%v), want %v", name, got, err, want)
		}
	}
	recs, err := db.Replicas().ForBlob(ctx, info.Digests.SHA256)
	if err != nil || len(recs) != 2 {
		t.Fatalf("replica records %v, %v", recs, err)
	}
}

func TestMirrorTierErrors(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for name, tune := range map[string]func(*config.BlobConfig){
		"undeclared member": func(b *config.BlobConfig) { b.Mirror.Members = append(b.Mirror.Members, "ghost") },
		"member fails": func(b *config.BlobConfig) {
			b.Stores["far"] = config.BlobStoreConfig{BlobBackendConfig: config.BlobBackendConfig{Backend: "tape"}}
		},
		"quorum unreachable": func(b *config.BlobConfig) { b.Mirror.WriteQuorum = 3 },
	} {
		cfg := testConfig(t)
		cfg.Blob = mirrorConfig()
		tune(&cfg.Blob)
		hot, _ := tiers(cfg, db)
		if _, err := hot.open(ctx, db, slog.Default()); !errors.Is(err, errBlobStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTierDefaults(t *testing.T) {
	cfg := testConfig(t)
	hot, cold := tiers(cfg, inmem.New())
	if hot.root != filepath.Join(cfg.DataDir, "blobs") || hot.lease == cold.lease ||
		cold.root != filepath.Join(cfg.DataDir, "cold-blobs") ||
		cold.storesDir != filepath.Join(cfg.DataDir, "cold-blob-stores") {
		t.Fatalf("hot %+v cold %+v", hot, cold)
	}
	cfg.ColdBlob.FS.Root = "/srv/cold"
	if _, cold := tiers(cfg, inmem.New()); cold.root != "/srv/cold" {
		t.Fatalf("explicit cold root ignored: %s", cold.root)
	}
	// No cold tier configured builds nothing.
	if opened, err := cold.open(context.Background(), inmem.New(), slog.Default()); err != nil ||
		opened.store != nil {
		t.Fatalf("empty cold tier = %+v, %v", opened, err)
	}
}

func TestLogRepair(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := logRepair(log, "blob")
	f(mirror.RepairReport{}, errors.New("boom"))
	f(mirror.RepairReport{Skipped: true}, nil)
	f(mirror.RepairReport{}, nil)
	f(mirror.RepairReport{Copied: 2}, nil)
	for _, want := range []string{
		"level=WARN msg=\"blob mirror repair failed\"",
		"another pass holds the lease",
		"nothing to do",
		"level=INFO msg=\"blob mirror repair\" tier=blob copied=2",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}
