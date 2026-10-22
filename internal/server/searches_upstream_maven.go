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
	err := u.walk(ctx, key, func(full string) (bool, error) {
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
	metadata, result, err := u.readMaven(ctx, key, folder, "")
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
		versionFolder := path.Join(folder, version)
		if !u.allowed(key, versionFolder, true) {
			continue
		}
		if err = result.CheckFolder(ctx, versionFolder); err != nil {
			return nil, fmt.Errorf("search version authority: %w", err)
		}
		u.checks = append(
			u.checks,
			func(ctx context.Context) error { return result.CheckFolder(ctx, versionFolder) },
		)
		if integration && (exact || files && v != "") {
			entries, err := u.mavenSnapshots(ctx, key, folder, version)
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

func (u *upstreamSearch) mavenSnapshots(
	ctx context.Context,
	key, folder, version string,
) ([]versionEntry, error) {
	metadata, result, err := u.readMaven(ctx, key, folder, version)
	if err != nil {
		return nil, err
	}
	var out []versionEntry
	for _, snapshot := range metadata.Versioning.Snapshots {
		filename := path.Base(folder) + "-" + snapshot.Value
		if snapshot.Classifier != "" {
			filename += "-" + snapshot.Classifier
		}
		full := path.Join(folder, version, filename+"."+snapshot.Extension)
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
) (maven.SourceMetadata, source.Result, error) {
	var empty maven.SourceMetadata
	if !u.allowed(key, path.Join(folder, version), true) {
		return empty, source.Result{}, store.ErrNotFound
	}
	if u.r.s.cfg.RemoteSources == nil {
		return empty, source.Result{}, source.ErrUnsupported
	}
	u.reads++
	if u.reads > upstreamMetadataLimit {
		return empty, source.Result{}, invalidSearchMetadata()
	}
	g, a := layout.Org(path.Dir(folder)), path.Base(folder)
	metadataPath := path.Join(folder, version, "maven-metadata.xml")
	q := u.request(key)
	q.Resource = source.Resource{
		Kind:                source.Metadata,
		Method:              http.MethodGet,
		Path:                metadataPath,
		PermissionFolder:    true,
		PermissionPath:      path.Join(folder, version),
		Variant:             "search-maven-metadata-v1",
		ExpectedContentType: "application/xml",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
			metadata, err := readSearchMaven(body, g, a, version)
			if err == nil {
				err = validateSearchSnapshots(metadata, u.ls.layouts[key], folder, version)
			}
			return err
		},
	}
	result, err := u.r.s.cfg.RemoteSources.Read(ctx, q)
	if err != nil {
		return empty, result, fmt.Errorf("read search metadata: %w", err)
	}
	if result.Stream == nil {
		return empty, result, source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	metadata, err := readSearchMaven(result.Stream.Body, g, a, version)
	if err == nil {
		err = validateSearchSnapshots(metadata, u.ls.layouts[key], folder, version)
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
