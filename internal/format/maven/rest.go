package maven

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// CalculateMetadata does calculateMetadata's work on folder in c, a local
// repository of the Maven group (decision 0036, #282). In one transaction
// it brings the package index of the files under folder, or directly in it
// when recursive is false, back in line with the tree, as a reindex would,
// then queues the metadata of each package those files belong to, and of
// the package and group each maven-metadata.xml there describes, so a file
// with nothing left to describe is removed. The work runs when the queue
// reaches it, and a read of the metadata waits for it, so the answer need
// not wait: Artifactory's also answers once its calculation is queued.
func CalculateMetadata(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	folder string,
	recursive bool,
) error {
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return err
	}
	folder = strings.Trim(folder, "/")
	criteria := files(c)
	switch {
	case !recursive:
		criteria = append(criteria, &aql.FieldCriterion{
			Field: aql.FieldPath, Op: aql.OpEq, Text: cmp.Or(folder, types.RootPath),
		})
	case folder != "":
		criteria = append(criteria, under([]string{folder + "/"}))
	}
	h := Handler{}
	err = env.Store.Tx(ctx, func(ctx context.Context) error {
		hits, err := env.Store.Items().Search(ctx, &aql.Query{Criteria: criteria}, aql.Options{})
		if err != nil {
			return fmt.Errorf("maven: files under %s: %w", folder, err)
		}
		refs := map[format.WorkRef]bool{}
		versions := map[string]Coordinates{}
		for _, hit := range hits {
			p := hit.Item.FullPath()
			if IsMetadata(p) {
				for _, ref := range h.Owed(c, false, p) {
					refs[ref] = true
				}
				continue
			}
			if co, ok := r.read(p); ok {
				versions[co.Package+" "+co.Version] = co
			}
		}
		// The folder's own metadata, which may be all a folder without
		// files of its own has left to remove.
		for _, ref := range h.Owed(c, false, strings.TrimPrefix(folder+"/"+MetadataFile, "/")) {
			refs[ref] = true
		}
		for _, k := range slices.Sorted(maps.Keys(versions)) {
			co := versions[k]
			if err := syncVersion(ctx, env, c, r, false, co); err != nil {
				return err
			}
			refs[format.WorkRef{Kind: KindMetadata, Key: workKey(c.Key, co.Package)}] = true
		}
		for _, ref := range slices.SortedFunc(maps.Keys(refs), func(a, b format.WorkRef) int {
			return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Key, b.Key))
		}) {
			if err := env.Enqueue(ctx, ref.Kind, ref.Key, nil); err != nil {
				return fmt.Errorf("maven: queue %s of %s: %w", ref.Kind, ref.Key, err)
			}
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// generatedDescription is the description of a default POM, as
// Artifactory's MavenPomBuilder writes it.
const generatedDescription = "Artifactory auto generated POM"

// GeneratePOM returns where generatePom writes the POM of the file jar in
// c, and what it writes (decision 0036, #282): the jar's own
// META-INF/maven/<groupId>/<artifactId>/pom.xml for the coordinates its
// path gives, or a default POM. The path is the jar's descriptor path in
// the repository's layout, without its classifier, as the POM of a
// classifier artifact is its main artifact's. A file the layout reads no
// coordinates from, or a POM, is refused with 400.
func GeneratePOM(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	jar types.Item,
) (string, []byte, error) {
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return "", nil, err
	}
	full := jar.FullPath()
	co, ok := r.read(full)
	if !ok || IsPom(full) {
		return "", nil, format.Rejectf(
			http.StatusBadRequest,
			"Cannot generate a POM for '%s': the repository layout reads no Maven coordinates from it",
			full,
		)
	}
	at, err := pomPath(r.l, full)
	if err != nil {
		return "", nil, format.Rejectf(http.StatusBadRequest,
			"Cannot generate a POM for '%s': the repository layout has no place for one: %v",
			full, err)
	}
	body, err := embeddedPOM(ctx, env, jar, co.Org, co.Module)
	if err != nil {
		return "", nil, err
	}
	if body == nil {
		body = defaultPOM(co.Org, co.Module, co.Version, co.Ext)
	}
	return at, body, nil
}

// pomPath is the path the POM of the file at full takes in l: its
// descriptor path when the layout's descriptor is a POM, as Maven's is, and
// otherwise its own path with the extension pom.
func pomPath(l *layout.Layout, full string) (string, error) {
	v, err := l.Match(full)
	if err != nil {
		return "", err //nolint:wrapcheck // the caller names the file
	}
	delete(v, layout.TokenClassifier)
	v[layout.TokenExt] = "pom"
	if p, err := l.BuildDescriptor(v); err == nil && IsPom(p) {
		return p, nil
	}
	return l.Build(v) //nolint:wrapcheck // the caller names the file
}

// embeddedPOM reads the POM a jar carries for g:a, nil when it carries
// none or is no archive. A shaded jar carries its dependencies' POMs too,
// so the one for the jar's own coordinates is taken, where 6.20's
// getPomFileAsStringFromJar takes the first it meets.
func embeddedPOM(ctx context.Context, env format.Env, jar types.Item, g, a string) ([]byte, error) {
	rc, info, err := env.Blobs.Open(ctx, jar.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("maven: open %s: %w", jar.FullPath(), err)
	}
	defer func() { _ = rc.Close() }()
	want := "META-INF/maven/" + g + "/" + a + "/pom.xml"
	_, body, err := archive.ReadFile(archive.ReaderAt(rc), info.Size, archive.Zip,
		archive.DefaultLimits, func(name string) bool { return name == want })
	switch {
	case errors.Is(err, archive.ErrNotFound), errors.Is(err, archive.ErrUnusable):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("maven: read %s: %w", jar.FullPath(), err)
	}
	return body, nil
}

// defaultPOM is the POM MavenPomBuilder writes for coordinates with no POM
// of their own: the model version, the coordinates, the packaging the
// file's extension names, and Artifactory's description.
func defaultPOM(g, a, v, packaging string) []byte {
	w := &xmlWriter{}
	w.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	w.b.WriteString(`<project xmlns="http://maven.apache.org/POM/4.0.0" ` +
		`xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" ` +
		`xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 ` +
		`http://maven.apache.org/xsd/maven-4.0.0.xsd">` + "\n")
	w.text(1, "modelVersion", "4.0.0")
	w.text(1, "groupId", g)
	w.text(1, "artifactId", a)
	w.text(1, "version", v)
	w.text(1, "packaging", packaging)
	w.text(1, "description", generatedDescription)
	w.b.WriteString("</project>\n")
	return w.b.Bytes()
}
