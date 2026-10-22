package nuget

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"time"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Resource URLs are data from the remote, never client-provided targets. The
// transport repeats address, redirect and credential-origin checks after DNS.
func resourceURL(c repo.Config, raw string, https bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(
			raw,
			"\\\r\n\x00",
		) || (u.Scheme != "http" && u.Scheme != "https") || (https && u.Scheme != "https") {
		return remoteFailure()
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." || strings.ContainsAny(p, "\\\x00") {
			return remoteFailure()
		}
	}
	base, _ := url.Parse(c.V3FeedURL)
	if base != nil && base.Scheme == "https" && u.Scheme != "https" {
		return remoteFailure()
	}
	trusted := false
	for _, rawBase := range []string{c.URL, c.V3FeedURL} {
		b, e := url.Parse(rawBase)
		if e == nil && b.Scheme == u.Scheme && strings.EqualFold(b.Host, u.Host) {
			trusted = true
		}
	}
	if !trusted {
		host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return remoteFailure()
		}
		if ip, e := netip.ParseAddr(host); e == nil {
			ip = ip.Unmap()
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
				ip.IsLinkLocalUnicast() {
				return remoteFailure()
			}
		}
	}
	return nil
}

func validateDocument(c repo.Config, d remoteDoc, kind, id string) error {
	switch kind {
	case "index":
		return validateIndex(c, d)
	case "versions":
		var versions []string
		if json.Unmarshal(d["versions"], &versions) != nil || versions == nil ||
			len(versions) > 10000 {
			return remoteFailure()
		}
		seen := map[string]bool{}
		for _, v := range versions {
			k, ok := versionKey(v)
			if !ok || seen[k] {
				return remoteFailure()
			}
			seen[k] = true
		}
	case "registration":
		return validateRegistration(c, d, id, false, false)
	case "page":
		return validateRegistration(c, d, id, true, false)
	case "search":
		return validateSearchDocument(d)
	case "autocomplete-id", "autocomplete-version":
		var entries []string
		if json.Unmarshal(d["data"], &entries) != nil || entries == nil || len(entries) > 1000 {
			return remoteFailure()
		}
		for _, entry := range entries {
			if kind == "autocomplete-id" {
				if !nugetfmt.ValidID(entry) {
					return remoteFailure()
				}
			} else if _, ok := versionKey(entry); !ok {
				return remoteFailure()
			}
		}
	case "signatures":
		var all bool
		_ = json.Unmarshal(d["allRepositorySigned"], &all)
		if id != "5.0.0" && all {
			return remoteFailure()
		}
		return validateSignatures(c, d)
	default:
		return remoteFailure()
	}
	return nil
}

func validateRegistration(c repo.Config, d remoteDoc, id string, page, inline bool) error {
	items := arrayField(d, "items")
	if items == nil || len(items) > 10000 || !remoteCount(d, len(items)) {
		return remoteFailure()
	}
	if raw := stringField(d, "@id"); raw != "" {
		if err := registrationPageID(c, raw, inline); err != nil {
			return err
		}
	}
	for _, entry := range items {
		if page {
			if err := validateEntry(c, entry, id); err != nil {
				return err
			}
			continue
		}
		if _, lo := versionKey(stringField(entry, "lower")); !lo {
			return remoteFailure()
		}
		if _, hi := versionKey(stringField(entry, "upper")); !hi {
			return remoteFailure()
		}
		_, inlinePage := entry["items"]
		if err := registrationPageID(c, stringField(entry, "@id"), inlinePage); err != nil {
			return err
		}
		var count int
		if json.Unmarshal(entry["count"], &count) != nil || count < 0 || count > 10000 {
			return remoteFailure()
		}
		if inlinePage {
			if err := validateRegistration(c, entry, id, true, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func registrationPageID(c repo.Config, raw string, inline bool) error {
	if !inline {
		return resourceURL(c, raw, false)
	}
	// NuGet identifies inline pages with index.json#page/... . These identities
	// are never fetched: all leaves are already present and delivery rewrites
	// page URLs. Keep the outbound URL rules for their base and for every link
	// that can actually initiate a request.
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(raw, "\\\r\n\x00") ||
		strings.ContainsAny(u.Fragment, "\\\r\n\x00") {
		return remoteFailure()
	}
	base, _, _ := strings.Cut(raw, "#")
	return resourceURL(c, base, false)
}

func validateEntry(c repo.Config, d remoteDoc, id string) error {
	catalog := objectField(d, "catalogEntry")
	if catalog == nil || !strings.EqualFold(stringField(catalog, "id"), id) {
		return remoteFailure()
	}
	if _, ok := versionKey(stringField(catalog, "version")); !ok {
		return remoteFailure()
	}
	if resourceURL(c, stringField(d, "@id"), false) != nil ||
		resourceURL(c, stringField(catalog, "@id"), false) != nil {
		return remoteFailure()
	}
	for _, target := range []string{stringField(d, "packageContent"), stringField(catalog, "packageContent")} {
		if target != "" {
			if err := resourceURL(c, target, false); err != nil {
				return err
			}
		}
	}
	if stringField(d, "packageContent") == "" && stringField(catalog, "packageContent") == "" {
		return remoteFailure()
	}
	if raw, ok := catalog["listed"]; ok {
		var listed bool
		if json.Unmarshal(raw, &listed) != nil {
			return remoteFailure()
		}
	}
	for _, g := range arrayField(catalog, "dependencyGroups") {
		for _, dep := range arrayField(g, "dependencies") {
			if !nugetfmt.ValidID(stringField(dep, "id")) {
				return remoteFailure()
			}
		}
	}
	_, err := entryChecksum(catalog)
	return err
}

func entryChecksum(d remoteDoc) (types.Checksums, error) {
	hash, algorithm := stringField(d, "packageHash"), stringField(d, "packageHashAlgorithm")
	if hash == "" && algorithm == "" {
		return types.Checksums{}, nil
	}
	if !strings.EqualFold(algorithm, "SHA512") {
		return types.Checksums{}, remoteFailure()
	}
	b, err := base64.StdEncoding.DecodeString(hash)
	if err != nil || len(b) != 64 {
		return types.Checksums{}, remoteFailure()
	}
	return types.Checksums{SHA512: hex.EncodeToString(b)}, nil
}

const sha256OID = "2.16.840.1.101.3.4.2.1"

func certificateHash(d remoteDoc) string {
	return strings.ToLower(stringField(objectField(d, "fingerprints"), sha256OID))
}

func validateSignatures(c repo.Config, d remoteDoc) error {
	var all bool
	if json.Unmarshal(d["allRepositorySigned"], &all) != nil {
		return remoteFailure()
	}
	certs := arrayField(d, "signingCertificates")
	if certs == nil || len(certs) > 256 {
		return remoteFailure()
	}
	for _, cert := range certs {
		if err := resourceURL(c, stringField(cert, "contentUrl"), true); err != nil {
			return err
		}
		b, err := hex.DecodeString(certificateHash(cert))
		if err != nil || len(b) != 32 {
			return remoteFailure()
		}
		for _, key := range []string{"subject", "issuer"} {
			if stringField(cert, key) == "" {
				return remoteFailure()
			}
		}
		for _, key := range []string{"notBefore", "notAfter"} {
			if _, err := time.Parse(time.RFC3339, stringField(cert, key)); err != nil {
				return remoteFailure()
			}
		}
	}
	return nil
}

func validateSearchDocument(d remoteDoc) error {
	entries := arrayField(d, "data")
	if entries == nil || len(entries) > 1000 {
		return remoteFailure()
	}
	for _, entry := range entries {
		name := stringField(entry, "id")
		if !nugetfmt.ValidID(name) {
			return remoteFailure()
		}
		if _, ok := versionKey(stringField(entry, "version")); !ok {
			return remoteFailure()
		}
		versions := arrayField(entry, "versions")
		if versions == nil || len(versions) > 10000 {
			return remoteFailure()
		}
		for _, v := range versions {
			if _, ok := versionKey(stringField(v, "version")); !ok {
				return remoteFailure()
			}
		}
	}

	return nil
}

func validateIndex(c repo.Config, d remoteDoc) error {
	version, ok := nugetfmt.ParseVersion(stringField(d, "version"))
	if !ok || !strings.HasPrefix(version.Normalized(), "3.") {
		return remoteFailure()
	}
	var resources []resource
	if json.Unmarshal(d["resources"], &resources) != nil || len(resources) == 0 ||
		len(resources) > 256 {
		return remoteFailure()
	}
	for _, r := range resources {
		if r.Type == "" ||
			resourceURL(c, r.ID, strings.HasPrefix(r.Type, "RepositorySignatures/")) != nil {
			return remoteFailure()
		}
	}

	return nil
}

func remoteCount(d remoteDoc, want int) bool {
	var n int
	return json.Unmarshal(d["count"], &n) == nil && n == want
}
