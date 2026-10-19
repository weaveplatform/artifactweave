package repo

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
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
	return validate(c, true)
}

// ValidateStored is Validate without the key rules, for an update that keeps
// the stored key. That key met them when a client created it, or the server
// created it under a name they reserve, such as artifactory-build-info, which
// they would otherwise refuse on every update.
func ValidateStored(c *Config) error {
	return validate(c, false)
}

func validate(c *Config, keyRules bool) error {
	var errs Errors
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	if keyRules {
		validateKey(c, add)
	}
	class := c.Class
	switch class {
	case Local, Remote, Virtual, Federated:
	case "":
		add("rclass", "is required")
	default:
		add("rclass", fmt.Sprintf("%q is not one of local, remote, virtual, federated", class))
	}
	// These fields are scoped to classes: terraformType names a local or
	// federated registry (the Terraform provider's federated Terraform
	// resource sends it), proxy settings exist only where an outbound
	// connection does (research 15, section 5). Accepting them elsewhere
	// would let a config round-trip through artifactweave that Artifactory
	// itself rejects.
	if c.TerraformType != "" && (class == Remote || class == Virtual) {
		add("terraformType", "is only valid on a local or federated repository")
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
	// A project's repository without environments is valid: Artifactory
	// assigns DEV, and repos.CheckEnvironments applies that default on every
	// write path (decision 0013).
	switch class {
	case Remote:
		if c.URL == "" {
			add("url", "is required for a remote repository")
		}
		validateRetrievalPeriod(c, add)
	case Virtual:
		// No members is valid: repositories is optional in 7.x, and deleting
		// the last member leaves a virtual repository that must still accept
		// updates. A deployment repository then has nothing to name, so the
		// check below refuses any.
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

// mavenGroup is the package types Artifactory treats as one family when it
// offers members for a virtual repository: 6.20's RepoType.isMavenGroup,
// which its virtual repository editor filters by (GetAvailableRepositories).
// 7.x documents only "the same package type", so the family is [I].
var mavenGroup = []PackageType{Maven, Gradle, Ivy, SBT, P2}

// AcceptsMember reports whether a virtual repository of package type
// virtual may list a member of package type member. A generic virtual
// repository aggregates any type; the Maven family shares one metadata
// format, so its types mix; every other type takes only its own.
func AcceptsMember(virtual, member PackageType) bool {
	switch {
	case virtual == Generic, virtual == member:
		return true
	case slices.Contains(mavenGroup, virtual):
		return slices.Contains(mavenGroup, member)
	}
	return false
}

// Repository key limits by class, from JFrog's "Repository Naming Rules and
// Limitations". A remote's 58 leaves room for the "-cache" suffix its cache is
// addressed by; 63 for registry keys is the longest DNS label, since a Docker
// key can be a host name under the subdomain method.
const (
	maxKeyLength         = 64
	maxRemoteKeyLength   = 58
	maxRegistryKeyLength = 63
)

// keyForbidden is the union of the characters JFrog's naming rules forbid
// (/ \ : | ? * " < >) and those the Terraform provider's repo_key validator
// refuses. The two do not conflict: the class pages forbid "special
// characters" in general, the provider spells them out, and only the double
// quote is missing from its list. Spaces are refused as white space of any
// kind.
const keyForbidden = `!@#$%^&*()+={}[]:;<>,/?~` + "`" + `|\"`

// reservedKeys are the names JFrog reserves for repositories Artifactory
// creates itself. Its list capitalises some as list items (Auto-trashcan)
// where the repository is lower case (auto-trashcan), and keys are unique
// without regard to case, so they match without case.
var reservedKeys = []string{
	".", "..", "&",
	"jfrog-usage-logs", "jfrog-billing-logs", "jfrog-logs",
	"artifactory-build-info", "artifactory-pipe-info", "auto-trashcan",
	"jfrog-support-bundle", "_intransit", "artifactory-edge-uploads", "release-bundles",
}

// reservedKeyPrefixes begin the keys JFrog keeps for its system repositories.
// They match without case, as reservedKeys do.
var reservedKeyPrefixes = []string{"jfrog-system-reserved", "jfrog-artifactory-system"}

// registryTypes are the package types whose key names images, as a host name
// under the subdomain method or a path component under the repository path,
// where neither upper case nor an underscore is valid.
var registryTypes = []PackageType{Docker, OCI, HelmOCI}

// validateKey applies JFrog's repository key rules to a key a client chose,
// reporting the first rule it breaks. A leading digit is allowed: Artifactory
// has accepted one since 7.53.1 (RTFACT-27514), although the class pages
// still say a key cannot begin with a number.
func validateKey(c *Config, add func(field, msg string)) {
	key := c.Key
	if key == "" {
		add("key", "is required")
		return
	}
	if msg := keyLength(c); msg != "" {
		add("key", msg)
		return
	}
	if i := strings.IndexFunc(key, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(keyForbidden, r)
	}); i >= 0 {
		r, _ := utf8.DecodeRuneInString(key[i:])
		add(
			"key",
			fmt.Sprintf("must not contain %q: spaces and %s are not allowed", r, keyForbidden),
		)
		return
	}
	lower := strings.ToLower(key)
	if slices.Contains(reservedKeys, lower) {
		add("key", fmt.Sprintf("%q is a reserved repository key", key))
		return
	}
	for _, p := range reservedKeyPrefixes {
		if strings.HasPrefix(lower, p) {
			add("key", fmt.Sprintf("must not start with %s, which is reserved", p))
			return
		}
	}
	if strings.HasSuffix(key, cacheSuffix) {
		add("key", "must not end in -cache, which is reserved for remote repository caches")
		return
	}
	if slices.Contains(registryTypes, c.PackageType) &&
		strings.IndexFunc(key, func(r rune) bool { return r == '_' || unicode.IsUpper(r) }) >= 0 {
		add("key", fmt.Sprintf(
			"must be lower case without an underscore for package type %s",
			c.PackageType))
	}
}

// keyLength returns why the key is too long for its class, or "". Length is
// counted in characters, as the documentation states it.
func keyLength(c *Config) string {
	n := utf8.RuneCountInString(c.Key)
	switch {
	case c.Class == Remote && n > maxRemoteKeyLength:
		return fmt.Sprintf(
			"must be at most %d characters in a remote repository",
			maxRemoteKeyLength,
		)
	case (c.Class == Local || c.Class == Federated) &&
		slices.Contains(registryTypes, c.PackageType) && n > maxRegistryKeyLength:
		return fmt.Sprintf("must be at most %d characters in a %s %s repository",
			maxRegistryKeyLength, c.Class, c.PackageType)
	case n > maxKeyLength:
		return fmt.Sprintf("must be at most %d characters", maxKeyLength)
	}
	return ""
}

// validateRetrievalPeriod applies the minimum metadata retrieval cache
// period of a remote repository: 30 seconds, except on a smart remote
// repository (contentSynchronisation enabled, which the Terraform provider
// documents as proxying another Artifactory instance), and 14,400 seconds on
// a NuGet remote of chocolatey.org. It checks the stored value on every
// update too: JFrog's page keeps an existing shorter period only "until you
// edit" the repository. A negative period is left to validateNumbers.
func validateRetrievalPeriod(c *Config, add func(field, msg string)) {
	p := c.RetrievalCachePeriodSecs
	if p == nil || *p < 0 {
		return
	}
	minimum := MinRetrievalCachePeriodSecs
	switch {
	case c.PackageType == NuGet && chocolatey(c.URL):
		minimum = MinChocolateyRetrievalCachePeriodSecs
	case c.ContentSynchronisation != nil && c.ContentSynchronisation.Enabled:
		return
	}
	if *p < minimum {
		add("retrievalCachePeriodSecs", fmt.Sprintf("must be at least %d seconds", minimum))
	}
}

// chocolatey reports whether rawURL is on chocolatey.org or a host under it,
// such as community.chocolatey.org, which JFrog's page names. Whether 7.x
// matches the other hosts under chocolatey.org is [I].
func chocolatey(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "chocolatey.org" || strings.HasSuffix(host, ".chocolatey.org")
}

// validateNumbers rejects negative durations and counts. Zero is meaningful
// (unlimited or disabled) for every one of these fields, so only the sign is
// checked; a remote's retrievalCachePeriodSecs also has a minimum
// (validateRetrievalPeriod).
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
