package descriptor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/repos"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Result names what a patch changed, as "section.key".
type Result struct {
	Created, Updated, Deleted, Unchanged []string
	// Ignored are the sections accepted without effect.
	Ignored []string
	// DryRun is set when nothing was written.
	DryRun bool
}

// String is the plain-text summary the PATCH endpoint and `config apply`
// print.
func (r Result) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("Dry run: the configuration is valid and nothing was written.\n")
	} else {
		b.WriteString("Applied the configuration descriptor.\n")
	}
	for _, l := range []struct {
		label string
		names []string
	}{
		{"Created", r.Created},
		{"Updated", r.Updated},
		{"Deleted", r.Deleted},
		{"Unchanged", r.Unchanged},
		{"Ignored (accepted without effect)", r.Ignored},
	} {
		if len(l.names) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", l.label, strings.Join(l.names, ", "))
		}
	}
	return b.String()
}

type repoOp struct {
	name   string
	create bool
	cfg    repo.Config
}

type configOp struct {
	name string
	del  bool
	doc  types.ConfigDocument
}

// Apply checks the whole patch against the store's current state, then,
// unless dryRun, writes it in one transaction, so a patch with one bad entry
// changes nothing. Entries that would not change are left alone, which makes
// applying the same descriptor twice a no-op the second time.
func Apply(
	ctx context.Context,
	st store.Store,
	p *Patch,
	now time.Time,
	dryRun bool,
) (Result, error) {
	res := Result{Ignored: p.Ignored, DryRun: dryRun}
	deletes, writes, err := planRepos(ctx, st, p.Repos, &res)
	if err != nil {
		return res, err
	}
	configs, err := planConfigs(ctx, st, p.Configs, now, &res)
	if err != nil {
		return res, err
	}
	if dryRun {
		return res, nil
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		for _, key := range deletes {
			if _, _, err := repos.Remove(ctx, st, key); err != nil {
				return err //nolint:wrapcheck // names the repository
			}
		}
		for _, op := range writes {
			write := st.Repos().Update
			if op.create {
				write = st.Repos().Create
			}
			if err := write(ctx, op.cfg); err != nil {
				return fmt.Errorf("%s: %w", op.name, err)
			}
		}
		for _, op := range configs {
			if op.del {
				err = st.Configs().Delete(ctx, op.doc.Kind, op.doc.Key)
			} else {
				err = st.Configs().Put(ctx, op.doc)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", op.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("apply configuration: %w", err)
	}
	return res, nil
}

// planRepos works out the repository changes against the state the patch
// leaves behind: a virtual repository may list members created, and must not
// list members deleted, by the same patch.
func planRepos(
	ctx context.Context,
	st store.Store,
	changes []RepoChange,
	res *Result,
) ([]string, []repoOp, error) {
	existing, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("list repositories: %w", err)
	}
	stored := map[string]repo.Config{}
	for _, c := range existing {
		stored[c.Key] = c
	}
	final := map[string]repo.Config{}
	maps.Copy(final, stored)
	var deletes []string
	for _, c := range changes {
		name := c.Section + "." + c.Key
		if !c.Delete {
			continue
		}
		cur, ok := stored[c.Key]
		switch {
		case repos.Protected(c.Key):
			return nil, nil, fmt.Errorf(
				"%w: %s: this repository cannot be deleted",
				ErrInvalid,
				name,
			)
		case !ok:
			res.Unchanged = append(res.Unchanged, name)
			continue
		case cur.Class != c.Class:
			return nil, nil, fmt.Errorf(
				"%w: %s: %s is a %s repository",
				ErrInvalid,
				name,
				c.Key,
				cur.Class,
			)
		}
		deletes = append(deletes, c.Key)
		delete(final, c.Key)
		res.Deleted = append(res.Deleted, name)
	}
	var writes []repoOp
	for _, c := range changes {
		if c.Delete {
			continue
		}
		name := c.Section + "." + c.Key
		op, changed, err := planRepo(c, stored, final)
		if err != nil {
			return nil, nil, entryError(name, err)
		}
		switch {
		case !changed:
			res.Unchanged = append(res.Unchanged, name)
		case op.create:
			res.Created = append(res.Created, name)
		default:
			res.Updated = append(res.Updated, name)
		}
		if changed {
			op.name = name
			writes = append(writes, op)
			final[op.cfg.Key] = op.cfg
		}
	}
	known := make([]repo.Config, 0, len(final))
	for _, c := range final {
		known = append(known, c)
	}
	for _, op := range writes {
		if err := repos.CheckMembers(op.cfg, known); err != nil {
			return nil, nil, entryError(op.name, err)
		}
		if err := repos.CheckKeypairs(ctx, st.Keys(), op.cfg); err != nil {
			return nil, nil, entryError(op.name, err)
		}
	}
	return deletes, writes, nil
}

func planRepo(c RepoChange, stored, final map[string]repo.Config) (repoOp, bool, error) {
	if cur, ok := stored[c.Key]; ok {
		// Merge consumes its patch, so it gets a copy.
		updated, err := repos.Merge(cur, maps.Clone(c.Fields))
		if err != nil {
			return repoOp{}, false, err //nolint:wrapcheck // named by entryError
		}
		same, err := sameJSON(cur, updated)
		return repoOp{cfg: updated}, !same, err
	}
	fields := map[string]json.RawMessage{}
	for k, v := range c.Fields {
		// A null on a new repository is its default anyway.
		if !repos.IsNull(v) {
			fields[k] = v
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return repoOp{}, false, fmt.Errorf("encode repository: %w", err)
	}
	var cfg repo.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return repoOp{}, false, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	all := make([]repo.Config, 0, len(final))
	for _, f := range final {
		all = append(all, f)
	}
	cfg, err = repos.PrepareCreate(cfg, all)
	return repoOp{create: true, cfg: cfg}, true, err //nolint:wrapcheck // named by entryError
}

func sameJSON(a, b any) (bool, error) {
	ja, err := json.Marshal(a)
	if err != nil {
		return false, fmt.Errorf("encode: %w", err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false, fmt.Errorf("encode: %w", err)
	}
	return bytes.Equal(ja, jb), nil
}

// entryError names the entry in a validation error, keeping it an
// ErrInvalid so every entry point answers 400. Errors that are not about the
// configuration pass through.
func entryError(name string, err error) error {
	var ae *apierror.Error
	switch {
	case errors.As(err, &ae) && ae.Status < 500:
		return fmt.Errorf("%w: %s: %s", ErrInvalid, name, ae.Message)
	case errors.Is(err, ErrInvalid):
		return fmt.Errorf("%s: %w", name, err)
	default:
		return err
	}
}

func planConfigs(
	ctx context.Context,
	st store.Store,
	changes []ConfigChange,
	now time.Time,
	res *Result,
) ([]configOp, error) {
	var ops []configOp
	for _, c := range changes {
		name := c.Section + "." + c.Key
		cur, err := st.Configs().Get(ctx, c.Kind, c.Key)
		found := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if c.Delete {
			if !found {
				res.Unchanged = append(res.Unchanged, name)
				continue
			}
			res.Deleted = append(res.Deleted, name)
			ops = append(ops, configOp{name: name, del: true, doc: cur})
			continue
		}
		doc, secrets, err := mergeConfig(cur, found, c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := validateConfig(c.Kind, c.Key, doc); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, name, err)
		}
		if found && sameDocument(cur, encoded, secrets) {
			res.Unchanged = append(res.Unchanged, name)
			continue
		}
		if found {
			res.Updated = append(res.Updated, name)
		} else {
			res.Created = append(res.Created, name)
		}
		ops = append(ops, configOp{name: name, doc: types.ConfigDocument{
			Kind:     c.Kind,
			Key:      c.Key,
			Enabled:  true,
			Document: encoded,
			Secrets:  secrets,
			Updated:  now,
		}})
	}
	return ops, nil
}

// mergeConfig applies a change to the stored document field by field.
// Secret fields go to the sealed secrets, never the document.
func mergeConfig(
	cur types.ConfigDocument,
	found bool,
	c ConfigChange,
) (map[string]any, map[string]string, error) {
	doc := map[string]any{}
	secrets := map[string]string{}
	if found {
		if len(cur.Document) > 0 {
			if err := json.Unmarshal(cur.Document, &doc); err != nil {
				return nil, nil, fmt.Errorf("stored document: %w", err)
			}
		}
		maps.Copy(secrets, cur.Secrets)
	}
	for k, v := range c.Fields {
		secret := false
		for _, s := range c.secrets {
			secret = secret || s == k
		}
		switch {
		case secret && v == nil:
			delete(secrets, k)
		case secret:
			s, ok := v.(string)
			if !ok {
				return nil, nil, fmt.Errorf("%w: %s must be a string", ErrInvalid, k)
			}
			secrets[k] = s
		case v == nil:
			delete(doc, k)
		default:
			doc[k] = v
		}
	}
	if len(secrets) == 0 {
		secrets = nil
	}
	return doc, secrets, nil
}

// sameDocument compares through a re-encoding of the stored document, so a
// store that returns it with different whitespace still compares equal.
func sameDocument(cur types.ConfigDocument, encoded []byte, secrets map[string]string) bool {
	var stored any
	if err := json.Unmarshal(cur.Document, &stored); err != nil {
		return false
	}
	again, err := json.Marshal(stored)
	if err != nil {
		return false
	}
	return bytes.Equal(again, encoded) && maps.Equal(cur.Secrets, secrets)
}

func validateConfig(kind types.ConfigKind, key string, doc map[string]any) error {
	switch kind {
	case types.ConfigProxy:
		if host, _ := doc["host"].(string); host == "" {
			return fmt.Errorf("%w: a proxy needs a host", ErrInvalid)
		}
		if port, ok := number(
			doc["port"],
		); !ok || port < 1 || port > 65535 ||
			port != float64(int(port)) {
			return fmt.Errorf("%w: a proxy needs a port between 1 and 65535", ErrInvalid)
		}
	case types.ConfigLayout:
		def := layout.Definition{Name: key}
		strs := map[string]*string{
			"artifactPathPattern":             &def.ArtifactPathPattern,
			"descriptorPathPattern":           &def.DescriptorPathPattern,
			"folderIntegrationRevisionRegExp": &def.FolderIntegrationRevisionRegExp,
			"fileIntegrationRevisionRegExp":   &def.FileIntegrationRevisionRegExp,
		}
		for field, dst := range strs {
			if v, ok := doc[field]; ok {
				s, ok := v.(string)
				if !ok {
					return fmt.Errorf("%w: %s must be a string", ErrInvalid, field)
				}
				*dst = s
			}
		}
		if v, ok := doc["distinctiveDescriptorPathPattern"]; ok {
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf(
					"%w: distinctiveDescriptorPathPattern must be a boolean",
					ErrInvalid,
				)
			}
			def.DistinctiveDescriptorPathPattern = b
		}
		if def.ArtifactPathPattern == "" {
			return fmt.Errorf("%w: a layout needs an artifactPathPattern", ErrInvalid)
		}
		if _, err := layout.Compile(def); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	case types.ConfigPropertySet:
		if v, ok := doc["properties"]; ok {
			props, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: properties must be a mapping of property names", ErrInvalid)
			}
			for name, p := range props {
				if _, ok := p.(map[string]any); p != nil && !ok {
					return fmt.Errorf("%w: property %s must be a mapping", ErrInvalid, name)
				}
			}
		}
	}
	return nil
}

// number reads a YAML integer (int) or a stored JSON number (float64).
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}
