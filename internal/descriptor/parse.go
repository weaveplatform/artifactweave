// Package descriptor reads Artifactory's YAML configuration descriptor and
// applies it (decision 0009): the body of PATCH /api/system/configuration,
// `artifactweave config apply` and the bootstrap descriptor. Named entities
// that do not exist are created, existing ones are updated field by field,
// omitted keys are untouched, and a key whose value is null is deleted.
package descriptor

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrInvalid reports a descriptor that cannot be applied; its message names
// the first problem. Entry points answer it with 400.
var ErrInvalid = errors.New("invalid configuration descriptor")

// Sections artifactweave applies. Every other top-level section is accepted
// and reported as ignored.
const (
	SectionLocal        = "localRepositories"
	SectionRemote       = "remoteRepositories"
	SectionVirtual      = "virtualRepositories"
	SectionProxies      = "proxies"
	SectionPropertySets = "propertySets"
	SectionLayouts      = "repoLayouts"
)

// repoSections maps the repository sections to their class, in the order
// they are applied: a virtual repository may list members created by the
// same patch.
var repoSections = []struct {
	name  string
	class repo.Class
}{
	{SectionLocal, repo.Local},
	{SectionRemote, repo.Remote},
	{SectionVirtual, repo.Virtual},
}

// configSections are the named sections stored as configuration documents,
// with the fields each accepts and which of them are secrets (sealed at rest
// and never part of the stored document).
var configSections = []struct {
	name    string
	kind    types.ConfigKind
	fields  []string
	secrets []string
}{
	{SectionProxies, types.ConfigProxy, []string{
		"host", "port", "username", "password", "domain", "ntHost", "redirectedToHosts",
		"platformDefault", "services",
	}, []string{"password"}},
	{SectionPropertySets, types.ConfigPropertySet, []string{"properties", "visible"}, nil},
	{SectionLayouts, types.ConfigLayout, []string{
		"artifactPathPattern", "distinctiveDescriptorPathPattern", "descriptorPathPattern",
		"folderIntegrationRevisionRegExp", "fileIntegrationRevisionRegExp",
	}, nil},
}

// ownNames are the fields in which an entry repeats its own key: the
// artifactory_repository_layout resource sends name inside
// repoLayouts.<name> on every create and update. The key already is the
// name, so the field is checked and dropped rather than stored twice; a
// stored copy would make the same layout written with and without it
// compare as changed.
var ownNames = map[string]string{SectionLayouts: "name"}

// Patch is a parsed descriptor.
type Patch struct {
	Repos   []RepoChange
	Configs []ConfigChange
	// Ignored names the top-level sections accepted without effect. They
	// are not stored: sections such as mailServer and security carry
	// passwords, which must never reach the database unsealed.
	Ignored []string
}

// RepoChange is one entry of a repository section.
type RepoChange struct {
	Section string
	Class   repo.Class
	Key     string
	Delete  bool
	// Fields uses the JSON API's names, so updates go through the same
	// merge as the repositories API; a JSON null resets a field.
	Fields map[string]json.RawMessage
}

// ConfigChange is one entry of a proxies, propertySets or repoLayouts
// section.
type ConfigChange struct {
	Section string
	Kind    types.ConfigKind
	Key     string
	Delete  bool
	// Fields are the descriptor's own names; a nil value removes the field.
	Fields  map[string]any
	secrets []string
}

// Lookup resolves an environment variable, as os.LookupEnv does.
type Lookup func(name string) (string, bool)

// Parse reads a descriptor. String values of the form ${NAME} are resolved
// through env; an unset variable is an error rather than an empty value,
// which would silently clear a password.
func Parse(doc []byte, env Lookup) (*Patch, error) {
	var top map[string]any
	if err := yaml.Unmarshal(doc, &top); err != nil {
		return nil, fmt.Errorf("%w: not a YAML mapping: %w", ErrInvalid, err)
	}
	// Artifactory treats an empty document as "delete every repository".
	if len(top) == 0 {
		return nil, fmt.Errorf(
			"%w: the document is empty; delete entries one by one with a null value",
			ErrInvalid,
		)
	}
	resolved, err := resolveEnv(top, env)
	if err != nil {
		return nil, err
	}
	top, _ = resolved.(map[string]any)
	p := &Patch{}
	for _, rs := range repoSections {
		entries, err := section(top, rs.name)
		if err != nil {
			return nil, err
		}
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			c, err := repoChange(rs.name, rs.class, key, entries[key])
			if err != nil {
				return nil, err
			}
			p.Repos = append(p.Repos, c)
		}
		delete(top, rs.name)
	}
	for _, cs := range configSections {
		entries, err := section(top, cs.name)
		if err != nil {
			return nil, err
		}
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			c := ConfigChange{Section: cs.name, Kind: cs.kind, Key: key, secrets: cs.secrets}
			switch v := entries[key].(type) {
			case nil:
				c.Delete = true
			case map[string]any:
				if err := dropOwnName(cs.name, key, v); err != nil {
					return nil, err
				}
				if bad := unknown(v, cs.fields); bad != "" {
					return nil, fmt.Errorf(
						"%w: %s.%s: unknown field %s",
						ErrInvalid,
						cs.name,
						key,
						bad,
					)
				}
				c.Fields = v
			default:
				return nil, fmt.Errorf(
					"%w: %s.%s must be a mapping or null",
					ErrInvalid,
					cs.name,
					key,
				)
			}
			p.Configs = append(p.Configs, c)
		}
		delete(top, cs.name)
	}
	p.Ignored = slices.Sorted(maps.Keys(top))
	return p, nil
}

// section returns the entries of a named section. A whole section set to
// null would delete everything in it; Artifactory has no such form, so it is
// refused rather than guessed at.
func section(top map[string]any, name string) (map[string]any, error) {
	v, ok := top[name]
	if !ok {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(
			"%w: %s must be a mapping of names to entries; to delete one, set it to null",
			ErrInvalid,
			name,
		)
	}
	return m, nil
}

// dropOwnName removes the field in which an entry names itself. A null
// passes, as a repository's key: null does. A different name is refused
// rather than read as a rename: the entry key is what the descriptor
// identifies the layout by, and nothing documents which of the two wins.
func dropOwnName(sectionName, key string, entry map[string]any) error {
	field, ok := ownNames[sectionName]
	if !ok {
		return nil
	}
	if v := entry[field]; v != nil && v != key {
		return fmt.Errorf(
			"%w: %s.%s: %s %v does not match the entry key",
			ErrInvalid,
			sectionName,
			key,
			field,
			v,
		)
	}
	delete(entry, field)
	return nil
}

func unknown(m map[string]any, allowed []string) string {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !slices.Contains(allowed, k) {
			return k
		}
	}
	return ""
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// resolveEnv replaces ${NAME} references in every string value (not in
// keys).
func resolveEnv(v any, env Lookup) (any, error) {
	switch t := v.(type) {
	case string:
		var missing string
		out := envRef.ReplaceAllStringFunc(t, func(ref string) string {
			name := envRef.FindStringSubmatch(ref)[1]
			val, ok := env(name)
			if !ok && missing == "" {
				missing = name
			}
			return val
		})
		if missing != "" {
			return nil, fmt.Errorf("%w: environment variable %s is not set", ErrInvalid, missing)
		}
		return out, nil
	case map[string]any:
		for k, e := range t {
			r, err := resolveEnv(e, env)
			if err != nil {
				return nil, err
			}
			t[k] = r
		}
		return t, nil
	case []any:
		for i, e := range t {
			r, err := resolveEnv(e, env)
			if err != nil {
				return nil, err
			}
			t[i] = r
		}
		return t, nil
	default:
		return v, nil
	}
}

// Renamed fields: the descriptor's spelling to the JSON API's.
var renamed = map[string]string{
	"type":       "packageType",
	"repoLayout": "repoLayoutRef",
}

// Blocks are the descriptor's nested groups of fields that the JSON API
// keeps flat: block sub-field to JSON field. vcs.git is nested once more.
var blocks = map[string]map[string]string{
	"xray": {"enabled": "xrayIndex"},
	"helm": {"chartsBaseUrl": "chartsBaseUrl"},
	"nuget": {
		"feedContextPath": "feedContextPath", "downloadContextPath": "downloadContextPath",
		"v3FeedUrl": "v3FeedUrl", "symbolServerUrl": "symbolServerUrl",
	},
	"externalDependencies": {
		"enabled": "externalDependenciesEnabled", "patterns": "externalDependenciesPatterns",
		"remoteRepo": "externalDependenciesRemoteRepo",
	},
	"bower":    {"bowerRegistryUrl": "bowerRegistryUrl"},
	"composer": {"composerRegistryUrl": "composerRegistryUrl"},
	"pypi": {
		"pyPIRegistryUrl": "pyPIRegistryUrl", "registryUrl": "pyPIRegistryUrl",
		"repositorySuffix": "pyPIRepositorySuffix", "pyPIRepositorySuffix": "pyPIRepositorySuffix",
	},
	"virtualCacheConfig": {"virtualRetrievalCachePeriodSecs": "virtualRetrievalCachePeriodSecs"},
	"vcs":                {"type": "vcsType"},
	"vcs.git":            {"provider": "vcsGitProvider", "downloadUrl": "vcsGitDownloadUrl"},
}

// jsonFields are the JSON API's repository field names, which the
// descriptor also accepts as written.
var jsonFields = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeFor[repo.Config]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		out[name] = true
	}
	return out
}()

func repoChange(sectionName string, class repo.Class, key string, v any) (RepoChange, error) {
	c := RepoChange{Section: sectionName, Class: class, Key: key}
	where := sectionName + "." + key
	switch entry := v.(type) {
	case nil:
		c.Delete = true
		return c, nil
	case map[string]any:
		fields := map[string]any{}
		for _, name := range slices.Sorted(maps.Keys(entry)) {
			if err := addField(fields, where, name, entry[name]); err != nil {
				return c, err
			}
		}
		if k, ok := fields["key"]; ok && k != nil && k != key {
			return c, fmt.Errorf(
				"%w: %s: key %v does not match the entry name",
				ErrInvalid,
				where,
				k,
			)
		}
		if rc, ok := fields["rclass"]; ok && rc != nil &&
			!strings.EqualFold(fmt.Sprint(rc), string(class)) {
			return c, fmt.Errorf(
				"%w: %s: rclass %v does not belong in %s",
				ErrInvalid,
				where,
				rc,
				sectionName,
			)
		}
		fields["key"], fields["rclass"] = key, string(class)
		c.Fields = map[string]json.RawMessage{}
		for name, val := range fields {
			raw, err := json.Marshal(val)
			if err != nil {
				return c, fmt.Errorf("%w: %s.%s: %w", ErrInvalid, where, name, err)
			}
			c.Fields[name] = raw
		}
		return c, nil
	default:
		return c, fmt.Errorf("%w: %s must be a mapping or null", ErrInvalid, where)
	}
}

// addField maps one descriptor field onto the JSON names in out.
func addField(out map[string]any, where, name string, v any) error {
	if to, ok := renamed[name]; ok {
		out[to] = v
		return nil
	}
	if sub, ok := blocks[name]; ok {
		return addBlock(out, where+"."+name, name, sub, v)
	}
	if !jsonFields[name] {
		return fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, name)
	}
	out[name] = v
	return nil
}

// addBlock flattens a nested block. A null block resets every field in it.
func addBlock(out map[string]any, where, name string, sub map[string]string, v any) error {
	m, ok := v.(map[string]any)
	if v != nil && !ok {
		return fmt.Errorf("%w: %s must be a mapping", ErrInvalid, where)
	}
	if v == nil {
		for _, to := range sub {
			out[to] = nil
		}
		if nested, ok := blocks[name+".git"]; ok {
			for _, to := range nested {
				out[to] = nil
			}
		}
		return nil
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if nested, ok := blocks[name+"."+k]; ok {
			if err := addBlock(out, where+"."+k, name+"."+k, nested, m[k]); err != nil {
				return err
			}
			continue
		}
		to, ok := sub[k]
		if !ok {
			return fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, k)
		}
		out[to] = m[k]
	}
	return nil
}
