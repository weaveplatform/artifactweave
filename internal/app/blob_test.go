package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

func TestAccOpenBackend(t *testing.T) {
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
			continue
		}
		// Every object store signs storage URLs except Azure on a SAS token;
		// local disk signs nothing.
		sg, ok := s.(blob.Signer)
		want := name != "fs" && name != "azure sas"
		if got := ok && sg.Signs(blob.ViaStorage); got != want {
			t.Errorf("%s: signs storage URLs %v, want %v", name, got, want)
		}
	}
}

// signer signs the routes it lists.
type signer struct {
	blob.Store
	routes []blob.Via
}

func (s signer) Signs(via blob.Via) bool { return slices.Contains(s.routes, via) }

func (signer) SignURL(context.Context, string, blob.SignOptions) (string, error) { return "", nil }

func TestAccLogSigning(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "plain", Class: repo.Local},
		{Key: "download", Class: repo.Local, DownloadRedirect: repo.Flex(true)},
		{Key: "cdn", Class: repo.Local, CDNRedirect: repo.Flex(true)},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fsStore, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		blobs blob.Store
		warn  []string // repositories warned about
	}{
		{"local disk", fsStore, []string{"download", "cdn"}},
		{"storage only", signer{fsStore, []blob.Via{blob.ViaStorage}}, []string{"cdn"}},
		{"storage and CDN", signer{fsStore, []blob.Via{blob.ViaStorage, blob.ViaCDN}}, nil},
	} {
		var logs bytes.Buffer
		logSigning(ctx, slog.New(slog.NewTextHandler(&logs, nil)), db, c.blobs)
		if n := strings.Count(logs.String(), "level=WARN"); n != len(c.warn) {
			t.Errorf("%s: %d warnings, want %d:\n%s", c.name, n, len(c.warn), logs.String())
		}
		for _, key := range c.warn {
			if !strings.Contains(logs.String(), "repo="+key) {
				t.Errorf("%s: no warning for %s:\n%s", c.name, key, logs.String())
			}
		}
		if !strings.Contains(logs.String(), "download redirect routes") {
			t.Errorf("%s: no summary:\n%s", c.name, logs.String())
		}
	}
	var logs bytes.Buffer
	logSigning(ctx, slog.New(slog.NewTextHandler(&logs, nil)), failingRepos{db}, fsStore)
	if !strings.Contains(logs.String(), "listing repositories") {
		t.Fatalf("a listing failure is logged:\n%s", logs.String())
	}
}

// failingRepos is a store whose repository listing fails.
type failingRepos struct{ store.Store }

func (f failingRepos) Repos() store.RepoStore { return failingRepoList{f.Store.Repos()} }

type failingRepoList struct{ store.RepoStore }

func (failingRepoList) List(context.Context, store.RepoFilter) ([]repo.Config, error) {
	return nil, errors.New("listing failed")
}

func TestAccOpenBackendErrors(t *testing.T) {
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

func TestAccMirrorTier(t *testing.T) {
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

func TestAccMirrorTierErrors(t *testing.T) {
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

func TestAccTierDefaults(t *testing.T) {
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

func TestAccLogRepair(t *testing.T) {
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

// cloudFrontKey writes a PKCS#1 RSA key file and returns its path.
func cloudFrontKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "cloudfront.pem")
	pemKey := pem.EncodeToMemory(
		&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)},
	)
	if err := os.WriteFile(file, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestAccOpenBackendWithCloudFront(t *testing.T) {
	isolateAWS(t)
	s3With := func(cf config.CloudFrontConfig) config.BlobBackendConfig {
		return config.BlobBackendConfig{Backend: "s3", S3: config.BlobS3Config{
			Bucket: "b", Prefix: "art", Region: "us-east-1", Endpoint: "http://127.0.0.1:9",
			PathStyle: true, CDN: config.BlobS3CDNConfig{CloudFront: cf},
		}}
	}
	good := config.CloudFrontConfig{
		Domain: "d111.cloudfront.net", KeyPairID: "K1", PrivateKeyFile: cloudFrontKey(t),
		PathPrefix: "art",
	}
	s, err := openBackend(context.Background(), "blob", s3With(good), session.NewMemory(), "")
	if err != nil {
		t.Fatal(err)
	}
	sg, ok := s.(blob.Signer)
	if !ok || !sg.Signs(blob.ViaCDN) || !sg.Signs(blob.ViaStorage) {
		t.Fatal("an s3 store with CloudFront signs both routes")
	}
	sha := strings.Repeat("ab", 32)
	u, err := sg.SignURL(
		context.Background(),
		sha,
		blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute},
	)
	if err != nil ||
		!strings.HasPrefix(u, "https://d111.cloudfront.net/sha256/ab/"+sha+"?Expires=") {
		t.Fatalf("%q %v", u, err)
	}
	missing, badKey, outside := good, good, good
	missing.PrivateKeyFile = filepath.Join(t.TempDir(), "absent.pem")
	badKey.PrivateKeyFile = filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(badKey.PrivateKeyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside.PathPrefix = "elsewhere"
	for name, cf := range map[string]config.CloudFrontConfig{
		"missing key file": missing, "not a key": badKey, "origin outside the prefix": outside,
	} {
		_, err := openBackend(context.Background(), "blob", s3With(cf), session.NewMemory(), "")
		if !errors.Is(err, errBlobStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A store with CloudFront, alone or as any mirror member, limits what goes
// to the CDN to what CloudFront caches; other CDNs set no limit.
func TestAccCDNMaxSize(t *testing.T) {
	cf := config.BlobBackendConfig{Backend: "s3", S3: config.BlobS3Config{
		CDN: config.BlobS3CDNConfig{CloudFront: config.CloudFrontConfig{Domain: "d.example"}},
	}}
	plain := config.BlobBackendConfig{Backend: "s3"}
	for name, c := range map[string]struct {
		blob config.BlobConfig
		want int64
	}{
		"s3 with CloudFront": {config.BlobConfig{BlobBackendConfig: cf}, cdn.CloudFrontMaxSize},
		"s3 alone":           {config.BlobConfig{BlobBackendConfig: plain}, 0},
		"fs":                 {config.BlobConfig{BlobBackendConfig: config.BlobBackendConfig{Backend: "fs"}}, 0},
		"a mirror member with CloudFront": {config.BlobConfig{
			BlobBackendConfig: config.BlobBackendConfig{Backend: "mirror"},
			Stores: map[string]config.BlobStoreConfig{
				"a": {BlobBackendConfig: plain}, "b": {BlobBackendConfig: cf},
			},
			Mirror: config.BlobMirrorConfig{Members: []string{"a", "b"}},
		}, cdn.CloudFrontMaxSize},
		"CloudFront on a store the mirror leaves out": {config.BlobConfig{
			BlobBackendConfig: config.BlobBackendConfig{Backend: "mirror"},
			Stores: map[string]config.BlobStoreConfig{
				"a": {BlobBackendConfig: plain}, "b": {BlobBackendConfig: cf},
			},
			Mirror: config.BlobMirrorConfig{Members: []string{"a"}},
		}, 0},
	} {
		if got := cdnMaxSize(c.blob); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestAccOpenBackendWithFrontDoor(t *testing.T) {
	azWith := func(fd config.FrontDoorConfig) config.BlobBackendConfig {
		return config.BlobBackendConfig{Backend: "azure", Azure: config.BlobAzureConfig{
			Account: "acct", Container: "c", Prefix: "art", Credential: config.AzureSharedKey,
			AccountKey: "AAAA", CDN: config.BlobAzureCDNConfig{FrontDoor: fd},
		}}
	}
	good := config.FrontDoorConfig{Endpoint: "artifacts-x1.z01.azurefd.net"}
	s, err := openBackend(context.Background(), "blob", azWith(good), session.NewMemory(), "")
	if err != nil {
		t.Fatal(err)
	}
	sg, ok := s.(blob.Signer)
	if !ok || !sg.Signs(blob.ViaCDN) || !sg.Signs(blob.ViaStorage) {
		t.Fatal("an azure store with a Front Door signs both routes")
	}
	sha := strings.Repeat("ab", 32)
	u, err := sg.SignURL(
		context.Background(),
		sha,
		blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute},
	)
	if err != nil ||
		!strings.HasPrefix(u, "https://artifacts-x1.z01.azurefd.net/c/art/sha256/ab/"+sha+"?") {
		t.Fatalf("%q %v", u, err)
	}
	for name, fd := range map[string]config.FrontDoorConfig{
		"bad endpoint":              {Endpoint: "ftp://x"},
		"origin outside the prefix": {Endpoint: "fd.azurefd.net", PathPrefix: "other"},
	} {
		_, err := openBackend(context.Background(), "blob", azWith(fd), session.NewMemory(), "")
		if !errors.Is(err, errBlobStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// cloudCDNKeyFile writes a base64url Cloud CDN key file and returns its path.
func cloudCDNKeyFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "cloud-cdn.key")
	key := base64.URLEncoding.EncodeToString([]byte("0123456789abcdef"))
	if err := os.WriteFile(file, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestAccOpenBackendWithCloudCDN(t *testing.T) {
	gcsWith := func(cc config.CloudCDNConfig) config.BlobBackendConfig {
		return config.BlobBackendConfig{Backend: "gcs", GCS: config.BlobGCSConfig{
			Bucket: "b", Prefix: "art", CredentialsFile: gcsKeyFile(t),
			CDN: config.BlobGCSCDNConfig{CloudCDN: cc},
		}}
	}
	good := config.CloudCDNConfig{
		Domain: "cdn.example.com", KeyName: "key-1", KeyFile: cloudCDNKeyFile(t),
		PathPrefix: "art",
	}
	s, err := openBackend(context.Background(), "blob", gcsWith(good), session.NewMemory(), "")
	if err != nil {
		t.Fatal(err)
	}
	sg, ok := s.(blob.Signer)
	if !ok || !sg.Signs(blob.ViaCDN) || !sg.Signs(blob.ViaStorage) {
		t.Fatal("a gcs store with a key and Cloud CDN signs both routes")
	}
	sha := strings.Repeat("ab", 32)
	u, err := sg.SignURL(
		context.Background(),
		sha,
		blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute},
	)
	if err != nil ||
		!strings.HasPrefix(u, "https://cdn.example.com/sha256/ab/"+sha+"?Expires=") ||
		!strings.Contains(u, "&KeyName=key-1&Signature=") {
		t.Fatalf("%q %v", u, err)
	}
	missing, badKey, badName, outside := good, good, good, good
	missing.KeyFile = filepath.Join(t.TempDir(), "absent.key")
	badKey.KeyFile = filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(badKey.KeyFile, []byte("not a key!"), 0o600); err != nil {
		t.Fatal(err)
	}
	badName.KeyName = "Key 1"
	outside.PathPrefix = "elsewhere"
	for name, cc := range map[string]config.CloudCDNConfig{
		"missing key file": missing, "not a key": badKey, "bad key name": badName,
		"origin outside the prefix": outside,
	} {
		_, err := openBackend(context.Background(), "blob", gcsWith(cc), session.NewMemory(), "")
		if !errors.Is(err, errBlobStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
