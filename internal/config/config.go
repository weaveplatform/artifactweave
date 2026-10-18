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
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/pkg/cdn"
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
	// DefaultRedirectMinSize matches Artifactory's self-hosted 1 MB: below
	// it a redirect costs the client more than the bytes it saves.
	DefaultRedirectMinSize ByteSize = 1 << 20
	// DefaultRedirectTTL covers a client's retries and the later parts of a
	// split download, which Artifactory's 30 seconds do not (decision 0029).
	DefaultRedirectTTL = 5 * time.Minute
	// maxRedirectTTL is the V4 signing limit on S3 and GCS.
	maxRedirectTTL = 7 * 24 * time.Hour
	// DefaultAQLMaxResults is the cap JFrog SaaS sets with
	// aql.search.query.max.limit; self-managed Artifactory has none (DV-030).
	DefaultAQLMaxResults = 500000
	// DefaultAQLScanFactor reads no extra rows for non-admin filtering, the
	// default of artifactory.aql.query.non.admin.scan.factor.
	DefaultAQLScanFactor = 1
	// DefaultUserQueryLimit is artifactory.search.userQueryLimit's default.
	DefaultUserQueryLimit = 1000
	// The persistent work queue's defaults (decision 0033).
	DefaultWorkWorkers     = 4
	DefaultWorkLease       = 2 * time.Minute
	DefaultWorkMaxAttempts = 10
	// DefaultNPMMaxPublishSize is decision 0034's cap on an npm publish
	// body. The body carries the tarball in base64, a third larger than the
	// tarball itself.
	DefaultNPMMaxPublishSize ByteSize = 1 << 30
)

// Config is the application configuration. YAML tags are the file keys; the
// same dotted key names are used by the flag overlay and, upper-cased with
// the prefix, by the environment.
type Config struct {
	Listen   string         `yaml:"listen"`
	BaseURL  string         `yaml:"base_url"`
	DataDir  string         `yaml:"data_dir"`
	Compat   CompatConfig   `yaml:"compat"`
	Database DatabaseConfig `yaml:"database"`
	Blob     BlobConfig     `yaml:"blob"`
	// ColdBlob is the archive tier (decision 0024); an empty backend means
	// none is configured.
	ColdBlob  BlobConfig `yaml:"cold_blob"`
	MasterKey string     `yaml:"master_key"`
	// MasterKeyPrevious is the key being rotated away from; it is kept
	// until `artifactweave keys rotate` has re-sealed every value.
	MasterKeyPrevious string          `yaml:"master_key_previous"`
	Bootstrap         BootstrapConfig `yaml:"bootstrap"`
	Docker            DockerConfig    `yaml:"docker"`
	Log               LogConfig       `yaml:"log"`
	GC                GCConfig        `yaml:"gc"`
	Download          DownloadConfig  `yaml:"download"`
	Search            SearchConfig    `yaml:"search"`
	Work              WorkConfig      `yaml:"work"`
	Build             BuildConfig     `yaml:"build"`
	NPM               NPMConfig       `yaml:"npm"`
	Token             TokenConfig     `yaml:"token"`
}

// TokenConfig holds the access token settings (decision 0005).
type TokenConfig struct {
	// AllowBasicAuth lets a user name and password sent with HTTP Basic
	// create tokens through POST /access/api/v1/tokens. Off by default, as
	// Artifactory 7.x ships token.allow-basic-auth ("Enable Token Generation
	// via API"): a token outlives a change of the password that minted it,
	// so a leaked password would otherwise leave credentials behind.
	AllowBasicAuth bool `yaml:"allow_basic_auth"`
}

// BuildConfig holds the build-info settings (decision 0014).
type BuildConfig struct {
	// BlockDuplicateEntries refuses a build-info publish whose module lists
	// one file twice, as Artifactory 7.x's
	// artifactory.build.block.duplicate.entries does by default.
	BlockDuplicateEntries bool `yaml:"block_duplicate_entries"`
}

// NPMConfig holds the npm registry's options (decision 0034).
type NPMConfig struct {
	// MaxPublishSize is the largest npm publish request accepted; a larger
	// one answers 413.
	MaxPublishSize ByteSize `yaml:"max_publish_size"`
}

// WorkConfig sizes the persistent work queue (decision 0033), which runs
// derived work such as package metadata after the change that needs it.
type WorkConfig struct {
	// Workers is how many work items run at once on this node.
	Workers int `yaml:"workers"`
	// Lease is how long a run holds its item before another node may take
	// it over; a running item's lease is renewed every half lease.
	Lease time.Duration `yaml:"lease"`
	// MaxAttempts is how many runs an item gets before it is parked.
	MaxAttempts int `yaml:"max_attempts"`
}

// SearchConfig holds the search limits.
type SearchConfig struct {
	AQL AQLConfig `yaml:"aql"`
	// UserQueryLimit caps the results of a GET search for anonymous
	// callers, and for every non-admin unless LimitAnonymousUsersOnly; zero
	// means no cap. search.aql.max_results caps every caller's.
	UserQueryLimit int `yaml:"user_query_limit"`
	// LimitAnonymousUsersOnly spares signed-in users UserQueryLimit, as
	// Artifactory's artifactory.search.limitAnonymousUsersOnly does by
	// default.
	LimitAnonymousUsersOnly bool `yaml:"limit_anonymous_users_only"`
}

// AQLConfig limits POST /api/search/aql (decision 0012).
type AQLConfig struct {
	// MaxResults caps the rows of every query, its own limit included; zero
	// means no cap.
	MaxResults int `yaml:"max_results"`
	// NonAdminScanFactor multiplies a non-admin query's limit to read
	// enough rows that the limit is still met once unreadable rows are
	// filtered out. A request's scan-factor header overrides it.
	NonAdminScanFactor int `yaml:"non_admin_scan_factor"`
}

// DownloadConfig holds how downloads are served.
type DownloadConfig struct {
	Redirect RedirectConfig `yaml:"redirect"`
}

// RedirectConfig tunes downloadRedirect and cdnRedirect (decision 0029).
type RedirectConfig struct {
	// MinSize is the smallest file that redirects; smaller files stream.
	MinSize ByteSize `yaml:"min_size"`
	// TTL is the least time a signed URL stays valid once handed out.
	TTL time.Duration `yaml:"ttl"`
}

// GCConfig schedules garbage collection (decision 0010). Zero values mean
// the defaults: a pass every four hours, as Artifactory schedules it, and an
// hour's grace.
type GCConfig struct {
	// Interval is the time between scheduled passes.
	Interval time.Duration `yaml:"interval"`
	// GracePeriod is how long unreferenced content is kept before it is
	// deleted. It must outlast the slowest upload: content is written before
	// the reference that keeps it.
	GracePeriod time.Duration `yaml:"grace_period"`
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

// BootstrapConfig seeds the first start.
type BootstrapConfig struct {
	AdminPassword string `yaml:"admin_password"`
	Descriptor    string `yaml:"descriptor"`
}

// DockerConfig holds the Docker access-method options.
type DockerConfig struct {
	// SubdomainMatch adds the subdomain method to repository-path routing:
	// <repo>.<base_url host>/v2/<image> addresses an image in repo. It needs
	// base_url, whose host the repository subdomains sit under.
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
	ErrMissingBucket = errors.New("a bucket is required for this blob backend")
	ErrMissingValue  = errors.New("a required configuration value is missing")
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
		Download: DownloadConfig{Redirect: RedirectConfig{
			MinSize: DefaultRedirectMinSize, TTL: DefaultRedirectTTL,
		}},
		Search: SearchConfig{
			AQL: AQLConfig{
				MaxResults: DefaultAQLMaxResults, NonAdminScanFactor: DefaultAQLScanFactor,
			},
			UserQueryLimit: DefaultUserQueryLimit, LimitAnonymousUsersOnly: true,
		},
		Work: WorkConfig{
			Workers:     DefaultWorkWorkers,
			Lease:       DefaultWorkLease,
			MaxAttempts: DefaultWorkMaxAttempts,
		},
		Build: BuildConfig{BlockDuplicateEntries: true},
		NPM:   NPMConfig{MaxPublishSize: DefaultNPMMaxPublishSize},
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
		// Named stores exist only once the file declares them, so their keys
		// are generated after it has been read.
		dynamic := append(storeKeys("blob", cfg.Blob), storeKeys("cold_blob", cfg.ColdBlob)...)
		for _, k := range append(slices.Clone(keys), dynamic...) {
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
var keys = append(append([]string{
	"listen", "base_url", "data_dir",
	"compat.artifactory_version",
	"database.driver", "database.dsn",
	"master_key", "master_key_previous",
	"bootstrap.admin_password", "bootstrap.descriptor",
	"docker.subdomain_match",
	"log.level", "log.format",
	"gc.interval", "gc.grace_period",
	"download.redirect.min_size", "download.redirect.ttl",
	"search.aql.max_results", "search.aql.non_admin_scan_factor",
	"search.user_query_limit", "search.limit_anonymous_users_only",
	"work.workers", "work.lease", "work.max_attempts",
	"build.block_duplicate_entries",
	"npm.max_publish_size",
	"token.allow_basic_auth",
}, blobKeys("blob")...), blobKeys("cold_blob")...)

func envName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// set assigns one dotted key. Booleans parse strictly so "yes" in an
// environment variable fails loudly instead of being read as false.
func set(cfg *Config, key, value string) error {
	if sub, ok := strings.CutPrefix(key, "blob."); ok {
		return setBlob(&cfg.Blob, sub, key, value)
	}
	if sub, ok := strings.CutPrefix(key, "cold_blob."); ok {
		return setBlob(&cfg.ColdBlob, sub, key, value)
	}
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
	case "gc.interval":
		return setDuration(&cfg.GC.Interval, key, value)
	case "gc.grace_period":
		return setDuration(&cfg.GC.GracePeriod, key, value)
	case "download.redirect.min_size":
		return setByteSize(&cfg.Download.Redirect.MinSize, key, value)
	case "download.redirect.ttl":
		return setDuration(&cfg.Download.Redirect.TTL, key, value)
	case "search.aql.max_results":
		return setInt(&cfg.Search.AQL.MaxResults, key, value)
	case "search.aql.non_admin_scan_factor":
		return setInt(&cfg.Search.AQL.NonAdminScanFactor, key, value)
	case "search.user_query_limit":
		return setInt(&cfg.Search.UserQueryLimit, key, value)
	case "search.limit_anonymous_users_only":
		return setBool(&cfg.Search.LimitAnonymousUsersOnly, key, value)
	case "work.workers":
		return setInt(&cfg.Work.Workers, key, value)
	case "work.lease":
		return setDuration(&cfg.Work.Lease, key, value)
	case "work.max_attempts":
		return setInt(&cfg.Work.MaxAttempts, key, value)
	case "build.block_duplicate_entries":
		return setBool(&cfg.Build.BlockDuplicateEntries, key, value)
	case "npm.max_publish_size":
		return setByteSize(&cfg.NPM.MaxPublishSize, key, value)
	case "token.allow_basic_auth":
		return setBool(&cfg.Token.AllowBasicAuth, key, value)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return nil
}

func setDuration(dst *time.Duration, key, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%w: %s=%q is not a duration", ErrInvalidValue, key, value)
	}
	*dst = d
	return nil
}

func setInt(dst *int, key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%w: %s=%q is not an integer", ErrInvalidValue, key, value)
	}
	*dst = n
	return nil
}

func setByteSize(dst *ByteSize, key, value string) error {
	n, err := ParseByteSize(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
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
	} else if c.Docker.SubdomainMatch {
		// Without a configured parent every host would be a candidate
		// subdomain, and a repository named after the first label of the
		// registry's own host would capture all of its /v2 traffic.
		errs = append(errs, fmt.Errorf(
			"%w: docker.subdomain_match needs base_url, whose host repository subdomains sit under",
			ErrMissingValue))
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
	errs = append(errs, c.Blob.validate("blob", true)...)
	errs = append(errs, c.ColdBlob.validate("cold_blob", false)...)
	errs = append(errs, c.validateCDN()...)
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
	if c.GC.Interval < 0 || c.GC.GracePeriod < 0 {
		errs = append(errs, fmt.Errorf("%w: gc.interval and gc.grace_period must not be negative",
			ErrInvalidValue))
	}
	if ttl := c.Download.Redirect.TTL; ttl <= 0 || ttl > maxRedirectTTL {
		errs = append(errs, fmt.Errorf(
			"%w: download.redirect.ttl %s must be more than zero and at most 7 days",
			ErrInvalidValue, ttl))
	}
	if c.Search.AQL.MaxResults < 0 {
		errs = append(errs, fmt.Errorf("%w: search.aql.max_results %d must not be negative",
			ErrInvalidValue, c.Search.AQL.MaxResults))
	}
	if c.Search.AQL.NonAdminScanFactor < 1 {
		errs = append(errs, fmt.Errorf("%w: search.aql.non_admin_scan_factor %d must be at least 1",
			ErrInvalidValue, c.Search.AQL.NonAdminScanFactor))
	}
	if c.Search.UserQueryLimit < 0 {
		errs = append(errs, fmt.Errorf("%w: search.user_query_limit %d must not be negative",
			ErrInvalidValue, c.Search.UserQueryLimit))
	}
	if c.Work.Workers < 1 || c.Work.MaxAttempts < 1 {
		errs = append(
			errs,
			fmt.Errorf("%w: work.workers %d and work.max_attempts %d must be at least 1",
				ErrInvalidValue, c.Work.Workers, c.Work.MaxAttempts),
		)
	}
	// Renewing every half lease must leave a heartbeat time to land.
	if c.Work.Lease < time.Second {
		errs = append(errs, fmt.Errorf("%w: work.lease %s must be at least 1s",
			ErrInvalidValue, c.Work.Lease))
	}
	if c.NPM.MaxPublishSize <= 0 {
		errs = append(errs, fmt.Errorf("%w: npm.max_publish_size %s must be more than zero",
			ErrInvalidValue, c.NPM.MaxPublishSize))
	}
	return errors.Join(errs...)
}

// validateCDN refuses a CDN on the archive tier, whose content is not
// served from an edge, and a CDN on the registry's own host or a subdomain
// of it: Go clients, the JFrog CLI among them, keep sending their
// Authorization header across a redirect to those hosts, so the
// credentials would reach the CDN (decision 0029).
func (c Config) validateCDN() []error {
	var errs []error
	cold := c.ColdBlob.cdnDomains("cold_blob")
	for _, key := range slices.Sorted(maps.Keys(cold)) {
		errs = append(errs, fmt.Errorf("%w: %s: the archive tier has no CDN", ErrInvalidValue, key))
	}
	base, err := url.Parse(c.BaseURL)
	if c.BaseURL == "" || err != nil {
		return errs
	}
	registry := strings.ToLower(base.Hostname())
	hot := c.Blob.cdnDomains("blob")
	for _, key := range slices.Sorted(maps.Keys(hot)) {
		u, err := cdn.ParseDomain(hot[key])
		if err != nil {
			continue // reported by the backend's own validation
		}
		host := strings.ToLower(u.Hostname())
		if host == registry || strings.HasSuffix(host, "."+registry) {
			errs = append(errs, fmt.Errorf(
				"%w: %s %q is base_url's host or a subdomain of it, so clients would send it "+
					"their credentials; use a sibling host", ErrInvalidValue, key, hot[key]))
		}
	}
	return errs
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
	r.Blob = c.Blob.redacted()
	r.ColdBlob = c.ColdBlob.redacted()
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
