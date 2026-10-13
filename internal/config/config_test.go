package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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
		}},
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
		env[envName(k)] = "true"
		if strings.Contains(k, "listen") {
			env[envName(k)] = ":1"
		}
	}
	// Non-boolean keys accept "true" as a plain string; the two booleans parse it.
	cfg := Default()
	for _, k := range keys {
		if err := set(&cfg, k, env[envName(k)]); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	if !cfg.Docker.SubdomainMatch || !cfg.Blob.S3.PathStyle || cfg.Blob.S3.Bucket != "true" {
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
			LoadOptions{Flags: map[string]string{"blob.backend": "gcs"}},
			ErrUnknownBlob,
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
	s := cfg.String()
	for _, secret := range []string{"topsecret", "hunter2", "hunter3"} {
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
