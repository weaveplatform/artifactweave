package terraformhttp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type remotePlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type remoteProviderVersion struct {
	Version   string           `json:"version"`
	Protocols []string         `json:"protocols,omitempty"`
	Platforms []remotePlatform `json:"platforms,omitempty"`
}

type remoteProviderPackage struct {
	Protocols    []string `json:"protocols"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Filename     string   `json:"filename"`
	DownloadURL  string   `json:"download_url"`
	SumsURL      string   `json:"shasums_url"`
	SignatureURL string   `json:"shasums_signature_url"`
	Shasum       string   `json:"shasum"`
	SigningKeys  struct {
		GPG []publicKey `json:"gpg_public_keys"`
	} `json:"signing_keys"`
	doc map[string]json.RawMessage
	// paths are the cache paths of the archive, the SHA256SUMS and its
	// signature, in that order, which their URLs set (providerFilePath).
	paths []string
}

func remoteProviderError(message string) error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Terraform provider: "+message)
}

func providerJSON(body io.Reader, doc any) error {
	data, err := io.ReadAll(io.LimitReader(body, moduleMetadataLimit+1))
	if err != nil || len(data) > moduleMetadataLimit || json.Unmarshal(data, doc) != nil {
		return remoteProviderError("malformed or oversized metadata")
	}
	return nil
}

// providerListingPath is where a listing checks Read on a platform's
// archive, before it reads the package metadata that sets the archive's
// URL and so its cache path (providerFilePath): the archive's name in the
// provider's own folder. Where 7.x checks a listing is [I], so a listing
// filters as it did before the files moved to 7.x's paths.
func providerListingPath(q providerOp) string {
	return providerFolder(q) + "/" + q.version + "/" + providerArchiveName(q)
}

// providerArchiveName is the archive's name in a local provider
// repository's layout (decision 0040).
func providerArchiveName(q providerOp) string {
	_, typ, _ := strings.Cut(q.name, "/")
	return "terraform-provider-" + typ + "_" + q.version + "_" + q.os + "_" + q.arch + ".zip"
}

// validRemoteProvider checks q's name, version and platform by the rules
// the index reads a local repository's archive by.
func validRemoteProvider(q providerOp) bool {
	file, ok := tf.ParseProviderPackageFile(q.name + "/" + q.version + "/" + providerArchiveName(q))
	return ok && file.Package() == q.name && file.Version == q.version && file.OS == q.os &&
		file.Arch == q.arch
}

// providerFilePath is the cache path of a provider file at target: below
// the remote's Providers URL, JFrog's "base URL of the provider's storage
// API", where a HashiCorp release is, or under external/<host>/
// (remoteFilePath).
func providerFilePath(c repo.Config, target string) (string, bool) {
	return remoteFilePath(c.TerraformProvidersURL, target)
}

func validProviderProtocols(protocols []string, required bool) bool {
	if len(protocols) > 64 || required && len(protocols) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, version := range protocols {
		major, minor, dotted := strings.Cut(version, ".")
		if major == "" || dotted && minor == "" ||
			strings.Trim(major+minor, "0123456789") != "" ||
			seen[version] ||
			len(version) > 32 {
			return false
		}
		seen[version] = true
	}
	return true
}

func parseRemoteProviderVersions(body io.Reader, q providerOp) ([]remoteProviderVersion, error) {
	var doc struct {
		Versions []remoteProviderVersion `json:"versions"`
	}
	if providerJSON(body, &doc) != nil || doc.Versions == nil ||
		len(doc.Versions) > moduleVersionLimit {
		return nil, remoteProviderError("invalid version list")
	}
	seen := map[string]bool{}
	for _, version := range doc.Versions {
		q.version, q.os, q.arch = version.Version, "linux", "amd64"
		if !validRemoteProvider(q) || seen[version.Version] ||
			!validProviderProtocols(version.Protocols, false) ||
			len(version.Platforms) > 256 {
			return nil, remoteProviderError("invalid version or platform")
		}
		seen[version.Version] = true
		platforms := map[string]bool{}
		for _, platform := range version.Platforms {
			q.os, q.arch = platform.OS, platform.Arch
			key := q.os + "_" + q.arch
			if !validRemoteProvider(q) || platforms[key] {
				return nil, remoteProviderError("invalid or duplicate platform")
			}
			platforms[key] = true
		}
	}
	return doc.Versions, nil
}

func parseProviderDiscovery(body io.Reader, base string) (string, error) {
	var doc map[string]json.RawMessage
	if providerJSON(body, &doc) != nil || doc == nil {
		return "", remoteProviderError("invalid discovery document")
	}
	var raw string
	if json.Unmarshal(doc["providers.v1"], &raw) != nil {
		return "", remoteProviderError("missing providers.v1 service")
	}
	target, err := moduleURL(base, raw)
	if err != nil {
		return "", remoteProviderError("invalid providers.v1 URL")
	}
	u, _ := url.Parse(target)
	if u.RawQuery != "" || !strings.HasSuffix(u.Path, "/") {
		return "", remoteProviderError("invalid providers.v1 URL")
	}
	return target, nil
}

func allowProviderURL(c repo.Config, target string) error {
	// These are operator-selected provider endpoints. Module storage does not
	// automatically authorize unrelated provider storage origins.
	c.URL = c.TerraformProvidersURL
	return allowModuleURL(c, target)
}

func providerURLGuard(c repo.Config, initial string) func(context.Context, *url.URL) error {
	return func(_ context.Context, target *url.URL) error {
		if target == nil {
			return remoteProviderError("missing destination URL")
		}
		resolved, err := moduleURL(initial, target.String())
		if err != nil {
			return remoteProviderError("invalid destination URL")
		}
		return allowProviderURL(c, resolved)
	}
}

func parseRemoteProviderPackage(
	body io.Reader,
	base string,
	c repo.Config,
	q providerOp,
) (remoteProviderPackage, error) {
	var result remoteProviderPackage
	var raw map[string]json.RawMessage
	if providerJSON(body, &raw) != nil || raw == nil {
		return result, remoteProviderError("invalid package metadata")
	}
	data, _ := json.Marshal(raw)
	if json.Unmarshal(data, &result) != nil || result.OS != q.os || result.Arch != q.arch ||
		!validProviderProtocols(result.Protocols, true) ||
		len(result.SigningKeys.GPG) == 0 ||
		len(result.SigningKeys.GPG) > 32 {
		return result, remoteProviderError("invalid package platform, protocols or signing keys")
	}
	if result.Filename == "" || path.Base(result.Filename) != result.Filename ||
		strings.ContainsAny(result.Filename, "\\\x00\r\n") ||
		!strings.HasSuffix(result.Filename, ".zip") ||
		len(result.Filename) > 255 {
		return result, remoteProviderError("invalid archive filename")
	}
	sum, err := hex.DecodeString(result.Shasum)
	if err != nil || len(sum) != 32 {
		return result, remoteProviderError("invalid archive SHA-256")
	}
	seen := map[string]bool{}
	for _, key := range result.SigningKeys.GPG {
		ring, e := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Armor))
		if e != nil || len(ring) != 1 || ring[0].PrivateKey != nil ||
			ring[0].PrimaryKey.KeyIdString() != key.ID ||
			seen[key.ID] {
			return result, remoteProviderError("signing key identity does not match its public key")
		}
		seen[key.ID] = true
	}
	for _, rawURL := range []*string{&result.DownloadURL, &result.SumsURL, &result.SignatureURL} {
		target, e := moduleURL(base, *rawURL)
		if e != nil {
			return result, remoteProviderError("invalid package URL")
		}
		if e = allowProviderURL(c, target); e != nil {
			return result, e
		}
		*rawURL = target
		path, ok := providerFilePath(c, target)
		if !ok {
			return result, remoteProviderError("invalid package URL")
		}
		result.paths = append(result.paths, path)
	}
	result.doc = raw
	return result, nil
}

func providerFolder(q providerOp) string {
	if q.isUpstreamMirror {
		return "provider-mirrors/" + q.host + "/" + q.name
	}
	return q.name
}

// providerDocument is the 7.x cache path of the registry document a client
// asks a remote for, where the remote's patterns are checked. 7.x caches
// the version list at .terraform/providers/<ns>/<type>/versions, also when
// it answers the network mirror's index (RTFACT-31026), so the mirror's
// documents are checked there; a platform's package metadata is taken to
// sit below it [I].
func providerDocument(q providerOp) string {
	base := ".terraform/providers/" + q.name
	if q.isList || q.isMirror {
		return base + "/versions"
	}
	return base + "/" + q.version + "/download/" + q.os + "/" + q.arch
}
