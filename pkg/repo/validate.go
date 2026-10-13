package repo

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrInvalid is the sentinel every validation failure wraps.
var ErrInvalid = errors.New("invalid repository configuration")

// FieldError reports one invalid field. Field is the JSON name so the error
// can be shown to API clients and Terraform users as they see the document.
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string {
	return e.Field + ": " + e.Message
}

// Errors is the list of field errors Validate found. It unwraps to ErrInvalid.
type Errors []FieldError

func (es Errors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return ErrInvalid.Error() + ": " + strings.Join(parts, "; ")
}

// Unwrap lets errors.Is(err, ErrInvalid) work on the list.
func (es Errors) Unwrap() error {
	return ErrInvalid
}

// cacheSuffix is reserved: Artifactory exposes a remote repository's cache
// under "<key>-cache" without it being a repository of its own, so a real
// repository with that suffix would be unaddressable.
const cacheSuffix = "-cache"

// Validate checks the rules a create or full update must satisfy. It returns
// nil or an Errors list; it does not apply defaults, so run Defaults first when
// validating a document that relies on them.
func Validate(c *Config) error {
	var errs Errors
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	validateKey(c.Key, add)
	class := c.Class
	switch class {
	case Local, Remote, Virtual, Federated:
	case "":
		add("rclass", "is required")
	default:
		add("rclass", fmt.Sprintf("%q is not one of local, remote, virtual, federated", class))
	}
	// The crawled schemas scope these fields to classes: terraformType exists only
	// on local, proxy settings only where an outbound connection exists
	// (research 15, section 5). Accepting them elsewhere would let a config
	// round-trip through artifactweave that Artifactory itself rejects.
	if c.TerraformType != "" && class != "" && class != Local {
		add("terraformType", "is only valid on a local repository")
	}
	if (c.Proxy != "" || c.DisableProxy != nil) && (class == Local || class == Virtual) {
		add("proxy", "is only valid on remote and federated repositories")
	}
	if c.PackageType != "" && class != "" {
		if allowed := AllowedPackageTypes(
			class,
		); allowed != nil &&
			!slices.Contains(allowed, c.PackageType) {
			add(
				"packageType",
				fmt.Sprintf("%q is not allowed for a %s repository", c.PackageType, class),
			)
		}
	}
	if c.ProjectKey != "" && len(c.Environments) == 0 {
		add("environments", "is required when projectKey is set")
	}
	switch class {
	case Remote:
		if c.URL == "" {
			add("url", "is required for a remote repository")
		}
	case Virtual:
		if len(c.Repositories) == 0 {
			add("repositories", "must list at least one member")
		}
		if c.DefaultDeploymentRepo != "" &&
			!slices.Contains(c.Repositories, c.DefaultDeploymentRepo) {
			add(
				"defaultDeploymentRepo",
				fmt.Sprintf("%q is not one of the member repositories", c.DefaultDeploymentRepo),
			)
		}
	}
	validateNumbers(c, add)
	if errs == nil {
		return nil
	}
	return errs
}

func validateKey(key string, add func(field, msg string)) {
	switch {
	case key == "":
		add("key", "is required")
	case strings.ContainsAny(key, " /\\"):
		add("key", "must not contain spaces or slashes")
	case strings.HasSuffix(key, cacheSuffix):
		add("key", "must not end in -cache, which is reserved for remote repository caches")
	}
}

// validateNumbers rejects negative durations and counts. Zero is meaningful
// (unlimited or disabled) for every one of these fields, so only the sign is
// checked.
func validateNumbers(c *Config, add func(field, msg string)) {
	fields := []struct {
		name string
		v    *int
	}{
		{"maxUniqueSnapshots", c.MaxUniqueSnapshots},
		{"maxUniqueTags", c.MaxUniqueTags},
		{"dockerTagRetention", c.DockerTagRetention},
		{"yumRootDepth", c.YumRootDepth},
		{"socketTimeoutMillis", c.SocketTimeoutMillis},
		{"retrievalCachePeriodSecs", c.RetrievalCachePeriodSecs},
		{"missedRetrievalCachePeriodSecs", c.MissedRetrievalCachePeriodSecs},
		{"metadataRetrievalTimeoutSecs", c.MetadataRetrievalTimeoutSecs},
		{"assumedOfflinePeriodSecs", c.AssumedOfflinePeriodSecs},
		{"unusedArtifactsCleanupPeriodHours", c.UnusedArtifactsCleanupPeriodHours},
		{"virtualRetrievalCachePeriodSecs", c.VirtualRetrievalCachePeriodSecs},
		{"signedUrlTtl", c.SignedURLTTL},
		{"xrayDataTtl", c.XrayDataTTL},
	}
	for _, f := range fields {
		if f.v != nil && *f.v < 0 {
			add(f.name, "must not be negative")
		}
	}
}
