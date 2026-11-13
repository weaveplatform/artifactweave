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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/usage"
	tfbe "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/session"
	"github.com/weaveplatform/artifactweave/internal/platform/cdn"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/workers/core/model"
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
	az := func(cred string, set func(*config.BlobAzure)) config.BlobBackend {
		a := config.BlobAzure{Account: "acct", Container: "c", Credential: cred}
		set(&a)
		return config.BlobBackend{Backend: "azure", Azure: a}
	}
	none := func(*config.BlobAzure) {}
	for name, b := range map[string]config.BlobBackend{
		"fs": {Backend: "fs"},
		"s3": {Backend: "s3", S3: config.BlobS3{
			Bucket: "b", Region: "us-east-1", Endpoint: "http://127.0.0.1:9", PathStyle: true,
		}},
		"s3 profile": {Backend: "s3", S3: config.BlobS3{Bucket: "b", Profile: "other"}},
		"gcs": {Backend: "gcs", GCS: config.BlobGCS{
			Bucket: "b", CredentialsFile: gcsKeyFile(t), PartSize: 2 << 20,
		}},
		"azure shared key": az(config.AzureSharedKey,
			func(a *config.BlobAzure) { a.AccountKey = "AAAA" }),
		"azure sas": az(config.AzureSAS,
			func(a *config.BlobAzure) { a.SASToken = "sv=2024&sig=x" }),
		"azure connection string": az(config.AzureConnectionString, func(a *config.BlobAzure) {
			a.ConnectionString = "DefaultEndpointsProtocol=https;AccountName=acct;" +
				"AccountKey=AAAA;EndpointSuffix=core.windows.net"
		}),
		"azure default":           az(config.AzureDefault, none),
		"azure managed identity":  az(config.AzureManagedIdentity, func(a *config.BlobAzure) { a.ManagedIdentityClientID = "id" }),
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
		{"storage and cdn", signer{fsStore, []blob.Via{blob.ViaStorage, blob.ViaCDN}}, nil},
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
type failingRepos struct{ *inmem.Store }

func (f failingRepos) Repos() port.RepoStore { return failingRepoList{f.Store.Repos()} }

type failingRepoList struct{ port.RepoStore }

func (failingRepoList) List(context.Context, port.RepoFilter) ([]repo.Config, error) {
	return nil, errors.New("listing failed")
}

func TestAccOpenBackendErrors(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]config.BlobBackend{
		"fs root under a file": {Backend: "fs"},
		"s3 unknown profile": {Backend: "s3", S3: config.BlobS3{
			Bucket: "b", Profile: "missing",
		}},
		"gcs missing key file": {Backend: "gcs", GCS: config.BlobGCS{
			Bucket: "b", CredentialsFile: filepath.Join(t.TempDir(), "absent.json"),
		}},
		"gcs bad part size": {Backend: "gcs", GCS: config.BlobGCS{
			Bucket: "b", CredentialsFile: gcsKeyFile(t), PartSize: 1000,
		}},
		"azure workload identity without token": {Backend: "azure", Azure: config.BlobAzure{
			Account: "acct", Container: "c", Credential: config.AzureWorkloadIdentity,
		}},
		"azure no container": {Backend: "azure", Azure: config.BlobAzure{
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
func mirrorConfig() config.Blob {
	off := false
	return config.Blob{
		BlobBackend: config.BlobBackend{Backend: "mirror"},
		Stores: map[string]config.BlobStore{
			"near": {BlobBackend: config.BlobBackend{Backend: "fs"}, Zone: "home"},
			"far":  {BlobBackend: config.BlobBackend{Backend: "fs"}, Zone: "away"},
			"drained": {
				BlobBackend:  config.BlobBackend{Backend: "fs"},
				WriteEnabled: &off,
			},
		},
		Mirror: config.BlobMirror{Members: []string{"near", "far", "drained"}, Zone: "home"},
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
	for name, tune := range map[string]func(*config.Blob){
		"undeclared member": func(b *config.Blob) { b.Mirror.Members = append(b.Mirror.Members, "ghost") },
		"member fails": func(b *config.Blob) {
			b.Stores["far"] = config.BlobStore{BlobBackend: config.BlobBackend{Backend: "tape"}}
		},
		"quorum unreachable": func(b *config.Blob) { b.Mirror.WriteQuorum = 3 },
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
	s3With := func(cf config.CloudFront) config.BlobBackend {
		return config.BlobBackend{Backend: "s3", S3: config.BlobS3{
			Bucket: "b", Prefix: "art", Region: "us-east-1", Endpoint: "http://127.0.0.1:9",
			PathStyle: true, CDN: config.BlobS3CDN{CloudFront: cf},
		}}
	}
	good := config.CloudFront{
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
	for name, cf := range map[string]config.CloudFront{
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
	cf := config.BlobBackend{Backend: "s3", S3: config.BlobS3{
		CDN: config.BlobS3CDN{CloudFront: config.CloudFront{Domain: "d.example"}},
	}}
	plain := config.BlobBackend{Backend: "s3"}
	for name, c := range map[string]struct {
		blob     config.Blob
		expected int64
	}{
		"s3 with CloudFront": {config.Blob{BlobBackend: cf}, cdn.CloudFrontMaxSize},
		"s3 alone":           {config.Blob{BlobBackend: plain}, 0},
		"fs":                 {config.Blob{BlobBackend: config.BlobBackend{Backend: "fs"}}, 0},
		"a mirror member with CloudFront": {config.Blob{
			BlobBackend: config.BlobBackend{Backend: "mirror"},
			Stores: map[string]config.BlobStore{
				"a": {BlobBackend: plain}, "b": {BlobBackend: cf},
			},
			Mirror: config.BlobMirror{Members: []string{"a", "b"}},
		}, cdn.CloudFrontMaxSize},
		"CloudFront on a store the mirror leaves out": {config.Blob{
			BlobBackend: config.BlobBackend{Backend: "mirror"},
			Stores: map[string]config.BlobStore{
				"a": {BlobBackend: plain}, "b": {BlobBackend: cf},
			},
			Mirror: config.BlobMirror{Members: []string{"a"}},
		}, 0},
	} {
		if got := cdnMaxSize(c.blob); got != c.expected {
			t.Errorf("%s: %d, want %d", name, got, c.expected)
		}
	}
}

func TestAccOpenBackendWithFrontDoor(t *testing.T) {
	azWith := func(fd config.FrontDoor) config.BlobBackend {
		return config.BlobBackend{Backend: "azure", Azure: config.BlobAzure{
			Account: "acct", Container: "c", Prefix: "art", Credential: config.AzureSharedKey,
			AccountKey: "AAAA", CDN: config.BlobAzureCDN{FrontDoor: fd},
		}}
	}
	good := config.FrontDoor{Endpoint: "artifacts-x1.z01.azurefd.net"}
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
	for name, fd := range map[string]config.FrontDoor{
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
	gcsWith := func(cc config.CloudCDN) config.BlobBackend {
		return config.BlobBackend{Backend: "gcs", GCS: config.BlobGCS{
			Bucket: "b", Prefix: "art", CredentialsFile: gcsKeyFile(t),
			CDN: config.BlobGCSCDN{CloudCDN: cc},
		}}
	}
	good := config.CloudCDN{
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
	for name, cc := range map[string]config.CloudCDN{
		"missing key file": missing, "not a key": badKey, "bad key name": badName,
		"origin outside the prefix": outside,
	} {
		_, err := openBackend(context.Background(), "blob", gcsWith(cc), session.NewMemory(), "")
		if !errors.Is(err, errBlobStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

const (
	keyOne = "0123456789abcdef0123456789abcdef"
	keyTwo = "fedcba9876543210fedcba9876543210"
)

func TestAccKeyring(t *testing.T) {
	cfg := config.Default()
	if k, err := Keyring(cfg); k != nil || err != nil {
		t.Fatalf("no key = %v, %v", k, err)
	}
	cfg.MasterKey = keyOne
	k, err := Keyring(cfg)
	if err != nil || k.Active() != 1 {
		t.Fatalf("one key = %v, %v", k, err)
	}
	cfg.MasterKeyPrevious, cfg.MasterKey = keyOne, "v2:"+keyTwo
	k, err = Keyring(cfg)
	if err != nil || k.Active() != 2 || len(k.Versions()) != 2 {
		t.Fatalf("rotation pair = %v, %v", k, err)
	}
	cfg.MasterKeyPrevious = "short"
	if _, err := Keyring(cfg); !errors.Is(err, crypt.ErrWeakKey) {
		t.Fatalf("weak previous key: %v", err)
	}
	cfg.MasterKeyPrevious, cfg.MasterKey = keyOne, keyTwo
	if _, err := Keyring(cfg); !errors.Is(err, crypt.ErrBadVersion) {
		t.Fatalf("two version-1 keys: %v", err)
	}
}

func TestAccMigrateCommand(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "nested", "data")
	var out bytes.Buffer
	run := func(action string, to int, module string) string {
		t.Helper()
		out.Reset()
		if err := Migrate(ctx, cfg, action, to, module, &out); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return out.String()
	}
	if got := run("version", 0, ""); !strings.HasPrefix(got, "schema version 0 of ") {
		t.Fatalf("fresh version: %q", got)
	}
	if _, err := os.Stat(cfg.SQLitePath()); err != nil {
		t.Fatalf("database file not created under the data directory: %v", err)
	}
	if got := run(
		"up",
		0,
		"",
	); !strings.HasPrefix(got, "applied ") ||
		strings.HasPrefix(got, "applied 0") {
		t.Fatalf("up: %q", got)
	}
	if got := run("up", 0, ""); !strings.HasPrefix(got, "applied 0 ") {
		t.Fatalf("second up: %q", got)
	}
	got := run("down", -1, "")
	if !strings.HasPrefix(got, "reverted 1 ") {
		t.Fatalf("down one step: %q", got)
	}
	// One step back is the newest migration of the last stream in the
	// canonical module order.
	if got != "reverted 1 migration(s): [workers:1]\n" {
		t.Fatalf("down one step reverted %q; want workers:1", got)
	}
	if got := run("down", 0, ""); strings.HasPrefix(got, "reverted 0 ") {
		t.Fatalf("down to 0: %q", got)
	}
	if got := run("version", 0, ""); !strings.HasPrefix(got, "schema version 0 of ") {
		t.Fatalf("after down: %q", got)
	}
	if err := Migrate(ctx, cfg, "sideways", 0, "", &out); !errors.Is(err, ErrUnknownMigration) {
		t.Fatalf("unknown action: %v", err)
	}

	// --module limits each action to one stream.
	const applied = "applied 2 migration(s): [artifacts:1 artifacts:2]\n"
	if got := run("up", 0, "artifacts"); got != applied {
		t.Fatalf("up --module artifacts: %q", got)
	}
	if got := run("version", 0, "artifacts"); got != "schema version 2 of 2 (sqlite, artifacts)\n" {
		t.Fatalf("version --module artifacts: %q", got)
	}
	want := "schema version 0 of 1 (sqlite, platform)\n" +
		"schema version 2 of 2 (sqlite, artifacts)\n"
	for _, m := range []string{"access", "notifications", "retention", "federation", "releases", "workers"} {
		want += "schema version 0 of 1 (sqlite, " + m + ")\n"
	}
	if got := run("version", 0, ""); got != want {
		t.Fatalf("version of every stream: %q; want %q", got, want)
	}
	if got := run("down", -1, "platform"); got != "reverted 0 migration(s): []\n" {
		t.Fatalf("down --module platform with nothing applied: %q", got)
	}
	if got := run("down", -1, "artifacts"); got != "reverted 1 migration(s): [artifacts:2]\n" {
		t.Fatalf("down --module artifacts: %q", got)
	}
	for _, action := range []string{"up", "down", "version"} {
		err := Migrate(ctx, cfg, action, -1, "nope", &out)
		want := "migrate " + action + `: sqlcommon: migration: unknown module "nope"`
		if !errors.Is(err, dialect.ErrUnknownModule) || err.Error() != want {
			t.Fatalf("%s --module nope: %v; want %s", action, err, want)
		}
	}
	err := Migrate(ctx, cfg, "down", 2, "", &out)
	if !errors.Is(err, dialect.ErrModuleRequired) ||
		err.Error() != "migrate down: sqlcommon: migration: a target version other than 0 needs a module" {
		t.Fatalf("down --to 2 without a module: %v", err)
	}
}

func TestAccOpenStoreErrors(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://%zz"
	if _, err := OpenStore(ctx, cfg, appsql.Options{}); err == nil {
		t.Fatal("a malformed PostgreSQL DSN opened")
	}
	if err := Migrate(ctx, cfg, "up", 0, "", &bytes.Buffer{}); err == nil {
		t.Fatal("migrate against an unopenable store succeeded")
	}
	// A data directory that is a file cannot hold the database.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = config.Default()
	cfg.DataDir = filepath.Join(file, "data")
	if _, err := OpenStore(ctx, cfg, appsql.Options{}); err == nil {
		t.Fatal("a data directory under a file opened")
	}
	cfg.DataDir = file
	if _, err := OpenStore(ctx, cfg, appsql.Options{}); err == nil {
		t.Fatal("a database path that is not a file opened")
	}
}

func TestAccRotateKeys(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	var out bytes.Buffer
	if err := RotateKeys(ctx, cfg, &out); !errors.Is(err, config.ErrMissingKey) {
		t.Fatalf("no master key: %v", err)
	}
	cfg.MasterKey = keyOne
	k, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(ctx, cfg, appsql.Options{Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Workers().
		Create(ctx, model.Worker{Key: "w", Action: "BEFORE_DOWNLOAD"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Workers().PutSecret(ctx, "w", "token", "secret"); err != nil {
		t.Fatal(err)
	}
	tf := writeState(t, cfg, s, k)
	_ = s.Close()

	cfg.MasterKeyPrevious, cfg.MasterKey = keyOne, "v2:"+keyTwo
	if err := RotateKeys(ctx, cfg, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "re-sealed 1 value(s) under key version 2") ||
		!strings.Contains(got, "re-sealed 1 Terraform state(s) under key version 2") ||
		!strings.Contains(got, "remove master_key_previous") {
		t.Fatalf("output: %q", got)
	}
	cfg.MasterKeyPrevious = ""
	k, err = Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(ctx, cfg, appsql.Options{Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got, err := s.Workers().Secrets(ctx, "w"); err != nil || got["token"] != "secret" {
		t.Fatalf("after rotation = %v, %v", got, err)
	}
	if got := readState(t, cfg, s, k, tf); got != tfState {
		t.Fatalf("after rotation the state reads %q", got)
	}

	cfg.MasterKey = "short"
	if err := RotateKeys(ctx, cfg, &out); !errors.Is(err, crypt.ErrWeakKey) {
		t.Fatalf("weak key: %v", err)
	}
	cfg.MasterKey = keyOne
	cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://%zz"
	if err := RotateKeys(ctx, cfg, &out); err == nil {
		t.Fatal("rotation against an unopenable store succeeded")
	}
}

// TestAccOptimizeRunsRepairNow checks that the optimize trigger reaches the
// repair loop: with the interval an hour away, only the trigger can start
// the logged pass.
func TestAccOptimizeRunsRepairNow(t *testing.T) {
	orig := repairEvery
	repairEvery = time.Hour
	t.Cleanup(func() { repairEvery = orig })
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := testConfig(t)
	cfg.Blob = mirrorConfig()
	ctx := context.Background()
	rt, err := OpenRuntime(ctx, cfg, log, RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	admin := rt.mirrorsAdmin()
	if admin == nil {
		t.Fatal("a mirror tier has no admin")
	}
	st, err := admin.Status(ctx)
	if err != nil || len(st) != 1 || st[0].Tier != "blob" || len(st[0].Members) != 3 ||
		st[0].Members[0].Kind != "fs" {
		t.Fatalf("status %+v, %v", st, err)
	}
	// A second optimize while the first pass is queued must not block.
	admin.Optimize()
	admin.Optimize()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(),
		"blob mirror repair: nothing to do"); {
		if time.Now().After(deadline) {
			t.Fatalf("optimize started no repair pass:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// The store is closed, so the status queries fail.
	if _, err := admin.Status(ctx); err == nil {
		t.Fatal("status after close succeeded")
	}
}

func TestAccNoMirrorNoAdmin(t *testing.T) {
	ctx := context.Background()
	rt, err := OpenRuntime(ctx, testConfig(t), slog.New(slog.DiscardHandler), RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if rt.mirrorsAdmin() != nil {
		t.Fatal("an fs-only runtime reports mirrors")
	}
}

const tfState = `{"version":4,"serial":1,"lineage":"l"}`

// stateService is the Terraform state service over cfg's hot blob store.
func stateService(
	t *testing.T,
	cfg config.Config,
	s *appsql.Store,
	k *crypt.Keyring,
) *terraformstate.Service {
	t.Helper()
	hot, _ := tiers(cfg, s)
	opened, err := hot.open(context.Background(), s, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return &terraformstate.Service{
		Items: &item.Service{Store: s, Blobs: opened.store, Formats: formatSettings(cfg, k).For},
		Keys:  k,
	}
}

// writeState writes tfState, sealed under k, to workspace app of a new
// Terraform backend repository, and returns the repository.
func writeState(t *testing.T, cfg config.Config, s *appsql.Store, k *crypt.Keyring) repo.Config {
	t.Helper()
	ctx := context.Background()
	c := *repo.Defaults(&repo.Config{Key: "tf", Class: repo.Local, PackageType: repo.TerraformBackend})
	if err := s.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	svc := stateService(t, cfg, s, k)
	if _, err := svc.CreateWorkspace(ctx, c, "app", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteState(ctx, c, "app", terraformstate.StateWrite{
		Body: strings.NewReader(tfState), MD5: tfbe.MD5([]byte(tfState)), Serial: 1, By: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// readState reads workspace app's current state in c under k.
func readState(
	t *testing.T,
	cfg config.Config,
	s *appsql.Store,
	k *crypt.Keyring,
	c repo.Config,
) string {
	t.Helper()
	ctx := context.Background()
	svc := stateService(t, cfg, s, k)
	cur, err := svc.CurrentState(ctx, c, "app")
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := svc.ReadState(ctx, c, "app", cur.Ms)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

// A configuration without a hot blob store holds no states to reseal.
func TestAccResealStatesWithoutBlobs(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.MasterKey = keyOne
	k, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(ctx, cfg, appsql.Options{Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cfg.Blob.Backend = ""
	if rep, err := resealStates(ctx, cfg, s, k); err != nil || rep.Resealed != 0 {
		t.Fatalf("resealStates without blobs = %+v, %v", rep, err)
	}
}

func TestAccLocation(t *testing.T) {
	cfg := testConfig(t)
	nearDir := filepath.Join(cfg.DataDir, "blob-stores", "near")
	be := func(b config.BlobBackend) config.Config {
		c := cfg
		c.Blob = config.Blob{BlobBackend: b}
		return c
	}
	for name, tc := range map[string]struct {
		cfg              config.Config
		kind, dir, local string
	}{
		"fs": {
			cfg:  be(config.BlobBackend{Backend: "fs"}),
			kind: "file-system", dir: cfg.BlobRoot(), local: cfg.BlobRoot(),
		},
		"s3": {cfg: be(config.BlobBackend{
			Backend: "s3",
			S3:      config.BlobS3{Bucket: "b", Prefix: "p"},
		}), kind: "s3-storage-v3-direct", dir: usage.NoFilesystem},
		"gcs": {cfg: be(config.BlobBackend{
			Backend: "gcs",
			GCS:     config.BlobGCS{Bucket: "b"},
		}), kind: "google-storage-v2-direct", dir: usage.NoFilesystem},
		"azure": {
			cfg: be(config.BlobBackend{
				Backend: "azure",
				Azure:   config.BlobAzure{Account: "a", Container: "c", Prefix: "p"},
			}),
			kind: "azure-blob-storage-v2-direct", dir: usage.NoFilesystem,
		},
		// A mirror reports its first fs member's directory and volume, as
		// 6.20 does a chain's first file-system provider (#490).
		"mirror": {
			cfg:  func() config.Config { c := cfg; c.Blob = mirrorConfig(); return c }(),
			kind: "user-chain", dir: nearDir, local: nearDir,
		},
		"mirror after an object store": {
			cfg: func() config.Config {
				c := cfg
				c.Blob = mirrorConfig()
				c.Blob.Stores = maps.Clone(c.Blob.Stores)
				c.Blob.Stores["cloud"] = config.BlobStore{BlobBackend: config.BlobBackend{
					Backend: "s3", S3: config.BlobS3{Bucket: "b"},
				}}
				far := c.Blob.Stores["far"]
				far.FS.Root = "/srv/far"
				c.Blob.Stores["far"] = far
				c.Blob.Mirror.Members = []string{"cloud", "far", "near"}
				return c
			}(),
			kind: "user-chain", dir: "/srv/far", local: "/srv/far",
		},
		"mirror of object stores": {
			cfg: func() config.Config {
				c := cfg
				c.Blob = config.Blob{
					BlobBackend: config.BlobBackend{Backend: "mirror"},
					Stores: map[string]config.BlobStore{"cloud": {
						BlobBackend: config.BlobBackend{
							Backend: "s3", S3: config.BlobS3{Bucket: "b"},
						},
					}},
					Mirror: config.BlobMirror{Members: []string{"cloud", "unknown"}},
				}
				return c
			}(),
			kind: "user-chain", dir: usage.NoFilesystem,
		},
		"none": {cfg: be(config.BlobBackend{})},
	} {
		hot, _ := tiers(tc.cfg, inmem.New())
		got := hot.location()
		if got.Type != tc.kind || got.Directory != tc.dir || got.Root != tc.local {
			t.Errorf("%s: %+v", name, got)
		}
	}
}
