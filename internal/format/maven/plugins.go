package maven

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// pluginPOM is what the plugin metadata reads from a POM: its own elements,
// not those it inherits, as Artifactory's MavenModelUtils.stringToMavenModel
// reads them. Inspect's pom (pom.go) fills in the parent's instead.
type pluginPOM struct {
	XMLName    xml.Name `xml:"project"`
	GroupID    string   `xml:"groupId"`
	ArtifactID string   `xml:"artifactId"`
	Version    string   `xml:"version"`
	Packaging  string   `xml:"packaging"`
	Name       string   `xml:"name"`
}

// id is Maven's Model.getId: groupId:artifactId:packaging:version, with
// "[inherited]" for what the POM leaves to its parent and jar, the model's
// default, for no packaging.
func (p pluginPOM) id() string {
	inherited := func(s string) string {
		if s == "" {
			return "[inherited]"
		}
		return s
	}
	packaging := p.Packaging
	if packaging == "" {
		packaging = "jar"
	}
	return inherited(p.GroupID) + ":" + p.ArtifactID + ":" + packaging + ":" + inherited(p.Version)
}

var (
	mavenWord  = regexp.MustCompile(`-?maven-?`)
	pluginWord = regexp.MustCompile(`-?plugin-?`)
)

// goalPrefix is Maven's PluginDescriptor.getGoalPrefixFromArtifactId:
// maven-clean-plugin and clean-maven-plugin both give clean.
func goalPrefix(artifactID string) string {
	if artifactID == "maven-plugin-plugin" {
		return "plugin"
	}
	return pluginWord.ReplaceAllString(mavenWord.ReplaceAllString(artifactID, ""), "")
}

// groupDir is the folder c's layout keeps group g's packages in, without
// its slash; empty when the layout has none.
func groupDir(l *layout.Layout, g string) string {
	p := l.Prefixes(layout.Values{layout.TokenOrg: g})[0]
	if !strings.HasSuffix(p, "/") {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

// writePluginMetadata rebuilds group g's plugin metadata in c: one plugin
// per artifactId among the POMs PluginProperty marks in the group's folder,
// as MavenPluginsMetadataCalculator writes it. It is written only when it
// changes, and removed only when the group has no plugin left (DV-051).
func writePluginMetadata(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r reader,
	g string,
) error {
	dir := groupDir(r.l, g)
	if dir == "" {
		return nil
	}
	q := &aql.Query{Criteria: append(
		files(c),
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: dir + "/*"},
		&aql.PropertyCriterion{
			Part:  aql.PartPair,
			Key:   PluginProperty,
			Op:    aql.OpEq,
			Value: "true",
		},
	)}
	hits, err := env.Store.Items().Search(ctx, q, aql.Options{})
	if err != nil {
		return fmt.Errorf("maven: plugin POMs of %s: %w", g, err)
	}
	// Of a plugin's versions, the highest names it, where Artifactory takes
	// whichever POM its search returns first.
	poms := map[string]types.Item{}
	for _, h := range hits {
		p := h.Item.FullPath()
		if h.Item.Cached || !IsPom(p) || path.Dir(path.Dir(path.Dir(p))) != dir {
			continue
		}
		artifact := path.Base(path.Dir(path.Dir(p)))
		cur, seen := poms[artifact]
		if !seen || types.CompareMercury(versionFolder(p), versionFolder(cur.FullPath())) > 0 {
			poms[artifact] = h.Item
		}
	}
	m := metadata{}
	for _, artifact := range slices.Sorted(maps.Keys(poms)) {
		pl, ok, err := pluginOf(ctx, env, poms[artifact])
		if err != nil {
			return err
		}
		if ok {
			m.Plugins = append(m.Plugins, pl)
		}
	}
	target := dir + "/" + MetadataFile
	if len(m.Plugins) == 0 {
		return removeDerived(
			ctx,
			env,
			c,
			target,
			func(m metadata) bool { return m.Versioning == nil },
		)
	}
	old, it, err := stored(ctx, env, c, target)
	if err != nil {
		return err
	}
	if it.ID != 0 && bytes.Equal(old.bytes(), m.bytes()) {
		return nil
	}
	return derive(ctx, env, c, target, m)
}

func versionFolder(p string) string { return path.Base(path.Dir(p)) }

// pluginOf reads a plugin's entry from its POM; ok is false for a POM that
// does not parse, which names no plugin.
func pluginOf(ctx context.Context, env format.Env, it types.Item) (plugin, bool, error) {
	body, err := content(ctx, env, it)
	if err != nil {
		return plugin{}, false, err
	}
	var p pluginPOM
	if xml.Unmarshal(body, &p) != nil || p.ArtifactID == "" {
		return plugin{}, false, nil
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = "Unnamed - " + p.id()
	}
	return plugin{Name: name, Prefix: goalPrefix(p.ArtifactID), ArtifactID: p.ArtifactID}, true, nil
}
