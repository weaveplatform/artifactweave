package maven

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"

	"github.com/weaveplatform/artifactweave/internal/format"
)

// PluginProperty marks a POM whose packaging is maven-plugin, as
// Artifactory's MavenPomInterceptor does (PropertiesService
// .MAVEN_PLUGIN_PROPERTY_NAME); the plugin-level metadata (#279) lists
// them.
const PluginProperty = "artifactory.maven.mavenPlugin"

// maxPOM bounds what a POM read takes in. Artifactory reads any size; a
// POM is a few kilobytes, and anything near this is not one.
const maxPOM = 16 << 20

// Why content is no POM, besides XML that does not parse.
var (
	errTooLarge   = errors.New("the POM is larger than 16 MiB")
	errNotProject = errors.New("expected root element 'project'")
	errNoBlobs    = errors.New("maven: no blob store to read the POM from")
	errEncoding   = errors.New("unsupported encoding")
)

// pom is what Inspect reads from a POM: the coordinates Artifactory's POM
// consistency check compares with the path (PomTargetPathValidator), and
// the packaging and name the plugin marking and the package index keep.
type pom struct {
	// SHA256 is the content it was read from.
	SHA256 string
	// Err is why the content is no POM. Artifactory refuses such a file only
	// while the checks are on, so it is the check's to report, not Inspect's.
	Err error
	// GroupID and Version fall back to the parent's, as Maven's do.
	GroupID, ArtifactID, Version, Packaging, Name string
}

// pomXML is the part of Maven's POM model the checks read.
type pomXML struct {
	XMLName    xml.Name  `xml:""` // any root; parsePOM requires project
	GroupID    string    `xml:"groupId"`
	ArtifactID string    `xml:"artifactId"`
	Version    string    `xml:"version"`
	Packaging  string    `xml:"packaging"`
	Name       string    `xml:"name"`
	Parent     pomParent `xml:"parent"`
}

// pomParent is the part of a POM's parent element the checks read.
type pomParent struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
}

// readPOM reads the POM stored under sha256. Only a failure to read the
// store is an error; content that is no POM comes back with its Err set.
func readPOM(ctx context.Context, blobs format.Blobs, sha256 string) (*pom, error) {
	if blobs == nil {
		return nil, errNoBlobs
	}
	rc, _, err := blobs.Open(ctx, sha256)
	if err != nil {
		return nil, fmt.Errorf("maven: open POM %s: %w", sha256, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxPOM+1))
	if err != nil {
		return nil, fmt.Errorf("maven: read POM %s: %w", sha256, err)
	}
	p := parsePOM(b)
	p.SHA256 = sha256
	return p, nil
}

// parsePOM reads a POM's coordinates. Maven's own reader, which Artifactory
// uses in strict mode, also refuses elements outside the POM model; this
// one refuses only content that is not XML with a project root (DV-060).
func parsePOM(b []byte) *pom {
	if len(b) > maxPOM {
		return &pom{Err: errTooLarge}
	}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	// Artifactory 7.x reads a POM in the encoding its XML declaration
	// names: SaaS 7.110.5 fixed the upload and copy of an ISO-8859-1 POM
	// (RTDEV-56287), which 6.20 read as UTF-8 and refused.
	d.CharsetReader = pomCharset
	var x pomXML
	if err := d.Decode(&x); err != nil {
		return &pom{Err: err}
	}
	if x.XMLName.Local != "project" {
		return &pom{Err: fmt.Errorf("%w but found '%s'", errNotProject, x.XMLName.Local)}
	}
	trim := strings.TrimSpace
	return &pom{
		GroupID:    trim(orParent(x.GroupID, x.Parent.GroupID)),
		ArtifactID: trim(x.ArtifactID),
		Version:    trim(orParent(x.Version, x.Parent.Version)),
		Packaging:  trim(x.Packaging),
		Name:       trim(x.Name),
	}
}

// pomCharset decodes a POM whose declaration names an encoding other than
// UTF-8, which encoding/xml reads itself. The name is looked up as an IANA
// charset name, as Java's are, and then as a WHATWG label, which knows
// spellings such as "utf8" and "latin1"; an encoding neither knows makes
// the content no POM.
func pomCharset(label string, r io.Reader) (io.Reader, error) {
	enc, err := ianaindex.IANA.Encoding(label)
	if err != nil || enc == nil {
		if enc, err = htmlindex.Get(label); err != nil {
			return nil, fmt.Errorf("%w %q", errEncoding, label)
		}
	}
	return enc.NewDecoder().Reader(r), nil
}

// orParent is the first of a and b that is not blank.
func orParent(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// problem is Artifactory's complaint about p stored at target, or "" when
// it has none: the messages of PomTargetPathValidator.validate, which
// Artifactory answers 409 on a deploy while the checks are on. integration
// is whether the layout reads target as an integration (snapshot) file.
func (p *pom) problem(target string, integration bool) string {
	if p.Err != nil {
		return fmt.Sprintf("Failed to read POM for '%s': %v.", target, p.Err)
	}
	// A POM without a groupId, its own or its parent's, is not checked.
	if p.GroupID == "" {
		return ""
	}
	if p.Version == "" {
		return fmt.Sprintf(
			"The Pom version of '%s' does not exists. Please verify your POM content for correctness",
			target,
		)
	}
	// A snapshot path whose POM names a version other than a -SNAPSHOT one,
	// such as a timestamped build, is compared up to its artifact's folder.
	prefix := strings.ReplaceAll(p.GroupID, ".", "/") + "/" + p.ArtifactID + "/"
	if !integration || IsSnapshotVersion(p.Version) {
		prefix += p.Version
	}
	if strings.Contains(prefix, "${") || hasPrefixFold(target, prefix) {
		return ""
	}
	return fmt.Sprintf("The target deployment path '%s' does not match the POM's expected path "+
		"prefix '%s'. Please verify your POM content for correctness and make sure the source "+
		"path is a valid Maven repository root path.", target, prefix)
}

// isPlugin reports a POM with maven-plugin packaging.
func (p *pom) isPlugin() bool { return p.Err == nil && p.Packaging == "maven-plugin" }

// hasPrefixFold is StringUtils.startsWithIgnoreCase.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
