package sqlcommon

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// purposeRepoPassword is the sealing purpose of a remote repository's
// password; the row id is the repository key.
const purposeRepoPassword = "repositories.password" // #nosec G101 -- a column name, not a credential

// Repos implements store.Store.
func (s *Store) Repos() store.RepoStore { return repos{s} }

// Items implements store.Store.
func (s *Store) Items() store.ItemStore { return items{s} }

// Uploads implements store.Store.
func (s *Store) Uploads() store.UploadStore { return uploads{s} }

// Trash implements store.Store.
func (s *Store) Trash() store.TrashStore { return trash{s} }

// Packages implements store.Store.
func (s *Store) Packages() store.PackageStore { return packages{s} }

type repos struct{ s *Store }

func (r repos) Create(ctx context.Context, c repo.Config) error {
	if c.Key == "" {
		return fmt.Errorf("create repository: key is required: %w", store.ErrInvalid)
	}
	return r.s.Tx(ctx, func(ctx context.Context) error {
		doc, err := repoDoc(c)
		if err != nil {
			return err
		}
		now := time.Now().UnixMicro()
		if _, err := r.s.exec(
			ctx,
			"create repository "+c.Key,
			Insert(
				"repositories",
				[]string{
					"key",
					"rclass",
					"package_type",
					"project_key",
					"created_at",
					"updated_at",
					"doc",
					"header_secrets_version",
				},
			),
			c.Key,
			string(c.Class),
			string(c.PackageType),
			c.ProjectKey,
			now,
			now,
			doc,
			1,
		); err != nil {
			return err
		}
		if err := r.writeChildren(ctx, c); err != nil {
			return err
		}
		return remoteRecords(
			r,
		).PutSource(ctx, c.Key, types.RemoteSourceState{Source: store.NewRemoteSource(c.Key)})
	})
}

// repoDoc keeps secret values out of the document without changing the caller's configuration.
func repoDoc(c repo.Config) (string, error) {
	c.Password = ""
	c.CustomHTTPHeaders = maskedRepositoryHeaders(c.CustomHTTPHeaders)
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode repository %q: %w", c.Key, err)
	}
	return string(b), nil
}

// writeChildren replaces sealed credentials and the mirrored member and key
// pair rows of c.
func (r repos) writeChildren(ctx context.Context, c repo.Config) error {
	for _, table := range []string{"repository_secrets", "repository_members", "repository_keypairs"} {
		col := "repo_key"
		if table == "repository_members" {
			col = "virtual_key"
		}
		if _, err := r.s.exec(
			ctx,
			"clear "+table,
			"DELETE FROM "+table+" WHERE "+col+" = ?",
			c.Key,
		); err != nil {
			return err
		}
	}
	if c.Password != "" {
		sealed, err := r.s.seal(purposeRepoPassword, c.Key, c.Password)
		if err != nil {
			return err
		}
		if _, err := r.s.exec(
			ctx,
			"store repository password",
			Insert(
				"repository_secrets",
				[]string{"repo_key", "password"},
			),
			c.Key,
			sealed,
		); err != nil {
			return err
		}
	}
	if err := r.writeHeaderSecrets(ctx, c); err != nil {
		return err
	}
	if c.Class == repo.Virtual {
		for i, m := range c.Repositories {
			if _, err := r.s.exec(ctx, "store repository member",
				Insert("repository_members", []string{"virtual_key", "member_key", "position"}),
				c.Key, m, i); err != nil {
				return err
			}
		}
	}
	for role, name := range map[types.KeypairRole]string{
		types.KeypairPrimary: c.PrimaryKeyPairRef, types.KeypairSecondary: c.SecondaryKeyPairRef,
	} {
		if name == "" {
			continue
		}
		if _, err := r.s.exec(ctx, "store repository key pair",
			Insert("repository_keypairs", []string{"repo_key", "role", "keypair_name"}),
			c.Key, string(role), name); err != nil {
			return err
		}
	}
	return nil
}

func (r repos) Get(ctx context.Context, key string) (repo.Config, error) {
	configs, err := r.readConfigs(ctx, "r.key = ?", []any{key})
	if err != nil {
		return repo.Config{}, err
	}
	if len(configs) == 0 {
		return repo.Config{}, fmt.Errorf("get repository %s: %w", key, store.ErrNotFound)
	}
	return configs[0], nil
}

func decodeRepo(doc string) (repo.Config, error) {
	var c repo.Config
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		return c, fmt.Errorf("decode repository: %w", err)
	}
	return c, nil
}

func (r repos) Update(ctx context.Context, c repo.Config) error {
	return r.s.Tx(ctx, func(ctx context.Context) error {
		state, err := remoteRecords(r).Source(ctx, c.Key)
		if err != nil {
			return err
		}
		old, err := r.Get(ctx, c.Key)
		if err != nil {
			return err
		}
		doc, err := repoDoc(c)
		if err != nil {
			return err
		}
		if err := r.s.execOne(
			ctx,
			"update repository "+c.Key,
			"UPDATE repositories SET rclass = ?, package_type = ?, project_key = ?, updated_at = ?, doc = ?, header_secrets_version = 1 WHERE key = ?",
			string(
				c.Class,
			),
			string(c.PackageType),
			c.ProjectKey,
			time.Now().UnixMicro(),
			doc,
			c.Key,
		); err != nil {
			return err
		}
		if err := r.writeChildren(ctx, c); err != nil {
			return err
		}
		if store.RemotePolicyChanged(old, c) {
			if store.RemoteConfigChanged(old, c) {
				state.Source.Generation = types.SourceGeneration(rand.Text())
				state.ConnectionFailures = 0
				state.OfflineUntil = time.Time{}
				state.RetryAfterUntil = time.Time{}
			}
			state.PolicyRevision++
			state.Revision++
			return remoteRecords(r).PutSource(ctx, c.Key, state)
		}
		return nil
	})
}

func (r repos) Delete(ctx context.Context, key string) error {
	return r.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := r.s.RemoteCache().Invalidate(ctx, key, "", false); err != nil {
			return fmt.Errorf("remove remote cache: %w", err)
		}
		return r.s.execOne(
			ctx,
			"delete repository "+key,
			"DELETE FROM repositories WHERE key = ?",
			key,
		)
	})
}

func (r repos) List(ctx context.Context, f store.RepoFilter) ([]repo.Config, error) {
	where := "1 = 1"
	var args []any
	for col, v := range map[string]string{"r.rclass": string(f.Class), "r.package_type": string(f.PackageType), "r.project_key": f.ProjectKey} {
		if v != "" {
			where += " AND " + col + " = ?"
			args = append(args, v)
		}
	}
	return r.readConfigs(ctx, where, args)
}

func maskedRepositoryHeaders(headers []repo.CustomHTTPHeader) []repo.CustomHTTPHeader {
	out := slices.Clone(headers)
	for i := range out {
		if out[i].Sensitive {
			out[i].Value = ""
		}
	}
	return out
}

func (r repos) Including(ctx context.Context, key string) ([]string, error) {
	return r.keys(
		ctx,
		"virtual repositories including "+key,
		"SELECT DISTINCT virtual_key FROM repository_members WHERE member_key = ? ORDER BY virtual_key",
		key,
	)
}

func (r repos) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	return r.keys(
		ctx,
		"repositories using key pair "+name,
		"SELECT DISTINCT repo_key FROM repository_keypairs WHERE keypair_name = ? ORDER BY repo_key",
		name,
	)
}

// keys runs a query returning one key per row.
func (r repos) keys(ctx context.Context, op, q string, arg string) ([]string, error) {
	var out []string
	err := r.s.rows(ctx, op, q, []any{arg}, func(rs *sql.Rows) error {
		var k string
		err := rs.Scan(&k)
		out = append(out, k)
		return err //nolint:wrapcheck // wrapped by rows
	})
	return out, err
}
