// Package repos holds the repository operations that more than one entry
// point performs: the repositories API, the configuration descriptor
// (PATCH /api/system/configuration, config apply and the bootstrap
// descriptor). Its errors are apierror values, so every entry point answers
// a bad configuration with the same status and message.
package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MsgKeyExists answers a create whose key is taken, compared without case.
const MsgKeyExists = "Case insensitive repository key already exists"

func badRequest(format string, a ...any) error {
	return apierror.New(http.StatusBadRequest, fmt.Sprintf(format, a...))
}

// protected cannot be deleted: Artifactory's trash can and support
// bundle repositories, and the built-in build-info repository, which the
// repositories API cannot create again (its package type is in no allowed
// list). jfrog-support-bundle is singular, as JFrog's reserved names list
// and 7.x's own logs in its Jira (RTFACT-22258, RTFACT-21053) spell it; the
// delete page's example key JFrog-Support-Bundles is the odd one out.
var protected = []string{"auto-trashcan", "jfrog-support-bundle", types.BuildInfoRepository}

func Protected(key string) bool {
	return strings.TrimSpace(key) == "" ||
		slices.ContainsFunc(
			protected,
			func(p string) bool { return strings.EqualFold(p, key) },
		)
}

// PrepareCreate applies defaults and validates c for creation against the
// keys already taken (stored and earlier in the same batch).
func PrepareCreate(c repo.Config, existing []repo.Config) (repo.Config, error) {
	repo.Defaults(&c)
	if err := repo.Validate(&c); err != nil {
		return c, badRequest("%s", err.Error())
	}
	if slices.ContainsFunc(
		existing,
		func(e repo.Config) bool { return strings.EqualFold(e.Key, c.Key) },
	) {
		return c, badRequest(MsgKeyExists)
	}
	return c, nil
}

// CheckMembers rejects a virtual repository whose members do not exist
// among known, or have a package type the virtual repository cannot
// aggregate (repo.AcceptsMember). The default deployment repository is a
// member (repo.Validate), so it is covered too.
//
// The type check holds afterwards without a re-check: a repository's package
// type cannot change (Merge), and Remove takes a deleted repository out of
// every virtual repository, so a repository created again under its key
// joins none until a write names it, which this check sees.
func CheckMembers(c repo.Config, known []repo.Config) error {
	if c.Class != repo.Virtual {
		return nil
	}
	for _, m := range c.Repositories {
		i := slices.IndexFunc(known, func(k repo.Config) bool { return k.Key == m })
		if i < 0 {
			return badRequest("Virtual repository member '%s' does not exist", m)
		}
		if t := known[i].PackageType; !repo.AcceptsMember(c.PackageType, t) {
			return badRequest(
				"Virtual repository member '%s' has package type '%s', which does not match "+
					"the virtual repository's package type '%s'",
				m, t, c.PackageType)
		}
	}
	return nil
}

// CheckKeypairs rejects signing key references (primaryKeyPairRef,
// secondaryKeyPairRef) to key pairs that do not exist: a repository would
// otherwise fail only when it first signs.
func CheckKeypairs(ctx context.Context, keys store.KeyStore, c repo.Config) error {
	for _, ref := range []string{c.PrimaryKeyPairRef, c.SecondaryKeyPairRef} {
		if ref == "" {
			continue
		}
		_, err := keys.Keypair(ctx, ref)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return badRequest("Key pair '%s' does not exist", ref)
		case err != nil:
			return fmt.Errorf("repos: key pair %s: %w", ref, err)
		}
	}
	return nil
}

// CheckEnvironments applies Artifactory's rule for a repository's
// environments (decision 0013). Outside a project they are ignored, so none
// are stored. In a project an empty list becomes DEV, and every entry must be
// DEV, PROD, a global environment or one of the project's own environments,
// which are named "<projectKey>-<name>". Names compare without regard to case
// and are stored in the environment's own spelling, because renaming or
// deleting an environment finds its repositories by that spelling.
func CheckEnvironments(ctx context.Context, projects store.ProjectStore, c *repo.Config) error {
	if c.ProjectKey == "" {
		c.Environments = nil
		return nil
	}
	if len(c.Environments) == 0 {
		c.Environments = []string{types.EnvironmentDev}
	}
	known := slices.Clone(types.BuiltinEnvironments)
	global, err := projects.Environments(ctx, "")
	if err != nil {
		return fmt.Errorf("repos: global environments: %w", err)
	}
	for _, e := range global {
		known = append(known, e.Name)
	}
	scoped, err := projects.Environments(ctx, c.ProjectKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("repos: environments of project %s: %w", c.ProjectKey, err)
	}
	for _, e := range scoped {
		known = append(known, projectEnvironment(c.ProjectKey, e.Name))
	}
	out := make([]string, 0, len(c.Environments))
	for _, name := range c.Environments {
		i := slices.IndexFunc(known, func(k string) bool { return strings.EqualFold(k, name) })
		if i < 0 {
			return badRequest("Environment '%s' does not exist", name)
		}
		if !slices.Contains(out, known[i]) {
			out = append(out, known[i])
		}
	}
	c.Environments = out
	return nil
}

// projectEnvironment is the name a repository uses for a project
// environment: "<projectKey>-<name>", as JFrog names them (the platform
// provider's resource_project_environment). A stored name that already
// carries the prefix is used as it is.
func projectEnvironment(projectKey, name string) string {
	if strings.HasPrefix(strings.ToLower(name), strings.ToLower(projectKey)+"-") {
		return name
	}
	return projectKey + "-" + name
}

// Merge merges an update body into the stored configuration with
// Artifactory's rules: a field present replaces the stored value, a field
// left out keeps it, and null resets it to its default. The key, class and
// package type cannot change. A key that differs from the stored one only in
// case is a change too: the store finds a repository by its exact key, so
// accepting the respelling would fail the write after validation (#472).
func Merge(stored repo.Config, patch map[string]json.RawMessage) (repo.Config, error) {
	for field, current := range map[string]string{
		"key": stored.Key, "rclass": string(stored.Class), "packageType": string(stored.PackageType),
	} {
		raw, ok := patch[field]
		if !ok || IsNull(raw) {
			delete(patch, field)
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return stored, badRequest("The repository %s cannot be changed", field)
		}
		// Empty is no key, class or package type, so it changes nothing; a
		// re-encoded config carries rclass this way when it was left out.
		if v == "" {
			delete(patch, field)
			continue
		}
		same := strings.EqualFold(v, current)
		if field == "key" {
			same = v == current
		}
		if !same {
			return stored, badRequest("The repository %s cannot be changed", field)
		}
	}
	base, err := json.Marshal(stored)
	if err != nil {
		return stored, fmt.Errorf("encode repository: %w", err)
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &doc); err != nil {
		return stored, fmt.Errorf("decode repository: %w", err)
	}
	for field, raw := range patch {
		if IsNull(raw) {
			delete(doc, field)
		} else {
			doc[field] = raw
		}
	}
	merged, err := json.Marshal(doc)
	if err != nil {
		return stored, fmt.Errorf("encode update: %w", err)
	}
	var out repo.Config
	if err := json.Unmarshal(merged, &out); err != nil {
		return stored, badRequest("Invalid repository configuration: %s", err.Error())
	}
	repo.RestoreSecrets(stored, &out)
	repo.Defaults(&out)
	// An update cannot choose a key, so the key rules would only refuse
	// names the server took for itself, such as artifactory-build-info.
	if err := repo.ValidateStored(&out); err != nil {
		return stored, badRequest("%s", err.Error())
	}
	return out, nil
}

func IsNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// Remove deletes key with its content, in one transaction: the
// items of the repository and, for a remote, its cache (dropping their blob
// references for garbage collection to reclaim), the root records that hold
// their own properties, its package index, its place in the virtual
// repositories that list it, and its configuration. found is false when no
// configuration exists.
//
// Deleted content does not pass through the trash can yet; that arrives with
// the trash can itself (#48).
func Remove(ctx context.Context, st store.Store, key string) (int64, bool, error) {
	c, err := st.Repos().Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, true, fmt.Errorf("get repository: %w", err)
	}
	var files int64
	err = st.Tx(ctx, func(ctx context.Context) error {
		caches := []bool{false}
		if c.Class == repo.Remote {
			caches = append(caches, true)
		}
		for _, cached := range caches {
			res, err := st.Items().Delete(ctx, key, cached, "")
			if err != nil {
				return fmt.Errorf("delete content: %w", err)
			}
			files += res.Files
			for sha, n := range res.BlobRefs {
				if _, err := st.Blobs().Unref(ctx, sha, n); err != nil {
					return fmt.Errorf("release blob %s: %w", sha, err)
				}
			}
			// A content delete keeps the root's record, so it goes here: a
			// repository created later under the same key must not inherit
			// the old one's properties.
			if err := st.Items().DeleteRoot(ctx, key, cached); err != nil {
				return fmt.Errorf("delete root: %w", err)
			}
		}
		for {
			pkgs, _, err := st.Packages().List(ctx, key, store.Page{Limit: 100})
			if err != nil {
				return fmt.Errorf("list packages: %w", err)
			}
			if len(pkgs) == 0 {
				break
			}
			for _, p := range pkgs {
				if err := st.Packages().Delete(ctx, p.ID); err != nil {
					return fmt.Errorf("delete package %s: %w", p.Name, err)
				}
			}
		}
		including, err := st.Repos().Including(ctx, key)
		if err != nil {
			return fmt.Errorf("virtual repositories including %s: %w", key, err)
		}
		for _, vk := range including {
			v, err := st.Repos().Get(ctx, vk)
			if err != nil {
				return fmt.Errorf("get %s: %w", vk, err)
			}
			v.Repositories = slices.DeleteFunc(
				v.Repositories,
				func(m string) bool { return m == key },
			)
			if v.DefaultDeploymentRepo == key {
				v.DefaultDeploymentRepo = ""
			}
			if err := st.Repos().Update(ctx, v); err != nil {
				return fmt.Errorf("update %s: %w", vk, err)
			}
		}
		if err := st.Repos().Delete(ctx, key); err != nil {
			return fmt.Errorf("delete configuration: %w", err)
		}
		return nil
	})
	if err != nil {
		return files, true, fmt.Errorf("delete repository %s: %w", key, err)
	}
	return files, true, nil
}
