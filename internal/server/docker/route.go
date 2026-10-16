package docker

import (
	"regexp"
	"strings"
)

// Kind is the registry operation a path addresses, before its method picks
// the endpoint.
type Kind int

// The kinds of registry path. Their comments show the path below /v2/,
// where <name> is the image name and, on the repository-path method,
// begins with the repository key.
const (
	// KindBase is the empty path, the version probe clients send first.
	KindBase Kind = iota + 1
	// KindCatalog is _catalog, Docker's listing of image names: of every
	// repository the caller can read on the repository path, of one
	// repository under a subdomain, as <repo>/_catalog or under the
	// Artifactory prefix (decision 0003).
	KindCatalog
	// KindToken is token, the registry token endpoint.
	KindToken
	// KindAuth is auth under the Artifactory prefix, which answers the
	// config.json entry.
	KindAuth
	// KindBlob is <name>/blobs/<digest>.
	KindBlob
	// KindUploads is <name>/blobs/uploads/, which starts an upload.
	KindUploads
	// KindUpload is <name>/blobs/uploads/<session>, one upload in progress.
	KindUpload
	// KindManifest is <name>/manifests/<tag or digest>.
	KindManifest
	// KindTags is <name>/tags/list.
	KindTags
	// KindReferrers is <name>/referrers/<digest>.
	KindReferrers

	// kindRepoCatalog is <repo>/_catalog as parsed; the router turns it
	// into KindCatalog for that repository.
	kindRepoCatalog
)

// String names the kind as an operation, for messages.
func (k Kind) String() string {
	switch k {
	case KindBase:
		return "the version check"
	case KindCatalog, kindRepoCatalog:
		return "the catalog"
	case KindToken:
		return "the token endpoint"
	case KindAuth:
		return "the auth endpoint"
	case KindBlob:
		return "blobs"
	case KindUploads, KindUpload:
		return "blob uploads"
	case KindManifest:
		return "manifests"
	case KindTags:
		return "tag listing"
	case KindReferrers:
		return "the referrers API"
	default:
		return "unknown"
	}
}

// image reports whether the kind is an operation on an image, whose path
// names a repository and an image.
func (k Kind) image() bool {
	switch k {
	case KindBlob, KindUploads, KindUpload, KindManifest, KindTags, KindReferrers:
		return true
	default:
		return false
	}
}

// target is a path below /v2/ split into its operation and what precedes
// it.
type target struct {
	kind Kind
	// image is the segments before the operation, joined: on the
	// repository-path method the repository key and then the image name,
	// otherwise the image name alone. It may be empty or malformed; the
	// caller validates it.
	image string
	// ref is the digest, tag or upload session the path ends in.
	ref string
}

// parseTarget splits rest, the path after /v2/, into a target. Image names
// contain slashes and may contain the reserved words themselves
// (library/blobs is a valid name), so the operation is found from the end:
// the last segments name it and everything before them is the image. false
// means the path is no registry operation.
func parseTarget(rest string) (target, bool) {
	switch rest {
	case "":
		return target{kind: KindBase}, true
	case "_catalog":
		return target{kind: KindCatalog}, true
	case "token":
		return target{kind: KindToken}, true
	}
	segs := strings.Split(rest, "/")
	n := len(segs)
	if n < 2 {
		return target{}, false
	}
	last, op := segs[n-1], segs[n-2]
	image := func(k int) string { return strings.Join(segs[:n-k], "/") }
	switch {
	case n == 2 && last == "_catalog" && op != "":
		// The grammar forbids a leading underscore in a name component,
		// so no image operation ends in _catalog.
		return target{kind: kindRepoCatalog, image: op}, true
	case op == "tags" && last == "list":
		return target{kind: KindTags, image: image(2)}, true
	case op == "blobs" && last == "uploads":
		return target{kind: KindUploads, image: image(2)}, true
	case n >= 3 && segs[n-3] == "blobs" && op == "uploads":
		// A trailing slash starts an upload; anything else names one.
		if last == "" {
			return target{kind: KindUploads, image: image(3)}, true
		}
		return target{kind: KindUpload, image: image(3), ref: last}, true
	case last == "":
		// blobs/, manifests/ and referrers/ without the reference.
		return target{}, false
	case op == "manifests":
		return target{kind: KindManifest, image: image(2), ref: last}, true
	case op == "blobs":
		return target{kind: KindBlob, image: image(2), ref: last}, true
	case op == "referrers":
		return target{kind: KindReferrers, image: image(2), ref: last}, true
	default:
		return target{}, false
	}
}

// nameRE is the image name grammar of the OCI Distribution Specification
// v1.1: lower-case path components separated by slashes.
var nameRE = regexp.MustCompile(
	`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`,
)

// ValidName reports whether name is a valid image name.
func ValidName(name string) bool { return nameRE.MatchString(name) }
