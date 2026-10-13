package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// BlobConfig selects one blob store: the hot store (blob) or the archive
// tier (cold_blob).
type BlobConfig struct {
	Backend string          `yaml:"backend"`
	FS      BlobFSConfig    `yaml:"fs"`
	S3      BlobS3Config    `yaml:"s3"`
	GCS     BlobGCSConfig   `yaml:"gcs"`
	Azure   BlobAzureConfig `yaml:"azure"`
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
}

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

// blobKeys lists the settable keys of one blob section.
func blobKeys(prefix string) []string {
	sub := []string{
		"backend", "fs.root",
		"s3.bucket", "s3.prefix", "s3.endpoint", "s3.region", "s3.path_style",
		"gcs.bucket", "gcs.prefix", "gcs.endpoint", "gcs.credentials_file",
		"gcs.google_access_id", "gcs.part_size",
		"azure.account", "azure.endpoint", "azure.container", "azure.prefix",
		"azure.credential", "azure.account_key", "azure.sas_token", "azure.connection_string",
		"azure.managed_identity_client_id", "azure.part_size", "azure.copy_range_size",
		"azure.concurrency",
	}
	out := make([]string, len(sub))
	for i, k := range sub {
		out[i] = prefix + "." + k
	}
	return out
}

// setBlob assigns one key of a blob section; sub is the key without the
// section prefix and key the full name for errors.
func setBlob(b *BlobConfig, sub, key, value string) error {
	str := map[string]*string{
		"backend":                          &b.Backend,
		"fs.root":                          &b.FS.Root,
		"s3.bucket":                        &b.S3.Bucket,
		"s3.prefix":                        &b.S3.Prefix,
		"s3.endpoint":                      &b.S3.Endpoint,
		"s3.region":                        &b.S3.Region,
		"gcs.bucket":                       &b.GCS.Bucket,
		"gcs.prefix":                       &b.GCS.Prefix,
		"gcs.endpoint":                     &b.GCS.Endpoint,
		"gcs.credentials_file":             &b.GCS.CredentialsFile,
		"gcs.google_access_id":             &b.GCS.GoogleAccessID,
		"azure.account":                    &b.Azure.Account,
		"azure.endpoint":                   &b.Azure.Endpoint,
		"azure.container":                  &b.Azure.Container,
		"azure.prefix":                     &b.Azure.Prefix,
		"azure.credential":                 &b.Azure.Credential,
		"azure.account_key":                &b.Azure.AccountKey,
		"azure.sas_token":                  &b.Azure.SASToken,
		"azure.connection_string":          &b.Azure.ConnectionString,
		"azure.managed_identity_client_id": &b.Azure.ManagedIdentityClientID,
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
	switch b.Backend {
	case "":
		if required {
			errs = append(errs, fmt.Errorf("%w: %s.backend is empty", ErrUnknownBlob, prefix))
		}
	case "fs":
	case "s3":
		if b.S3.Bucket == "" {
			errs = append(errs, fmt.Errorf("%w: %s.s3.bucket", ErrMissingBucket, prefix))
		}
	case "gcs":
		errs = append(errs, b.GCS.validate(prefix)...)
	case "azure":
		errs = append(errs, b.Azure.validate(prefix)...)
	default:
		errs = append(errs, fmt.Errorf("%w: %s.backend %q", ErrUnknownBlob, prefix, b.Backend))
	}
	return errs
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

// redacted returns b with its secrets replaced.
func (b BlobConfig) redacted() BlobConfig {
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
