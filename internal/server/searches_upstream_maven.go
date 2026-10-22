package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func (u *upstreamSearch) mavenVersions(
	ctx context.Context,
	key, v string,
	files bool,
) ([]versionEntry, error) {
	if u.g != "" && u.a != "" && !hasWildcard(u.g+u.a) {
		if !searchMavenSegment(u.g) || !searchMavenSegment(u.a) {
			return nil, source.ErrInvalidRequest
		}
		return u.mavenModule(ctx, key, path.Join(layout.OrgPath(u.g), u.a), v, files)
	}
	var out []versionEntry
	gp, ap := wildcard(&u.g), wildcard(&u.a)
	err := u.walk(ctx, key, true, func(full string) (bool, error) {
		if path.Base(full) != "maven-metadata.xml" {
			return false, nil
		}
		folder := path.Dir(full)
		g, a := layout.Org(path.Dir(folder)), path.Base(folder)
		if !matches(gp, g) || !matches(ap, a) {
			return false, nil
		}
		versions, err := u.mavenModule(ctx, key, folder, v, files)
		if errors.Is(err, store.ErrNotFound) {
			return true, nil
		}
		out = append(out, versions...)
		// A group-level plugin-prefix metadata document has no versions. Keep
		// walking below it when a wildcard can also match that directory name.
		return len(versions) > 0, err
	})
	return out, err
}

func (u *upstreamSearch) mavenModule(
	ctx context.Context,
	key, folder, v string,
	files bool,
) ([]versionEntry, error) {
	d, err := u.mapping(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []versionEntry
	g, a := layout.Org(path.Dir(folder)), path.Base(folder)
	for _, scope := range d.Scopes(layout.Values{layout.TokenOrg: g, layout.TokenModule: a}) {
		if !scope.Allows(u.request(key)) {
			continue
		}
		found, err := u.mavenModuleScope(ctx, key, folder, v, files, d, scope)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

func (u *upstreamSearch) mavenModuleScope(
	ctx context.Context,
	key, folder, v string,
	files bool,
	d *remote.Discovery,
	scope remote.DiscoveryScope,
) ([]versionEntry, error) {
	metadata, result, err := u.readMaven(ctx, key, folder, "", scope)
	if err != nil {
		return nil, err
	}
	var out []versionEntry
	exact := v != "" && !hasWildcard(v)
	for _, version := range metadata.Versioning.Versions {
		integration := strings.HasSuffix(version, "-SNAPSHOT")
		if exact && (!integration || version != v) {
			continue
		}
		allowed, err := u.mavenVersionAuthority(ctx, key, folder, version, d, scope, result)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		if integration && (exact || files && v != "") {
			entries, err := u.mavenSnapshots(ctx, key, folder, version, scope)
			if errors.Is(err, store.ErrNotFound) {
				out = append(out, versionEntry{version, true})
				continue
			}
			if err != nil {
				if failure := u.failure(key, err); failure != nil {
					return nil, failure
				}
				continue
			}
			out = append(out, entries...)
		} else {
			out = append(out, versionEntry{version, integration})
		}
	}
	return out, nil
}

// A complete canonical version folder can authorize a version identity from
// authoritative metadata. Filename-only versions and descriptor-only grants
// need actual permitted files; they must never borrow a fabricated POM path.
func (u *upstreamSearch) mavenVersionAuthority(
	ctx context.Context,
	key, folder, version string,
	d *remote.Discovery,
	scope remote.DiscoveryScope,
	result source.Result,
) (bool, error) {
	g, a := layout.Org(path.Dir(folder)), path.Base(folder)
	if vscope, ok := d.VersionScope(g, a, version); ok && u.allowed(key, vscope.Path, true) {
		err := result.CheckFolder(ctx, vscope.Path)
		if err == nil {
			u.checks = append(
				u.checks,
				func(ctx context.Context) error { return result.CheckFolder(ctx, vscope.Path) },
			)
			return true, nil
		}
		if !errors.Is(err, source.ErrDenied) {
			return false, fmt.Errorf("search version authority: %w", err)
		}
	}
	listing, err := remote.ListMappedFolder(
		ctx,
		u.r.s.cfg.RemoteSources,
		u.request(key),
		d,
		scope,
		path.Join(folder, version),
		&u.budget,
	)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("search version files: %w", err)
	}
	u.checks = append(u.checks, listing.Revalidate)
	for _, entry := range listing.Files {
		co, ok := d.Canonical().Read(entry.Path)
		if ok && co.Org == g && co.Module == a && co.Version() == version {
			return true, nil
		}
	}
	return false, nil
}

func (u *upstreamSearch) mavenSnapshots(
	ctx context.Context,
	key, folder, version string,
	scope remote.DiscoveryScope,
) ([]versionEntry, error) {
	metadata, result, err := u.readMaven(ctx, key, folder, version, scope)
	if err != nil {
		return nil, err
	}
	var out []versionEntry
	for _, snapshot := range metadata.Versioning.Snapshots {
		filename := path.Base(folder) + "-" + snapshot.Value
		if snapshot.Classifier != "" {
			filename += "-" + snapshot.Classifier
		}
		upstream := path.Join(folder, version, filename+"."+snapshot.Extension)
		full, mapped, err := u.mappings[key].MapFile(upstream)
		if err != nil {
			return nil, fmt.Errorf("map search snapshot: %w", err)
		}
		if !mapped {
			return nil, invalidSearchMetadata()
		}
		co, ok := u.ls.layouts[key].Read(full)
		if !ok || !co.Integration() || co.Version() != version {
			return nil, invalidSearchMetadata()
		}
		if !u.allowed(key, full, false) {
			continue
		}
		if err = result.CheckRead(ctx, full); err != nil {
			return nil, fmt.Errorf("search snapshot authority: %w", err)
		}
		u.checks = append(
			u.checks,
			func(ctx context.Context) error { return result.CheckRead(ctx, full) },
		)
		out = append(out, versionEntry{co.FileVersion(), true})
	}
	// Old Maven repositories may publish only timestamp/buildNumber. That is a
	// version identity, not proof of any particular extension or classifier.
	if len(metadata.Versioning.Snapshots) == 0 {
		identity := version
		snapshot := metadata.Versioning.Snapshot
		if snapshot.Timestamp != "" || snapshot.BuildNumber != 0 {
			identity = fmt.Sprintf(
				"%s%s-%d",
				strings.TrimSuffix(version, "SNAPSHOT"),
				snapshot.Timestamp,
				snapshot.BuildNumber,
			)
		}
		out = append(out, versionEntry{identity, true})
	}
	return out, nil
}

func (u *upstreamSearch) readMaven(
	ctx context.Context,
	key, folder, version string,
	scope remote.DiscoveryScope,
) (maven.SourceMetadata, source.Result, error) {
	var empty maven.SourceMetadata
	if !scope.Allows(u.request(key)) {
		return empty, source.Result{}, store.ErrNotFound
	}
	if u.r.s.cfg.RemoteSources == nil {
		return empty, source.Result{}, source.ErrUnsupported
	}
	u.reads++
	if u.reads > upstreamMetadataLimit {
		return empty, source.Result{}, invalidSearchMetadata()
	}
	d, err := u.mapping(ctx, key)
	if err != nil {
		return empty, source.Result{}, err
	}
	g, a := layout.Org(path.Dir(folder)), path.Base(folder)
	metadataPath := path.Join(folder, version, "maven-metadata.xml")
	q := u.request(key)
	q.Resource = source.Resource{
		Kind:             source.Metadata,
		Method:           http.MethodGet,
		Path:             metadataPath,
		PermissionFolder: true,
		PermissionPath:   scope.Path,
		Variant:          "search-maven-metadata-v1",
	}
	canonical, upstream := d.Canonical().Definition(), d.Upstream().Definition()
	canonical.Name, upstream.Name = "", ""
	if canonical != upstream || scope.Root {
		q.Resource, err = d.MetadataResource(scope, metadataPath, "search-maven-metadata-v2")
		if err != nil {
			return empty, source.Result{}, fmt.Errorf("map search metadata: %w", err)
		}
	}
	q.Resource.ExpectedContentType = "application/xml"
	q.Resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
		metadata, err := readSearchMaven(body, g, a, version)
		if err == nil {
			err = validateMappedSearchSnapshots(metadata, d, folder, version)
		}
		return err
	}
	result, err := d.Read(ctx, u.r.s.cfg.RemoteSources, q)
	if err != nil {
		return empty, result, fmt.Errorf("read search metadata: %w", err)
	}
	if result.Stream == nil {
		return empty, result, source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	metadata, err := readSearchMaven(result.Stream.Body, g, a, version)
	if err == nil {
		err = validateMappedSearchSnapshots(metadata, d, folder, version)
	}
	if err != nil {
		return empty, result, err
	}
	u.entries += len(metadata.Versioning.Versions) + len(metadata.Versioning.Snapshots)
	if u.entries > 10000 {
		return empty, result, invalidSearchMetadata()
	}
	u.checks = append(u.checks, result.Revalidate)
	return metadata, result, nil
}

func validateMappedSearchSnapshots(
	metadata maven.SourceMetadata,
	d *remote.Discovery,
	folder, version string,
) error {
	if err := validateSearchSnapshots(metadata, d.Upstream(), folder, version); err != nil {
		return err
	}
	for _, snapshot := range metadata.Versioning.Snapshots {
		name := path.Base(folder) + "-" + snapshot.Value
		if snapshot.Classifier != "" {
			name += "-" + snapshot.Classifier
		}
		if _, ok, err := d.MapFile(
			path.Join(folder, version, name+"."+snapshot.Extension),
		); err != nil {
			return fmt.Errorf("validate mapped snapshot: %w", err)
		} else if !ok {
			return invalidSearchMetadata()
		}
	}
	return nil
}

func validateSearchSnapshots(
	metadata maven.SourceMetadata,
	l *layout.Layout,
	folder, version string,
) error {
	for _, snapshot := range metadata.Versioning.Snapshots {
		name := path.Base(folder) + "-" + snapshot.Value
		if snapshot.Classifier != "" {
			name += "-" + snapshot.Classifier
		}
		co, ok := l.Read(path.Join(folder, version, name+"."+snapshot.Extension))
		if !ok || !co.Integration() || co.Version() != version ||
			co.FileVersion() != snapshot.Value {
			return invalidSearchMetadata()
		}
	}
	return nil
}

func searchGroupMetadata(metadata maven.SourceMetadata, g, a string) bool {
	group := a
	if g != "" && g != "." {
		group = g + "." + a
	}
	return metadata.ArtifactID == "" && metadata.Version == "" &&
		len(metadata.Versioning.Versions) == 0 && len(metadata.Versioning.Snapshots) == 0 &&
		metadata.Versioning.Snapshot == (maven.SourceSnapshot{}) &&
		metadata.Versioning.Latest == "" && metadata.Versioning.Release == "" &&
		metadata.GroupID == group
}

func readSearchMaven(body io.Reader, g, a, version string) (maven.SourceMetadata, error) {
	metadata, err := maven.ReadSourceMetadata(body)
	if err != nil {
		return metadata, invalidSearchMetadata()
	}
	if version == "" && searchGroupMetadata(metadata, g, a) {
		return metadata, nil
	}
	if metadata.GroupID != "" && metadata.GroupID != g ||
		metadata.ArtifactID != "" && metadata.ArtifactID != a ||
		metadata.Version != version || len(metadata.Versioning.Versions)+len(metadata.Versioning.Snapshots) > 10000 {
		return metadata, invalidSearchMetadata()
	}
	for _, v := range metadata.Versioning.Versions {
		if !searchMavenSegment(v) {
			return metadata, invalidSearchMetadata()
		}
	}
	for _, snapshot := range metadata.Versioning.Snapshots {
		if !searchMavenSegment(snapshot.Value) || !searchMavenSegment(snapshot.Extension) ||
			snapshot.Classifier != "" && !searchMavenSegment(snapshot.Classifier) ||
			!strings.HasPrefix(snapshot.Value, strings.TrimSuffix(version, "SNAPSHOT")) {
			return metadata, invalidSearchMetadata()
		}
	}
	if snapshot := metadata.Versioning.Snapshot; snapshot.Timestamp != "" ||
		snapshot.BuildNumber != 0 {
		if _, err := time.Parse(
			"20060102.150405",
			snapshot.Timestamp,
		); err != nil ||
			snapshot.BuildNumber < 1 {
			return metadata, invalidSearchMetadata()
		}
	}
	return metadata, nil
}

func searchMavenSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\%?#;[]*?\r\n\x00")
}

func invalidSearchMetadata() error {
	return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
}
