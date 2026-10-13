// Package config loads the application configuration described in
// docs/operations/configuration-reference.md and decision record 0009.
//
// The precedence is flag, then environment, then file, then default. The file
// is strict YAML: an unknown key is an error rather than a silently ignored
// typo, because a misspelt storage key would otherwise start the server on
// the wrong backend without any warning.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvPrefix is prepended to every environment key. Nested keys join their
// segments with an underscore, so `database.dsn` is ARTIFACTWEAVE_DATABASE_DSN.
const EnvPrefix = "ARTIFACTWEAVE_"

// Defaults that the reference documents.
const (
	DefaultListen        = ":8081"
	DefaultDataDir       = "./data"
	DefaultCompatVersion = "7.155.0"
	DefaultDriver        = "sqlite"
	DefaultBlobBackend   = "fs"
	DefaultLogLevel      = "info"
	DefaultLogFormat     = "json"
)

// Config is the application configuration. YAML tags are the file keys; the
// same dotted key names are used by the flag overlay and, upper-cased with
// the prefix, by the environment.
type Config struct {
	Listen    string         `yaml:"listen"`
	BaseURL   string         `yaml:"base_url"`
	DataDir   string         `yaml:"data_dir"`
	Compat    CompatConfig   `yaml:"compat"`
	Database  DatabaseConfig `yaml:"database"`
	Blob      BlobConfig     `yaml:"blob"`
	MasterKey string         `yaml:"master_key"`
	// MasterKeyPrevious is the key being rotated away from; it is kept
	// until `artifactweave keys rotate` has re-sealed every value.
	MasterKeyPrevious string          `yaml:"master_key_previous"`
	Bootstrap         BootstrapConfig `yaml:"bootstrap"`
	Docker            DockerConfig    `yaml:"docker"`
	Log               LogConfig       `yaml:"log"`
}

// CompatConfig holds the Artifactory version string reported to clients.
// Clients gate features on it (decision record 0005), so it is configurable
// rather than derived from the build.
type CompatConfig struct {
	ArtifactoryVersion string `yaml:"artifactory_version"`
}

// DatabaseConfig selects the metadata store.
type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

// BlobConfig selects the blob store.
type BlobConfig struct {
	Backend string       `yaml:"backend"`
	FS      BlobFSConfig `yaml:"fs"`
	S3      BlobS3Config `yaml:"s3"`
}

// BlobFSConfig is the local disk backend.
type BlobFSConfig struct {
	Root string `yaml:"root"`
}

// BlobS3Config is the S3-compatible backend. Credentials come from the AWS
// SDK's own chain, never from this file.
type BlobS3Config struct {
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	PathStyle bool   `yaml:"path_style"`
}

// BootstrapConfig seeds the first start.
type BootstrapConfig struct {
	AdminPassword string `yaml:"admin_password"`
	Descriptor    string `yaml:"descriptor"`
}

// DockerConfig holds the Docker access-method options.
type DockerConfig struct {
	SubdomainMatch bool `yaml:"subdomain_match"`
}

// LogConfig holds slog settings.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// LoadOptions are the inputs to Load. Env and Flags are injected so tests
// never read the process environment.
type LoadOptions struct {
	// File is the YAML path; empty means no file.
	File string
	// Env looks up an environment variable; nil means no environment.
	Env func(string) (string, bool)
	// Flags maps dotted keys (for example "database.dsn") to values given on
	// the command line. Only keys the caller actually set belong here.
	Flags map[string]string
}

// Sentinel errors for Validate; wrap them with context.
var (
	ErrUnknownKey    = errors.New("unknown configuration key")
	ErrInvalidValue  = errors.New("invalid configuration value")
	ErrUnknownDriver = errors.New("unknown database driver")
	ErrMissingDSN    = errors.New("database.dsn is required for postgres")
	ErrUnknownBlob   = errors.New("unknown blob backend")
	ErrMissingBucket = errors.New("blob.s3.bucket is required for the s3 backend")
	ErrBadListen     = errors.New("listen is not a host:port address")
	ErrBadBaseURL    = errors.New("base_url must be an absolute http or https URL")
	ErrMissingKey    = errors.New("master_key is required (set ARTIFACTWEAVE_MASTER_KEY)")
)

// Default returns the configuration with every documented default applied.
func Default() Config {
	return Config{
		Listen:   DefaultListen,
		DataDir:  DefaultDataDir,
		Compat:   CompatConfig{ArtifactoryVersion: DefaultCompatVersion},
		Database: DatabaseConfig{Driver: DefaultDriver},
		Blob:     BlobConfig{Backend: DefaultBlobBackend},
		Log:      LogConfig{Level: DefaultLogLevel, Format: DefaultLogFormat},
	}
}

// Load applies file, environment and flags over the defaults, in that order,
// and validates the result.
func Load(opts LoadOptions) (Config, error) {
	cfg := Default()
	if opts.File != "" {
		if err := loadFile(&cfg, opts.File); err != nil {
			return Config{}, err
		}
	}
	if opts.Env != nil {
		for _, k := range keys {
			if v, ok := opts.Env(envName(k)); ok {
				if err := set(&cfg, k, v); err != nil {
					return Config{}, err
				}
			}
		}
	}
	for k, v := range opts.Flags {
		if err := set(&cfg, k, v); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// OSEnv adapts os.LookupEnv for LoadOptions.Env.
func OSEnv(name string) (string, bool) { return os.LookupEnv(name) }

func loadFile(cfg *Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	// Strict decoding: the whole point of a typed file is that a typo is an
	// error, not a default silently taking effect.
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnknownKey, path, err)
	}
	return nil
}

// keys lists every settable dotted key. Order matters only for determinism.
var keys = []string{
	"listen", "base_url", "data_dir",
	"compat.artifactory_version",
	"database.driver", "database.dsn",
	"blob.backend", "blob.fs.root",
	"blob.s3.bucket", "blob.s3.prefix", "blob.s3.endpoint", "blob.s3.region", "blob.s3.path_style",
	"master_key", "master_key_previous",
	"bootstrap.admin_password", "bootstrap.descriptor",
	"docker.subdomain_match",
	"log.level", "log.format",
}

func envName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// set assigns one dotted key. Booleans parse strictly so "yes" in an
// environment variable fails loudly instead of being read as false.
func set(cfg *Config, key, value string) error {
	switch key {
	case "listen":
		cfg.Listen = value
	case "base_url":
		cfg.BaseURL = value
	case "data_dir":
		cfg.DataDir = value
	case "compat.artifactory_version":
		cfg.Compat.ArtifactoryVersion = value
	case "database.driver":
		cfg.Database.Driver = value
	case "database.dsn":
		cfg.Database.DSN = value
	case "blob.backend":
		cfg.Blob.Backend = value
	case "blob.fs.root":
		cfg.Blob.FS.Root = value
	case "blob.s3.bucket":
		cfg.Blob.S3.Bucket = value
	case "blob.s3.prefix":
		cfg.Blob.S3.Prefix = value
	case "blob.s3.endpoint":
		cfg.Blob.S3.Endpoint = value
	case "blob.s3.region":
		cfg.Blob.S3.Region = value
	case "blob.s3.path_style":
		return setBool(&cfg.Blob.S3.PathStyle, key, value)
	case "master_key":
		cfg.MasterKey = value
	case "master_key_previous":
		cfg.MasterKeyPrevious = value
	case "bootstrap.admin_password":
		cfg.Bootstrap.AdminPassword = value
	case "bootstrap.descriptor":
		cfg.Bootstrap.Descriptor = value
	case "docker.subdomain_match":
		return setBool(&cfg.Docker.SubdomainMatch, key, value)
	case "log.level":
		cfg.Log.Level = value
	case "log.format":
		cfg.Log.Format = value
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return nil
}

func setBool(dst *bool, key, value string) error {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%w: %s=%q is not a boolean", ErrInvalidValue, key, value)
	}
	*dst = b
	return nil
}

// Validate checks the values that would otherwise fail at first use.
func (c Config) Validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("%w: %q", ErrBadListen, c.Listen))
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%w: %q", ErrBadBaseURL, c.BaseURL))
		}
	}
	switch c.Database.Driver {
	case "sqlite":
	case "postgres":
		if c.Database.DSN == "" {
			errs = append(errs, ErrMissingDSN)
		}
	default:
		errs = append(errs, fmt.Errorf("%w: %q", ErrUnknownDriver, c.Database.Driver))
	}
	switch c.Blob.Backend {
	case "fs":
	case "s3":
		if c.Blob.S3.Bucket == "" {
			errs = append(errs, ErrMissingBucket)
		}
	default:
		errs = append(errs, fmt.Errorf("%w: %q", ErrUnknownBlob, c.Blob.Backend))
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("%w: log.level %q", ErrInvalidValue, c.Log.Level))
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("%w: log.format %q", ErrInvalidValue, c.Log.Format))
	}
	return errors.Join(errs...)
}

// RequireMasterKey is the extra check `serve` makes; `config validate` and
// `migrate` run without the key so operators can check a file before the
// secret exists.
func (c Config) RequireMasterKey() error {
	if c.MasterKey == "" {
		return ErrMissingKey
	}
	return nil
}

// SQLitePath is the SQLite database file: the explicit DSN without a
// "file:" prefix, or artifactweave.db under DataDir.
func (c Config) SQLitePath() string {
	if c.Database.DSN != "" {
		return strings.TrimPrefix(c.Database.DSN, "file:")
	}
	return filepath.Join(c.DataDir, "artifactweave.db")
}

// DatabaseDSN is the explicit DSN or, for sqlite, the file under DataDir.
func (c Config) DatabaseDSN() string {
	if c.Database.DSN != "" {
		return c.Database.DSN
	}
	return "file:" + filepath.Join(c.DataDir, "artifactweave.db")
}

// BlobRoot is the explicit fs root or the blobs directory under DataDir.
func (c Config) BlobRoot() string {
	if c.Blob.FS.Root != "" {
		return c.Blob.FS.Root
	}
	return filepath.Join(c.DataDir, "blobs")
}

// String renders the configuration for logs and `config validate` with every
// secret replaced, so a pasted summary can never leak a key or password.
func (c Config) String() string {
	r := c
	r.MasterKey = redact(c.MasterKey)
	r.MasterKeyPrevious = redact(c.MasterKeyPrevious)
	r.Bootstrap.AdminPassword = redact(c.Bootstrap.AdminPassword)
	r.Database.DSN = redactDSN(c.Database.DSN)
	out, err := yaml.Marshal(r)
	if err != nil {
		// yaml.Marshal of a plain struct cannot fail; keep the signature honest.
		return "config: " + err.Error()
	}
	return string(out)
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "<redacted>"
}

// redactDSN hides any password in a URL-style DSN while keeping the host so
// a summary still says where the server connects.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "redacted")
	}
	return u.String()
}
