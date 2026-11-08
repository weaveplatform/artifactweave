package nuget

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The nuget.* properties the format sets on each indexed .nupkg from its
// nuspec, the keys Artifactory 6.20's NuGetProperties defines (decision
// 0038, "Properties"). The feeds read the index, never these, so editing
// one changes no feed, and a reindex writes it back.
const (
	// PropID is the nuspec's id with its case.
	PropID = "nuget.id"
	// PropVersion is the normalised version. [I] Whether 7.x writes the
	// normalised or the nuspec's version is unknown (#216).
	PropVersion      = "nuget.version"
	PropTitle        = "nuget.title"
	PropAuthors      = "nuget.authors"
	PropOwners       = "nuget.owners"
	PropSummary      = "nuget.summary"
	PropDescription  = "nuget.description"
	PropCopyright    = "nuget.copyright"
	PropReleaseNotes = "nuget.releaseNotes"
	PropProjectURL   = "nuget.projectUrl"
	PropLicenseURL   = "nuget.licenseUrl"
	PropIconURL      = "nuget.iconUrl"
	// PropTags is the nuspec's space-separated string.
	PropTags = "nuget.tags"
	// PropRequireLicenseAcceptance is "true" or "false".
	PropRequireLicenseAcceptance = "nuget.requireLicenseAcceptance"
	// PropDigest is the package's SHA-512 in base64, as the v2 feed's
	// PackageHash gives it. [I] 7.x's value is unknown (#216).
	PropDigest = "nuget.digest"
	// PropDependency has one value per dependency, "<id>:<range>:<target
	// framework>", the framework empty outside a <group>, as RTFACT-31474
	// shows 7.x writing it.
	PropDependency = "nuget.dependency"
)

// propertyKeys are every key the format sets, so a reindex can remove the
// ones a nuspec no longer gives.
var propertyKeys = []string{
	PropID, PropVersion, PropTitle, PropAuthors, PropOwners, PropSummary, PropDescription,
	PropCopyright, PropReleaseNotes, PropProjectURL, PropLicenseURL, PropIconURL, PropTags,
	PropRequireLicenseAcceptance, PropDigest, PropDependency,
}

// properties are the nuget.* properties of the package n describes, stored
// as it. An element n lacks, or leaves empty, gives no property, and each
// value is cut as the store would cut it (types.PropertyValueMax), so a
// comparison with what the store holds sees no change where there is none.
func properties(n *Nuspec, it types.Item) types.Properties {
	out := types.Properties{}
	one := func(k, v string) {
		if v != "" {
			out[k] = []string{types.TruncatePropertyValue(v, types.PropertyValueMax)}
		}
	}
	one(PropID, n.ID)
	one(PropVersion, n.Version.Normalized())
	one(PropTitle, n.Title)
	one(PropAuthors, n.Authors)
	one(PropOwners, n.Owners)
	one(PropSummary, n.Summary)
	one(PropDescription, n.Description)
	one(PropCopyright, n.Copyright)
	one(PropReleaseNotes, n.ReleaseNotes)
	one(PropProjectURL, n.ProjectURL)
	one(PropLicenseURL, n.LicenseURL)
	one(PropIconURL, n.IconURL)
	one(PropTags, n.Tags)
	one(PropDigest, digest(it.Actual.SHA512))
	if n.RequireLicenseAcceptance != nil {
		out[PropRequireLicenseAcceptance] = []string{
			strconv.FormatBool(*n.RequireLicenseAcceptance),
		}
	}
	// Sorted and without repeats, as a property's values are a set.
	var deps []string
	for _, g := range n.DependencyGroups {
		for _, d := range g.Dependencies {
			deps = append(deps, types.TruncatePropertyValue(
				d.ID+":"+d.Range+":"+g.TargetFramework, types.PropertyValueMax))
		}
	}
	slices.Sort(deps)
	if deps = slices.Compact(deps); len(deps) > 0 {
		out[PropDependency] = deps
	}
	return out
}

// setProperties sets on it the nuget.* properties of the package n
// describes, at at, removing those n no longer gives.
func setProperties(
	ctx context.Context,
	st store.Store,
	it types.Item,
	n *Nuspec,
	at time.Time,
) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("nuget: properties of %s: %w", it.FullPath(), err)
	}
	return patchProperties(ctx, st, it, props, n, at)
}

// patchProperties is setProperties for an item whose properties are props.
// Properties a user set, the JFrog CLI's build.* among them, are left
// alone, and nothing is written when nothing changes. They are written
// straight to the store, which runs no Annotated hook: the index does not
// follow them.
func patchProperties(
	ctx context.Context,
	st store.Store,
	it types.Item,
	props types.Properties,
	n *Nuspec,
	at time.Time,
) error {
	want := properties(n, it)
	set := types.Properties{}
	var remove []string
	for _, k := range propertyKeys {
		v, ok := want[k]
		_, had := props[k]
		switch {
		case ok && !slices.Equal(sorted(props[k]), v):
			set[k] = v
		case !ok && had:
			remove = append(remove, k)
		}
	}
	if len(set) == 0 && len(remove) == 0 {
		return nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, remove, at); err != nil {
		return fmt.Errorf("nuget: properties of %s: %w", it.FullPath(), err)
	}
	return nil
}

// sorted is values sorted, whatever order the store keeps them in.
func sorted(values []string) []string {
	return slices.Sorted(slices.Values(values))
}
