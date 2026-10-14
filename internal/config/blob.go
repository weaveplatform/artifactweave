package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

// BlobConfig selects one blob store: the hot store (blob) or the archive
// tier (cold_blob). Backend is fs, s3, gcs or azure for a single store, or
// mirror to keep every blob in several of the named Stores (decision 0028).
type BlobConfig struct {
	BlobBackendConfig `yaml:",inline"`
	// Stores are named stores, the members a mirror chooses from.
	Stores map[string]BlobStoreConfig `yaml:"stores"`
	Mirror BlobMirrorConfig           `yaml:"mirror"`
}

// BlobBackendConfig is one store's backend and its settings.
type BlobBackendConfig struct {
	Backend string          `yaml:"backend"`
	FS      BlobFSConfig    `yaml:"fs"`
	S3      BlobS3Config    `yaml:"s3"`
	GCS     BlobGCSConfig   `yaml:"gcs"`
	Azure   BlobAzureConfig `yaml:"azure"`
}

// BlobStoreConfig is a named store: a backend with its zone (a region or
// site label the mirror orders reads and writes by) and whether it accepts
// new copies.
type BlobStoreConfig struct {
	BlobBackendConfig `       yaml:",inline"`
	Zone              string `yaml:"zone"`
	// WriteEnabled false drains the store: no new copies, reads continue.
	// Unset means true.
	WriteEnabled *bool `yaml:"write_enabled"`
}

// Writable reports whether the store accepts new copies.
func (s BlobStoreConfig) Writable() bool { return s.WriteEnabled == nil || *s.WriteEnabled }

// BlobMirrorConfig is the mirror (decision 0028).
type BlobMirrorConfig struct {
	// Members names the stores that hold copies.
	Members []string `yaml:"members"`
	// Redundancy is the number of copies of each blob; zero means every
	// member.
	Redundancy int `yaml:"redundancy"`
	// WriteQuorum is how many copies must be stored before an upload
	// succeeds; zero means Redundancy-1 (at least 1), so one member down
	// does not stop uploads.
	WriteQuorum int `yaml:"write_quorum"`
	// Zone is this instance's zone; members in it are preferred.
	Zone string `yaml:"zone"`
	// MemberTimeout bounds how long a member may finish after the quorum;
	// zero means 30s.
	MemberTimeout time.Duration `yaml:"member_timeout"`
}

// Effective returns the redundancy and write quorum with their defaults
// applied for the configured members.
func (m BlobMirrorConfig) Effective() (redundancy, quorum int) {
	redundancy, quorum = m.Redundancy, m.WriteQuorum
	if redundancy == 0 {
		redundancy = len(m.Members)
	}
	if quorum == 0 {
		quorum = max(1, redundancy-1)
	}
	return redundancy, quorum
}

// storeName restricts store names so they map to environment variables and
// to upload-session backend names unambiguously.
var storeName = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// BlobFSConfig is the local disk backend.
type BlobFSConfig struct {
	Root string `yaml:"root"`
}

// BlobS3Config is the S3-compatible backend. Credentials come from the AWS
// SDK's own chain, never from this file; Profile selects a named profile of
// the shared AWS configuration, so mirror members can use different
// accounts.
type BlobS3Config struct {
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	PathStyle bool   `yaml:"path_style"`
	Profile   string `yaml:"profile"`
	// CDN is a CDN in front of the bucket (decision 0029).
	CDN BlobS3CDNConfig `yaml:"cdn"`
}

// BlobS3CDNConfig is the CDN in front of an s3 store.
type BlobS3CDNConfig struct {
	CloudFront CloudFrontConfig `yaml:"cloudfront"`
}

// CloudFrontConfig is a CloudFront distribution whose origin is the bucket.
// The private key stays in its file, which is read at start: the
// configuration holds only the path.
type CloudFrontConfig struct {
	// Domain is the distribution host, or an http(s) URL for a test edge.
	Domain string `yaml:"domain"`
	// KeyPairID is the public key's ID in a trusted key group.
	KeyPairID string `yaml:"key_pair_id"`
	// PrivateKeyFile is the PEM key that signs URLs.
	PrivateKeyFile string `yaml:"private_key_file"`
	// Hash is sha1 (the default) or sha256.
	Hash string `yaml:"hash"`
	// PathPrefix is the distribution origin's path inside the bucket.
	PathPrefix string `yaml:"path_prefix"`
}

// Enabled reports whether any field is set.
func (c CloudFrontConfig) Enabled() bool { return c != CloudFrontConfig{} }

// BlobGCSConfig is the Google Cloud Storage backend (decision 0027).
// Credentials are Application Default Credentials unless CredentialsFile
// names a service-account key file.
type BlobGCSConfig struct {
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix"`
	Endpoint        string `yaml:"endpoint"`
	CredentialsFile string `yaml:"credentials_file"`
	// GoogleAccessID is the service account that signs download URLs when
	// the credentials alone do not name one.
	GoogleAccessID string   `yaml:"google_access_id"`
	PartSize       ByteSize `yaml:"part_size"`
}

// BlobAzureConfig is the Azure Blob Storage backend (decision 0027).
// AccountKey, SASToken and ConnectionString are secrets: prefer their
// environment variables to the file, and they are redacted from every
// summary.
type BlobAzureConfig struct {
	Account   string `yaml:"account"`
	Endpoint  string `yaml:"endpoint"`
	Container string `yaml:"container"`
	Prefix    string `yaml:"prefix"`
	// Credential is shared_key, sas, connection_string, default (the Azure
	// SDK's default chain), managed_identity or workload_identity. Empty
	// picks connection_string, shared_key or sas when that secret is set,
	// otherwise default.
	Credential              string   `yaml:"credential"`
	AccountKey              string   `yaml:"account_key"`
	SASToken                string   `yaml:"sas_token"`
	ConnectionString        string   `yaml:"connection_string"`
	ManagedIdentityClientID string   `yaml:"managed_identity_client_id"`
	PartSize                ByteSize `yaml:"part_size"`
	CopyRangeSize           ByteSize `yaml:"copy_range_size"`
	Concurrency             int      `yaml:"concurrency"`
	// CDN is a CDN in front of the container (decision 0029).
	CDN BlobAzureCDNConfig `yaml:"cdn"`
}

// BlobAzureCDNConfig is the CDN in front of an azure store.
type BlobAzureCDNConfig struct {
	FrontDoor FrontDoorConfig `yaml:"front_door"`
}

// FrontDoorConfig is an Azure Front Door endpoint whose origin is the
// storage account. Front Door checks no signature: the store signs a read
// SAS and Front Door passes it to storage, so the store's credential must be
// one that signs (not sas).
type FrontDoorConfig struct {
	// Endpoint is the endpoint host, or an http(s) URL for a test edge.
	Endpoint string `yaml:"endpoint"`
	// PathPrefix is the route's origin path inside the account, starting
	// with the container.
	PathPrefix string `yaml:"path_prefix"`
}

// Enabled reports whether any field is set.
func (c FrontDoorConfig) Enabled() bool { return c != FrontDoorConfig{} }

// Azure credential kinds.
const (
	AzureSharedKey        = "shared_key"
	AzureSAS              = "sas"
	AzureConnectionString = "connection_string"
	AzureDefault          = "default"
	AzureManagedIdentity  = "managed_identity"
	AzureWorkloadIdentity = "workload_identity"
)

// Limits the backends enforce, checked here so a bad value fails at start
// rather than on the first upload.
const (
	gcsPartAlign     = 256 << 10
	gcsMinPart       = 1 << 20
	azureMaxBlock    = 4000 << 20
	azureMaxCopyCall = 4000 << 20
)

// mirrorKeys are the mirror settings of a blob section.
var mirrorKeys = []string{
	"mirror.members", "mirror.redundancy", "mirror.write_quorum", "mirror.zone",
	"mirror.member_timeout",
}

// blobKeys lists the static settable keys of one blob section.
func blobKeys(prefix string) []string {
	return prefixed(prefix, append(backendKeys(), mirrorKeys...))
}

// storeKeys lists the keys of the named stores a blob section declares, for
// the environment pass after the file has been read.
func storeKeys(prefix string, b BlobConfig) []string {
	names := make([]string, 0, len(b.Stores))
	for name := range b.Stores {
		names = append(names, name)
	}
	slices.Sort(names)
	fields := append(backendKeys(), "zone", "write_enabled")
	out := make([]string, 0, len(names)*len(fields))
	for _, name := range names {
		out = append(out, prefixed(prefix+".stores."+name, fields)...)
	}
	return out
}

func prefixed(prefix string, sub []string) []string {
	out := make([]string, len(sub))
	for i, k := range sub {
		out[i] = prefix + "." + k
	}
	return out
}

// backendKeys lists the keys of one backend definition.
func backendKeys() []string {
	return []string{
		"backend", "fs.root",
		"s3.bucket", "s3.prefix", "s3.endpoint", "s3.region", "s3.path_style", "s3.profile",
		"s3.cdn.cloudfront.domain", "s3.cdn.cloudfront.key_pair_id",
		"s3.cdn.cloudfront.private_key_file", "s3.cdn.cloudfront.hash",
		"s3.cdn.cloudfront.path_prefix",
		"gcs.bucket", "gcs.prefix", "gcs.endpoint", "gcs.credentials_file",
		"gcs.google_access_id", "gcs.part_size",
		"azure.account", "azure.endpoint", "azure.container", "azure.prefix",
		"azure.credential", "azure.account_key", "azure.sas_token", "azure.connection_string",
		"azure.managed_identity_client_id", "azure.part_size", "azure.copy_range_size",
		"azure.concurrency",
		"azure.cdn.front_door.endpoint", "azure.cdn.front_door.path_prefix",
	}
}

// setBlob assigns one key of a blob section; sub is the key without the
// section prefix and key the full name for errors. A key of an undeclared
// store declares it, so flags can add a store.
func setBlob(b *BlobConfig, sub, key, value string) error {
	if rest, ok := strings.CutPrefix(sub, "stores."); ok {
		name, field, ok := strings.Cut(rest, ".")
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownKey, key)
		}
		if b.Stores == nil {
			b.Stores = map[string]BlobStoreConfig{}
		}
		st := b.Stores[name]
		if err := st.set(field, key, value); err != nil {
			return err
		}
		b.Stores[name] = st
		return nil
	}
	switch sub {
	case "mirror.members":
		b.Mirror.Members = nil
		for m := range strings.SplitSeq(value, ",") {
			if m = strings.TrimSpace(m); m != "" {
				b.Mirror.Members = append(b.Mirror.Members, m)
			}
		}
		return nil
	case "mirror.zone":
		b.Mirror.Zone = value
		return nil
	case "mirror.redundancy", "mirror.write_quorum":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: %s=%q is not an integer", ErrInvalidValue, key, value)
		}
		if sub == "mirror.redundancy" {
			b.Mirror.Redundancy = n
		} else {
			b.Mirror.WriteQuorum = n
		}
		return nil
	case "mirror.member_timeout":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("%w: %s=%q is not a duration", ErrInvalidValue, key, value)
		}
		b.Mirror.MemberTimeout = d
		return nil
	}
	return b.set(sub, key, value)
}

// set assigns one key of a named store.
func (st *BlobStoreConfig) set(field, key, value string) error {
	switch field {
	case "zone":
		st.Zone = value
		return nil
	case "write_enabled":
		var v bool
		if err := setBool(&v, key, value); err != nil {
			return err
		}
		st.WriteEnabled = &v
		return nil
	}
	return st.BlobBackendConfig.set(field, key, value)
}

// set assigns one key of a backend definition.
func (b *BlobBackendConfig) set(sub, key, value string) error {
	str := map[string]*string{
		"backend":                            &b.Backend,
		"fs.root":                            &b.FS.Root,
		"s3.bucket":                          &b.S3.Bucket,
		"s3.prefix":                          &b.S3.Prefix,
		"s3.endpoint":                        &b.S3.Endpoint,
		"s3.region":                          &b.S3.Region,
		"s3.profile":                         &b.S3.Profile,
		"s3.cdn.cloudfront.domain":           &b.S3.CDN.CloudFront.Domain,
		"s3.cdn.cloudfront.key_pair_id":      &b.S3.CDN.CloudFront.KeyPairID,
		"s3.cdn.cloudfront.private_key_file": &b.S3.CDN.CloudFront.PrivateKeyFile,
		"s3.cdn.cloudfront.hash":             &b.S3.CDN.CloudFront.Hash,
		"s3.cdn.cloudfront.path_prefix":      &b.S3.CDN.CloudFront.PathPrefix,
		"gcs.bucket":                         &b.GCS.Bucket,
		"gcs.prefix":                         &b.GCS.Prefix,
		"gcs.endpoint":                       &b.GCS.Endpoint,
		"gcs.credentials_file":               &b.GCS.CredentialsFile,
		"gcs.google_access_id":               &b.GCS.GoogleAccessID,
		"azure.account":                      &b.Azure.Account,
		"azure.endpoint":                     &b.Azure.Endpoint,
		"azure.container":                    &b.Azure.Container,
		"azure.prefix":                       &b.Azure.Prefix,
		"azure.credential":                   &b.Azure.Credential,
		"azure.account_key":                  &b.Azure.AccountKey,
		"azure.sas_token":                    &b.Azure.SASToken,
		"azure.connection_string":            &b.Azure.ConnectionString,
		"azure.managed_identity_client_id":   &b.Azure.ManagedIdentityClientID,
		"azure.cdn.front_door.endpoint":      &b.Azure.CDN.FrontDoor.Endpoint,
		"azure.cdn.front_door.path_prefix":   &b.Azure.CDN.FrontDoor.PathPrefix,
	}
	if dst, ok := str[sub]; ok {
		*dst = value
		return nil
	}
	size := map[string]*ByteSize{
		"gcs.part_size": &b.GCS.PartSize, "azure.part_size": &b.Azure.PartSize,
		"azure.copy_range_size": &b.Azure.CopyRangeSize,
	}
	if dst, ok := size[sub]; ok {
		v, err := ParseByteSize(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		*dst = v
		return nil
	}
	switch sub {
	case "s3.path_style":
		return setBool(&b.S3.PathStyle, key, value)
	case "azure.concurrency":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%w: %s=%q is not an integer", ErrInvalidValue, key, value)
		}
		b.Azure.Concurrency = n
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnknownKey, key)
}

// validate checks one blob section. An empty backend is an error for the
// hot store and means "not configured" for the archive tier.
func (b BlobConfig) validate(prefix string, required bool) []error {
	var errs []error
	names := make([]string, 0, len(b.Stores))
	for name := range b.Stores {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		sp := prefix + ".stores." + name
		if !storeName.MatchString(name) {
			errs = append(errs, fmt.Errorf("%w: store name %q must be 1 to 32 of a-z, 0-9 and _",
				ErrInvalidValue, name))
		}
		st := b.Stores[name]
		if st.Backend == "" || st.Backend == "mirror" {
			errs = append(errs, fmt.Errorf("%w: %s.backend %q (a store is fs, s3, gcs or azure)",
				ErrUnknownBlob, sp, st.Backend))
			continue
		}
		errs = append(errs, st.validate(sp)...)
	}
	switch b.Backend {
	case "":
		if required {
			errs = append(errs, fmt.Errorf("%w: %s.backend is empty", ErrUnknownBlob, prefix))
		}
	case "mirror":
		errs = append(errs, b.validateMirror(prefix)...)
	default:
		errs = append(errs, b.BlobBackendConfig.validate(prefix)...)
	}
	return errs
}

// validateMirror checks the mirror settings against the declared stores.
func (b BlobConfig) validateMirror(prefix string) []error {
	var errs []error
	m := b.Mirror
	if len(m.Members) == 0 {
		errs = append(errs, fmt.Errorf("%w: %s.mirror.members", ErrMissingValue, prefix))
	}
	seen := map[string]bool{}
	writable := 0
	for _, name := range m.Members {
		st, ok := b.Stores[name]
		switch {
		case !ok:
			errs = append(
				errs,
				fmt.Errorf("%w: %s.mirror.members names %q, which %s.stores does not declare",
					ErrInvalidValue, prefix, name, prefix),
			)
		case seen[name]:
			errs = append(
				errs,
				fmt.Errorf("%w: %s.mirror.members lists %q twice", ErrInvalidValue, prefix, name),
			)
		case st.Writable():
			writable++
		}
		seen[name] = true
	}
	r, w := m.Effective()
	switch {
	case len(m.Members) == 0:
	case r < 1 || r > len(m.Members):
		errs = append(
			errs,
			fmt.Errorf("%w: %s.mirror.redundancy %d must be between 1 and the %d members",
				ErrInvalidValue, prefix, r, len(m.Members)),
		)
	case w < 1 || w > r:
		errs = append(
			errs,
			fmt.Errorf("%w: %s.mirror.write_quorum %d must be between 1 and the redundancy %d",
				ErrInvalidValue, prefix, w, r),
		)
	case writable < w:
		errs = append(
			errs,
			fmt.Errorf("%w: %s.mirror has %d write-enabled members, fewer than the write quorum %d",
				ErrInvalidValue, prefix, writable, w),
		)
	}
	if m.MemberTimeout < 0 {
		errs = append(
			errs,
			fmt.Errorf("%w: %s.mirror.member_timeout %s", ErrInvalidValue, prefix, m.MemberTimeout),
		)
	}
	return errs
}

// validate checks one backend definition.
func (b BlobBackendConfig) validate(prefix string) []error {
	var errs []error
	switch b.Backend {
	case "fs":
	case "s3":
		if b.S3.Bucket == "" {
			errs = append(errs, fmt.Errorf("%w: %s.s3.bucket", ErrMissingBucket, prefix))
		}
		errs = append(errs, b.S3.CDN.CloudFront.validate(prefix+".s3.cdn.cloudfront")...)
	case "gcs":
		errs = append(errs, b.GCS.validate(prefix)...)
	case "azure":
		errs = append(errs, b.Azure.validate(prefix)...)
	default:
		errs = append(errs, fmt.Errorf("%w: %s.backend %q", ErrUnknownBlob, prefix, b.Backend))
	}
	return errs
}

func (c CloudFrontConfig) validate(prefix string) []error {
	if !c.Enabled() {
		return nil
	}
	var errs []error
	for _, f := range []struct{ name, value string }{
		{"domain", c.Domain},
		{"key_pair_id", c.KeyPairID},
		{"private_key_file", c.PrivateKeyFile},
	} {
		if f.value == "" {
			errs = append(errs, fmt.Errorf("%w: %s.%s", ErrMissingValue, prefix, f.name))
		}
	}
	if c.Domain != "" {
		if _, err := cdn.ParseDomain(c.Domain); err != nil {
			errs = append(errs, fmt.Errorf("%w: %s.domain: %w", ErrInvalidValue, prefix, err))
		}
	}
	switch strings.ToLower(c.Hash) {
	case "", cdn.HashSHA1, cdn.HashSHA256:
	default:
		errs = append(errs, fmt.Errorf("%w: %s.hash %q is not sha1 or sha256",
			ErrInvalidValue, prefix, c.Hash))
	}
	return errs
}

// cdnDomains maps the configuration key of every CDN domain in the section,
// the named stores' included, to its value.
func (b BlobConfig) cdnDomains(prefix string) map[string]string {
	out := map[string]string{}
	add := func(p string, bc BlobBackendConfig) {
		if cf := bc.S3.CDN.CloudFront; cf.Enabled() {
			out[p+".s3.cdn.cloudfront.domain"] = cf.Domain
		}
		if fd := bc.Azure.CDN.FrontDoor; fd.Enabled() {
			out[p+".azure.cdn.front_door.endpoint"] = fd.Endpoint
		}
	}
	add(prefix, b.BlobBackendConfig)
	for name, st := range b.Stores {
		add(prefix+".stores."+name, st.BlobBackendConfig)
	}
	return out
}

func (g BlobGCSConfig) validate(prefix string) []error {
	var errs []error
	if g.Bucket == "" {
		errs = append(errs, fmt.Errorf("%w: %s.gcs.bucket", ErrMissingBucket, prefix))
	}
	if g.PartSize != 0 && (g.PartSize < gcsMinPart || g.PartSize%gcsPartAlign != 0) {
		errs = append(
			errs,
			fmt.Errorf("%w: %s.gcs.part_size %s must be a multiple of 256KiB and at least 1MiB",
				ErrInvalidValue, prefix, g.PartSize),
		)
	}
	return errs
}

func (a BlobAzureConfig) validate(prefix string) []error {
	var errs []error
	missing := func(field string) {
		errs = append(errs, fmt.Errorf("%w: %s.azure.%s", ErrMissingValue, prefix, field))
	}
	if a.Container == "" {
		missing("container")
	}
	switch a.EffectiveCredential() {
	case AzureConnectionString:
		if a.ConnectionString == "" {
			missing("connection_string")
		}
	case AzureSharedKey:
		if a.AccountKey == "" {
			missing("account_key")
		}
		if a.Account == "" && a.Endpoint == "" {
			missing("account")
		}
	case AzureSAS:
		if a.SASToken == "" {
			missing("sas_token")
		}
		if a.Account == "" && a.Endpoint == "" {
			missing("account")
		}
	case AzureDefault, AzureManagedIdentity, AzureWorkloadIdentity:
		if a.Account == "" && a.Endpoint == "" {
			missing("account")
		}
	default:
		errs = append(
			errs,
			fmt.Errorf("%w: %s.azure.credential %q", ErrInvalidValue, prefix, a.Credential),
		)
	}
	if a.PartSize < 0 || a.PartSize > azureMaxBlock {
		errs = append(
			errs,
			fmt.Errorf("%w: %s.azure.part_size %s exceeds Azure's 4000MiB block limit",
				ErrInvalidValue, prefix, a.PartSize),
		)
	}
	if a.CopyRangeSize < 0 || a.CopyRangeSize > azureMaxCopyCall {
		errs = append(errs, fmt.Errorf("%w: %s.azure.copy_range_size %s exceeds 4000MiB",
			ErrInvalidValue, prefix, a.CopyRangeSize))
	}
	if a.Concurrency < 0 {
		errs = append(
			errs,
			fmt.Errorf("%w: %s.azure.concurrency %d", ErrInvalidValue, prefix, a.Concurrency),
		)
	}
	return append(errs, a.CDN.FrontDoor.validate(prefix+".azure.cdn.front_door",
		a.EffectiveCredential())...)
}

// validate checks a Front Door against the store's credential: a pre-issued
// SAS token signs nothing, and Front Door has no signature of its own to
// fall back on.
func (c FrontDoorConfig) validate(prefix, credential string) []error {
	if !c.Enabled() {
		return nil
	}
	var errs []error
	switch _, err := cdn.ParseDomain(c.Endpoint); {
	case c.Endpoint == "":
		errs = append(errs, fmt.Errorf("%w: %s.endpoint", ErrMissingValue, prefix))
	case err != nil:
		errs = append(errs, fmt.Errorf("%w: %s.endpoint: %w", ErrInvalidValue, prefix, err))
	}
	if credential == AzureSAS {
		errs = append(errs, fmt.Errorf(
			"%w: %s needs a credential that signs a SAS (shared_key, connection_string or "+
				"Entra ID), not sas",
			ErrInvalidValue,
			prefix,
		))
	}
	return errs
}

// EffectiveCredential is Credential, or the kind the configured secrets
// imply when it is empty.
func (a BlobAzureConfig) EffectiveCredential() string {
	switch {
	case a.Credential != "":
		return a.Credential
	case a.ConnectionString != "":
		return AzureConnectionString
	case a.AccountKey != "":
		return AzureSharedKey
	case a.SASToken != "":
		return AzureSAS
	default:
		return AzureDefault
	}
}

// redacted returns b with its secrets replaced, the named stores' included.
func (b BlobConfig) redacted() BlobConfig {
	b.BlobBackendConfig = b.BlobBackendConfig.redacted()
	if b.Stores != nil {
		stores := make(map[string]BlobStoreConfig, len(b.Stores))
		for name, st := range b.Stores {
			st.BlobBackendConfig = st.redacted()
			stores[name] = st
		}
		b.Stores = stores
	}
	return b
}

func (b BlobBackendConfig) redacted() BlobBackendConfig {
	b.Azure.AccountKey = redact(b.Azure.AccountKey)
	b.Azure.SASToken = redact(b.Azure.SASToken)
	b.Azure.ConnectionString = redact(b.Azure.ConnectionString)
	return b
}

// ByteSize is a size in bytes, written in the file and the environment as a
// plain number of bytes or with a binary suffix: "512KiB", "16MiB", "1GiB".
type ByteSize int64

var sizeUnits = []struct {
	suffix string
	mult   int64
}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}

// ParseByteSize reads a ByteSize.
func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range sizeUnits {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = strings.TrimSpace(num), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf(
			"%w: size %q: want bytes or a number with KiB, MiB or GiB",
			ErrInvalidValue,
			s,
		)
	}
	return ByteSize(n * mult), nil
}

// String renders the size with the largest exact binary suffix.
func (b ByteSize) String() string {
	for _, u := range sizeUnits[:3] {
		if b != 0 && int64(b)%u.mult == 0 {
			return strconv.FormatInt(int64(b)/u.mult, 10) + u.suffix
		}
	}
	return strconv.FormatInt(int64(b), 10)
}

// UnmarshalYAML accepts a number of bytes or a suffixed string.
func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*b = v
	return nil
}

// MarshalYAML renders the size as String does.
func (b ByteSize) MarshalYAML() (any, error) { return b.String(), nil }
