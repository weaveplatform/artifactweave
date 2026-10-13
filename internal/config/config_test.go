package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "artifactweave.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Default(), cfg); diff != "" {
		t.Fatal(diff)
	}
	if cfg.DatabaseDSN() != "file:"+filepath.Join("data", "artifactweave.db") {
		t.Fatalf("dsn %q", cfg.DatabaseDSN())
	}
	if cfg.BlobRoot() != filepath.Join("data", "blobs") {
		t.Fatalf("blob root %q", cfg.BlobRoot())
	}
}

func TestPrecedenceFlagOverEnvOverFile(t *testing.T) {
	file := writeFile(t, "listen: ':1'\ndata_dir: /file\nlog:\n  level: debug\n")
	cfg, err := Load(LoadOptions{
		File: file,
		Env: envOf(map[string]string{
			"ARTIFACTWEAVE_LISTEN":   ":2",
			"ARTIFACTWEAVE_DATA_DIR": "/env",
		}),
		Flags: map[string]string{"listen": ":3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":3" || cfg.DataDir != "/env" || cfg.Log.Level != "debug" {
		t.Fatalf("precedence wrong: %+v", cfg)
	}
}

func TestFileCoversEveryKey(t *testing.T) {
	file := writeFile(t, `
listen: ":9000"
base_url: https://repo.example.com
data_dir: /srv/aw
compat:
  artifactory_version: 7.160.1
database:
  driver: postgres
  dsn: postgres://u:p@db/aw
blob:
  backend: s3
  fs:
    root: /ignored
  s3:
    bucket: b
    prefix: p
    endpoint: http://minio:9000
    region: eu-west-1
    path_style: true
  gcs:
    bucket: g
    prefix: gp
    endpoint: http://gcs:4443
    credentials_file: /srv/sa.json
    google_access_id: aw@p.iam.gserviceaccount.com
    part_size: 32MiB
  azure:
    account: acct
    endpoint: https://acct.blob.core.windows.net/
    container: c
    prefix: ap
    credential: managed_identity
    account_key: ak
    sas_token: st
    connection_string: cs
    managed_identity_client_id: mi
    part_size: 16MiB
    copy_range_size: 64MiB
    concurrency: 4
cold_blob:
  backend: gcs
  gcs:
    bucket: archive
master_key: k
master_key_previous: kp
bootstrap:
  admin_password: pw
  descriptor: /srv/desc.yaml
docker:
  subdomain_match: true
log:
  level: warn
  format: text
`)
	cfg, err := Load(LoadOptions{File: file})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: ":9000", BaseURL: "https://repo.example.com", DataDir: "/srv/aw",
		Compat:   CompatConfig{ArtifactoryVersion: "7.160.1"},
		Database: DatabaseConfig{Driver: "postgres", DSN: "postgres://u:p@db/aw"},
		Blob: BlobConfig{Backend: "s3", FS: BlobFSConfig{Root: "/ignored"}, S3: BlobS3Config{
			Bucket:    "b",
			Prefix:    "p",
			Endpoint:  "http://minio:9000",
			Region:    "eu-west-1",
			PathStyle: true,
		}, GCS: BlobGCSConfig{
			Bucket: "g", Prefix: "gp", Endpoint: "http://gcs:4443", CredentialsFile: "/srv/sa.json",
			GoogleAccessID: "aw@p.iam.gserviceaccount.com", PartSize: 32 << 20,
		}, Azure: BlobAzureConfig{
			Account: "acct", Endpoint: "https://acct.blob.core.windows.net/", Container: "c",
			Prefix: "ap", Credential: "managed_identity", AccountKey: "ak", SASToken: "st",
			ConnectionString: "cs", ManagedIdentityClientID: "mi", PartSize: 16 << 20,
			CopyRangeSize: 64 << 20, Concurrency: 4,
		}},
		ColdBlob:  BlobConfig{Backend: "gcs", GCS: BlobGCSConfig{Bucket: "archive"}},
		MasterKey: "k", MasterKeyPrevious: "kp",
		Bootstrap: BootstrapConfig{AdminPassword: "pw", Descriptor: "/srv/desc.yaml"},
		Docker:    DockerConfig{SubdomainMatch: true},
		Log:       LogConfig{Level: "warn", Format: "text"},
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Fatal(diff)
	}
	if cfg.DatabaseDSN() != "postgres://u:p@db/aw" || cfg.BlobRoot() != "/ignored" {
		t.Fatalf("explicit values not honoured: %q %q", cfg.DatabaseDSN(), cfg.BlobRoot())
	}
}

func TestEnvCoversEveryKey(t *testing.T) {
	env := map[string]string{}
	for _, k := range keys {
		switch {
		case strings.Contains(k, "listen"):
			env[envName(k)] = ":1"
		case strings.HasSuffix(k, "_size"):
			env[envName(k)] = "8MiB"
		case strings.HasSuffix(k, ".concurrency"), strings.HasSuffix(k, ".redundancy"),
			strings.HasSuffix(k, ".write_quorum"):
			env[envName(k)] = "3"
		case strings.HasSuffix(k, ".member_timeout"), strings.HasPrefix(k, "gc."):
			env[envName(k)] = "45s"
		default:
			// String keys accept "true" as a plain string; the booleans parse it.
			env[envName(k)] = "true"
		}
	}
	cfg := Default()
	for _, k := range keys {
		if err := set(&cfg, k, env[envName(k)]); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	if !cfg.Docker.SubdomainMatch || !cfg.Blob.S3.PathStyle || cfg.Blob.S3.Bucket != "true" ||
		cfg.Blob.GCS.PartSize != 8<<20 || cfg.ColdBlob.Azure.Concurrency != 3 ||
		cfg.ColdBlob.Azure.AccountKey != "true" || cfg.GC.Interval != 45*time.Second ||
		cfg.GC.GracePeriod != 45*time.Second {
		t.Fatalf("env keys not applied: %+v", cfg)
	}
}

func TestUnknownFileKeyIsAnError(t *testing.T) {
	file := writeFile(t, "listen: ':1'\nlisten_addr: ':2'\n")
	_, err := Load(LoadOptions{File: file})
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(LoadOptions{File: filepath.Join(t.TempDir(), "nope.yaml")})
	if err == nil || !strings.Contains(err.Error(), "open config") {
		t.Fatalf("want open error, got %v", err)
	}
}

func TestBadValues(t *testing.T) {
	cases := []struct {
		name string
		opts LoadOptions
		want error
	}{
		{"unknown flag key", LoadOptions{Flags: map[string]string{"nope": "x"}}, ErrUnknownKey},
		{
			"bad bool env",
			LoadOptions{
				Env: envOf(map[string]string{"ARTIFACTWEAVE_DOCKER_SUBDOMAIN_MATCH": "yes"}),
			},
			ErrInvalidValue,
		},
		{
			"bad bool flag",
			LoadOptions{Flags: map[string]string{"blob.s3.path_style": "maybe"}},
			ErrInvalidValue,
		},
		{"bad listen", LoadOptions{Flags: map[string]string{"listen": "8081"}}, ErrBadListen},
		{
			"bad base url",
			LoadOptions{Flags: map[string]string{"base_url": "repo.example.com"}},
			ErrBadBaseURL,
		},
		{
			"ftp base url",
			LoadOptions{Flags: map[string]string{"base_url": "ftp://x"}},
			ErrBadBaseURL,
		},
		{
			"unknown driver",
			LoadOptions{Flags: map[string]string{"database.driver": "mysql"}},
			ErrUnknownDriver,
		},
		{
			"postgres without dsn",
			LoadOptions{Flags: map[string]string{"database.driver": "postgres"}},
			ErrMissingDSN,
		},
		{
			"unknown blob",
			LoadOptions{Flags: map[string]string{"blob.backend": "ftp"}},
			ErrUnknownBlob,
		},
		{"empty blob", LoadOptions{Flags: map[string]string{"blob.backend": ""}}, ErrUnknownBlob},
		{
			"unknown cold blob",
			LoadOptions{Flags: map[string]string{"cold_blob.backend": "tape"}},
			ErrUnknownBlob,
		},
		{
			"gcs without bucket",
			LoadOptions{Flags: map[string]string{"blob.backend": "gcs"}},
			ErrMissingBucket,
		},
		{
			"gcs part size not aligned",
			LoadOptions{Flags: map[string]string{
				"blob.backend": "gcs", "blob.gcs.bucket": "b", "blob.gcs.part_size": "1000000",
			}},
			ErrInvalidValue,
		},
		{"azure without container", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.account": "a",
		}}, ErrMissingValue},
		{"azure without account", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c",
		}}, ErrMissingValue},
		{"azure shared key without key", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c", "blob.azure.account": "a",
			"blob.azure.credential": "shared_key",
		}}, ErrMissingValue},
		{"azure sas without token", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c", "blob.azure.account": "a",
			"blob.azure.credential": "sas",
		}}, ErrMissingValue},
		{"azure connection string missing", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c",
			"blob.azure.credential": "connection_string",
		}}, ErrMissingValue},
		{"azure unknown credential", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c", "blob.azure.account": "a",
			"blob.azure.credential": "password",
		}}, ErrInvalidValue},
		{"azure part too large", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c", "blob.azure.account": "a",
			"blob.azure.part_size": "5GiB",
		}}, ErrInvalidValue},
		{"azure copy range too large", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c", "blob.azure.account": "a",
			"blob.azure.copy_range_size": "5GiB",
		}}, ErrInvalidValue},
		{"azure negative concurrency", LoadOptions{Flags: map[string]string{
			"blob.backend": "azure", "blob.azure.container": "c", "blob.azure.account": "a",
			"blob.azure.concurrency": "-1",
		}}, ErrInvalidValue},
		{
			"bad size",
			LoadOptions{Flags: map[string]string{"blob.gcs.part_size": "lots"}},
			ErrInvalidValue,
		},
		{
			"bad concurrency",
			LoadOptions{Flags: map[string]string{"blob.azure.concurrency": "many"}},
			ErrInvalidValue,
		},
		{
			"unknown blob key",
			LoadOptions{Flags: map[string]string{"blob.gcs.region": "x"}},
			ErrUnknownKey,
		},
		{
			"s3 without bucket",
			LoadOptions{Flags: map[string]string{"blob.backend": "s3"}},
			ErrMissingBucket,
		},
		{
			"bad log level",
			LoadOptions{Flags: map[string]string{"log.level": "trace"}},
			ErrInvalidValue,
		},
		{
			"bad log format",
			LoadOptions{Flags: map[string]string{"log.format": "xml"}},
			ErrInvalidValue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.opts)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateJoinsErrors(t *testing.T) {
	cfg := Default()
	cfg.Listen = "bad"
	cfg.Log.Level = "bad"
	err := cfg.Validate()
	if !errors.Is(err, ErrBadListen) || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("want both errors, got %v", err)
	}
}

func TestRequireMasterKey(t *testing.T) {
	cfg := Default()
	if !errors.Is(cfg.RequireMasterKey(), ErrMissingKey) {
		t.Fatal("empty key should fail")
	}
	cfg.MasterKey = "k"
	if err := cfg.RequireMasterKey(); err != nil {
		t.Fatal(err)
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	cfg := Default()
	cfg.MasterKey = "topsecret"
	cfg.Bootstrap.AdminPassword = "hunter2"
	cfg.Database.DSN = "postgres://user:hunter3@db:5432/aw?sslmode=disable"
	cfg.Blob.Azure.AccountKey = "azkey4"
	cfg.ColdBlob.Azure.SASToken = "sastoken5"
	cfg.ColdBlob.Azure.ConnectionString = "connstr6"
	s := cfg.String()
	for _, secret := range []string{"topsecret", "hunter2", "hunter3", "azkey4", "sastoken5", "connstr6"} {
		if strings.Contains(s, secret) {
			t.Fatalf("summary leaks %q:\n%s", secret, s)
		}
	}
	if !strings.Contains(s, "user:redacted@db:5432") || !strings.Contains(s, "<redacted>") {
		t.Fatalf("summary missing redaction markers:\n%s", s)
	}
	empty := Default().String()
	if strings.Contains(empty, "redacted") {
		t.Fatalf("empty secrets should stay empty:\n%s", empty)
	}
}

func TestRedactDSNPassthrough(t *testing.T) {
	for _, dsn := range []string{"", "file:x.db", "postgres://db/aw", "host=db user=u password=p", "postgres://u@db/aw"} {
		if got := redactDSN(dsn); got != dsn {
			t.Errorf("redactDSN(%q) = %q", dsn, got)
		}
	}
}

func TestOSEnv(t *testing.T) {
	t.Setenv("ARTIFACTWEAVE_TEST_KEY", "v")
	if v, ok := OSEnv("ARTIFACTWEAVE_TEST_KEY"); !ok || v != "v" {
		t.Fatalf("OSEnv = %q, %v", v, ok)
	}
}

func TestSQLitePath(t *testing.T) {
	cfg := Default()
	cfg.DataDir = "/srv/aw"
	if got := cfg.SQLitePath(); got != filepath.Join("/srv/aw", "artifactweave.db") {
		t.Fatalf("default = %q", got)
	}
	cfg.Database.DSN = "file:/tmp/x.db"
	if got := cfg.SQLitePath(); got != "/tmp/x.db" {
		t.Fatalf("file DSN = %q", got)
	}
}

func TestAzureCredentialDefaults(t *testing.T) {
	for want, a := range map[string]BlobAzureConfig{
		AzureConnectionString: {ConnectionString: "x"},
		AzureSharedKey:        {AccountKey: "x"},
		AzureSAS:              {SASToken: "x"},
		AzureDefault:          {},
		AzureWorkloadIdentity: {Credential: AzureWorkloadIdentity, AccountKey: "ignored"},
	} {
		if got := a.EffectiveCredential(); got != want {
			t.Errorf("%+v: credential %q, want %q", a, got, want)
		}
	}
	ok := BlobConfig{Backend: "azure", Azure: BlobAzureConfig{
		Container: "c", ConnectionString: "UseThis",
	}}
	if errs := ok.validate("blob", true); len(errs) != 0 {
		t.Fatalf("connection string alone is enough: %v", errs)
	}
	ok.Azure = BlobAzureConfig{Container: "c", Account: "a", Credential: AzureManagedIdentity}
	if errs := ok.validate("blob", true); len(errs) != 0 {
		t.Fatalf("managed identity with an account: %v", errs)
	}
	if errs := (BlobConfig{}).validate("cold_blob", false); len(errs) != 0 {
		t.Fatalf("an unconfigured archive tier is valid: %v", errs)
	}
}

func TestByteSize(t *testing.T) {
	for in, want := range map[string]ByteSize{
		"0": 0, "1024": 1024, "512KiB": 512 << 10, "16MiB": 16 << 20, "2GiB": 2 << 30,
		" 8 MiB ": 8 << 20, "100B": 100,
	} {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "1.5MiB", "MiB", "16TB", "9999999999GiB"} {
		if _, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) accepted", bad)
		}
	}
	for size, want := range map[ByteSize]string{
		0: "0", 100: "100", 1 << 10: "1KiB", 3 << 20: "3MiB", 1 << 30: "1GiB", (1 << 20) + 1: "1048577",
	} {
		if got := size.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", size, got, want)
		}
	}
	var out struct {
		Size ByteSize `yaml:"size"`
	}
	if err := yaml.Unmarshal([]byte("size: 4MiB\n"), &out); err != nil || out.Size != 4<<20 {
		t.Fatalf("yaml = %d, %v", out.Size, err)
	}
	if err := yaml.Unmarshal([]byte("size: huge\n"), &out); err == nil {
		t.Fatal("yaml accepted a bad size")
	}
	b, err := yaml.Marshal(out)
	if err != nil || strings.TrimSpace(string(b)) != "size: 4MiB" {
		t.Fatalf("marshal = %q, %v", b, err)
	}
}

// mirrorFile is a three-cloud mirror: two S3 regions and an Azure account.
const mirrorFile = `
blob:
  backend: mirror
  stores:
    aws_use1:
      backend: s3
      zone: us-east-1
      s3:
        bucket: aw-use1
        region: us-east-1
        profile: prod
    aws_euw1:
      backend: s3
      zone: eu-west-1
      s3:
        bucket: aw-euw1
        region: eu-west-1
    azure_weu:
      backend: azure
      zone: westeurope
      write_enabled: false
      azure:
        account: awstore
        container: blobs
        account_key: secretkey7
  mirror:
    members: [aws_use1, aws_euw1, azure_weu]
    zone: eu-west-1
    member_timeout: 45s
`

func TestMirrorFromFileAndEnv(t *testing.T) {
	file := writeFile(t, mirrorFile)
	cfg, err := Load(LoadOptions{File: file, Env: envOf(map[string]string{
		"ARTIFACTWEAVE_BLOB_STORES_AWS_EUW1_S3_BUCKET":  "aw-euw1-renamed",
		"ARTIFACTWEAVE_BLOB_STORES_AZURE_WEU_ACCOUNT":   "ignored", // not a key
		"ARTIFACTWEAVE_BLOB_MIRROR_WRITE_QUORUM":        "2",
		"ARTIFACTWEAVE_BLOB_STORES_AWS_USE1_S3_PROFILE": "other",
	})})
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Blob
	if b.Backend != "mirror" || len(b.Stores) != 3 || b.Mirror.Zone != "eu-west-1" ||
		b.Mirror.MemberTimeout != 45*time.Second {
		t.Fatalf("mirror = %+v", b)
	}
	if b.Stores["aws_euw1"].S3.Bucket != "aw-euw1-renamed" ||
		b.Stores["aws_use1"].S3.Profile != "other" {
		t.Fatalf("environment did not reach the declared stores: %+v", b.Stores)
	}
	if !b.Stores["aws_use1"].Writable() || b.Stores["azure_weu"].Writable() {
		t.Fatal("write_enabled defaults to true and false drains")
	}
	if r, w := b.Mirror.Effective(); r != 3 || w != 2 {
		t.Fatalf("Effective = %d, %d", r, w)
	}
	if s := cfg.String(); strings.Contains(s, "secretkey7") {
		t.Fatalf("store secret leaked:\n%s", s)
	}
	// Flags can declare a store and list members as comma-separated names.
	cfg, err = Load(LoadOptions{File: file, Flags: map[string]string{
		"blob.stores.local.backend": "fs",
		"blob.mirror.members":       "aws_use1, local,aws_euw1",
		"blob.mirror.redundancy":    "2",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Blob.Mirror.Members; len(got) != 3 || got[1] != "local" {
		t.Fatalf("members = %v", got)
	}
	if r, w := cfg.Blob.Mirror.Effective(); r != 2 || w != 1 {
		t.Fatalf("Effective = %d, %d", r, w)
	}
}

func TestMirrorValidation(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"blob.backend":          "mirror",
			"blob.stores.a.backend": "fs",
			"blob.stores.b.backend": "fs",
			"blob.stores.c.backend": "fs",
			"blob.mirror.members":   "a,b,c",
		}
	}
	cases := map[string]struct {
		set  map[string]string
		want error
	}{
		"no members": {map[string]string{"blob.mirror.members": ""}, ErrMissingValue},
		"unknown member": {
			map[string]string{"blob.mirror.members": "a,b,z"},
			ErrInvalidValue,
		},
		"duplicate member": {
			map[string]string{"blob.mirror.members": "a,a,b"},
			ErrInvalidValue,
		},
		"bad store name": {
			map[string]string{"blob.stores.Bad.backend": "fs"},
			ErrInvalidValue,
		},
		"store without backend": {map[string]string{"blob.stores.d.zone": "x"}, ErrUnknownBlob},
		"nested mirror": {
			map[string]string{"blob.stores.d.backend": "mirror"},
			ErrUnknownBlob,
		},
		"store misconfigured": {
			map[string]string{"blob.stores.d.backend": "s3"},
			ErrMissingBucket,
		},
		"redundancy too high": {
			map[string]string{"blob.mirror.redundancy": "4"},
			ErrInvalidValue,
		},
		"quorum too high": {
			map[string]string{"blob.mirror.redundancy": "2", "blob.mirror.write_quorum": "3"},
			ErrInvalidValue,
		},
		"too few writable": {map[string]string{
			"blob.stores.b.write_enabled": "false", "blob.stores.c.write_enabled": "false",
			"blob.mirror.write_quorum": "2",
		}, ErrInvalidValue},
		"negative timeout": {
			map[string]string{"blob.mirror.member_timeout": "-1s"},
			ErrInvalidValue,
		},
		"bad timeout": {
			map[string]string{"blob.mirror.member_timeout": "soon"},
			ErrInvalidValue,
		},
		"bad redundancy": {map[string]string{"blob.mirror.redundancy": "two"}, ErrInvalidValue},
		"bad write flag": {
			map[string]string{"blob.stores.a.write_enabled": "maybe"},
			ErrInvalidValue,
		},
		"store key shape":   {map[string]string{"blob.stores.a": "fs"}, ErrUnknownKey},
		"unknown store key": {map[string]string{"blob.stores.a.region": "x"}, ErrUnknownKey},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			flags := base()
			for k, v := range tc.set {
				flags[k] = v
			}
			if _, err := Load(LoadOptions{Flags: flags}); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	if _, err := Load(LoadOptions{Flags: base()}); err != nil {
		t.Fatalf("a valid mirror: %v", err)
	}
	cold := base()
	for k, v := range base() {
		delete(cold, k)
		cold["cold_"+k] = v
	}
	if _, err := Load(LoadOptions{Flags: cold}); err != nil {
		t.Fatalf("a mirrored archive tier: %v", err)
	}
}

func TestGCConfig(t *testing.T) {
	cfg := Default()
	if err := set(&cfg, "gc.grace_period", "an hour"); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("bad duration: %v", err)
	}
	cfg.GC.Interval = -time.Second
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("negative interval: %v", err)
	}
	file := writeFile(t, "gc:\n  interval: 30m\n  grace_period: 5m\n")
	got, err := Load(LoadOptions{File: file})
	if err != nil || got.GC.Interval != 30*time.Minute || got.GC.GracePeriod != 5*time.Minute {
		t.Fatalf("gc from file: %+v, %v", got.GC, err)
	}
}
