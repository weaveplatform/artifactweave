package pypi

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Admit applies the deploy policy of every package type, then refuses a
// write into the namespaces PyPI reserves, then applies the enforcement
// settings to an upload (decision 0037).
func (h Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := h.Handler.Admit(ctx, env, c, w)
	if err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	if err := checkReserved(c, w); err != nil {
		return w, err
	}
	if err := h.Settings.check(env, c, w); err != nil {
		return w, err
	}
	return w, nil
}

// reserved are the top-level folders 7.x keeps "for internal logic" in a
// PyPI repository and refuses deploys to with 400 (pypi-repositories,
// "Additional PyPI Publishing Tools").
var reserved = []string{"packages", "simple"}

// checkReserved refuses with 400 any write a client makes under packages/
// or simple/: an upload of any kind, and a copy or move into them, since a
// file under simple/ could not be served through the API. What a remote
// repository caches there is the server's own. [I] 7.x's message, and
// whether it refuses copies and moves (RTFACT-30372 shows 7.49.8 accepting
// one), are unknown (#216).
func checkReserved(c repo.Config, w format.Write) error {
	if w.Cached {
		return nil
	}
	top, _, _ := strings.Cut(w.Path, "/")
	for _, r := range reserved {
		if top == r {
			return format.Reject(http.StatusBadRequest,
				"The repository '%s' rejected '%s': packages/ and simple/ are reserved in a "+
					"PyPI repository.", c.Key, w.Path)
		}
	}
	return nil
}

// enforces reports whether an enforcement setting checks w, a write to c:
// an upload of a distribution file to a local or federated repository, by
// a REST deploy, a checksum deploy, an archive's entry or the upload API.
// Files stored before a setting was turned on stay, as in 7.x
// (pypi-repositories, "Backward Compatibility"). A copy or a move is not
// an upload, as npm.enforce_path does not check one either. [I] What 7.x
// does with a copy, a move or a federated repository is unknown (#216).
func (s Settings) enforces(c repo.Config, w format.Write) bool {
	return (s.EnforceLayout || s.EnforceNamingNormalization) &&
		w.Op == format.OpDeploy && !w.Cached &&
		(c.Class == repo.Local || c.Class == repo.Federated) && IsDistribution(w.Path)
}

// check applies the enforcement settings that are on to w, naming
// normalisation first, since it rests on the file name alone. [I] Which
// check 7.x answers first, and what it puts in place of <ARTIFACT_NAME>,
// are unknown (#216).
func (s Settings) check(env format.Env, c repo.Config, w format.Write) error {
	if !s.enforces(c, w) {
		return nil
	}
	file := path.Base(w.Path)
	var d *Dist
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == w.Checksums.SHA256 &&
		in.name == file {
		d = in.dist
	}
	if s.EnforceNamingNormalization && !normalized(file, d) {
		return format.Reject(http.StatusForbidden,
			"Action prevented due to artifact file name normalization requirements - "+
				"artifact name %s does not conform to normalized requirements", file)
	}
	if !s.EnforceLayout {
		return nil
	}
	if d == nil {
		return format.Reject(
			http.StatusForbidden,
			"Action prevented due to Enforce Layout policy - could not find metadata file to compare",
		)
	}
	if !matchesLayout(file, d) {
		// JFrog's documented text carries a stray `” +` from its source
		// between "the" and "package"; the message is the intended one.
		return format.Reject(http.StatusForbidden,
			"Action prevented due to Enforce Layout policy - package file name/version %s does "+
				"not match the package internal name/version %s/%s in the metadata file",
			file, d.Name, d.Version)
	}
	return nil
}

// normalized reports whether the file name passes naming normalisation:
// its name segment, what precedes "-<Version>" with the metadata's
// Version d gives, holds no "-" (pypi-repositories: jfrog_pypi-1.0.0.tar.gz
// and jfrog.pypi-1.0.0.tar.gz pass, jfrog-pypi-1.0.0.tar.gz does not).
// Without a Version in both, a wheel must have five "-"-separated fields,
// or six with a build tag that begins with a digit, and any other file's
// part before its last "-" must hold no "-" (decision 0037).
func normalized(file string, d *Dist) bool {
	t, ext, _ := typeOf(file)
	stem := strings.TrimSuffix(file, ext)
	if d != nil {
		if segment, ok := nameSegment(stem, d.Version); ok {
			return !strings.Contains(segment, "-")
		}
	}
	if t == Wheel {
		fields := strings.Split(stem, "-")
		return len(fields) == 5 ||
			len(fields) == 6 && fields[2] != "" && fields[2][0] >= '0' && fields[2][0] <= '9'
	}
	i := strings.LastIndex(stem, "-")
	return i < 0 || !strings.Contains(stem[:i], "-")
}

// nameSegment is what precedes the first "-<version>" in stem that ends
// the stem or is followed by "-".
func nameSegment(stem, version string) (string, bool) {
	sep := "-" + version
	for from := 0; ; {
		i := strings.Index(stem[from:], sep)
		if i < 0 {
			return "", false
		}
		end := from + i + len(sep)
		if end == len(stem) || stem[end] == '-' {
			return stem[:from+i], true
		}
		from += i + 1
	}
}

// matchesLayout reports whether the file name begins with the metadata's
// Name and Version exactly as written, followed by "-" for a wheel or an
// egg and by the extension for an sdist. The comparison is literal, as
// JFrog's table shows: jfrog_pypi-1.0.0.tar.gz is refused for a package
// named jfrog-pypi, and its text calls the check strict (pypi-repositories,
// "Use PyPI Enforce Layout").
func matchesLayout(file string, d *Dist) bool {
	t, ext, _ := typeOf(file)
	prefix := d.Name + "-" + d.Version
	if t == Sdist {
		return file == prefix+ext
	}
	return strings.HasPrefix(file, prefix+"-")
}
