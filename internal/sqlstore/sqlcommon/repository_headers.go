package sqlcommon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const purposeRepoHeader = "repositories.http_header"

var errRepositoryHeader = errors.New(
	"sqlcommon: repository sensitive header storage is inconsistent",
)

func repositoryHeaderID(key string, position int, name string) string {
	// Length-prefix the repository key so separators in identifiers cannot move
	// ciphertext to a different repository/header tuple with the same AAD.
	return strconv.Itoa(len(key)) + ":" + key + ":" + strconv.Itoa(position) + ":" + name
}

func (r repos) writeHeaderSecrets(ctx context.Context, c repo.Config) error {
	if _, err := r.s.exec(
		ctx,
		"replace repository header secrets",
		"DELETE FROM repository_header_secrets WHERE repo_key = ?",
		c.Key,
	); err != nil {
		return err
	}
	for i, h := range c.CustomHTTPHeaders {
		if !h.Sensitive {
			continue
		}
		sealed, err := r.s.seal(purposeRepoHeader, repositoryHeaderID(c.Key, i, h.Name), h.Value)
		if err != nil {
			return err
		}
		// Retain an explicit NULL row for an intentionally empty sensitive header.
		// A missing row must fail closed instead of silently dropping authentication.
		if _, err = r.s.exec(
			ctx,
			"store repository header secret",
			Insert("repository_header_secrets", []string{"repo_key", "position", "name", "value"}),
			c.Key,
			i,
			h.Name,
			sealed,
		); err != nil {
			return err
		}
	}
	return nil
}

// Read the document and every secret in one SQL snapshot. Separate queries can
// mix an old document with newly rotated/reordered headers under READ COMMITTED.
func (r repos) readConfigs(ctx context.Context, where string, args []any) ([]repo.Config, error) {
	query := `SELECT r.key, r.doc, r.header_secrets_version, s.password, h.position, h.name, h.value
 FROM repositories r LEFT JOIN repository_secrets s ON s.repo_key = r.key
 LEFT JOIN repository_header_secrets h ON h.repo_key = r.key WHERE ` + where + ` ORDER BY r.key, h.position`
	var out []repo.Config
	last := ""
	read, expected := 0, 0
	err := r.s.rows(ctx, "read repository configurations", query, args, func(rs *sql.Rows) error {
		var key, doc string
		var version int
		var password, value []byte
		var position sql.NullInt64
		var name sql.NullString
		if err := rs.Scan(&key, &doc, &version, &password, &position, &name, &value); err != nil {
			return err //nolint:wrapcheck // wrapped by rows
		}
		if version != 1 {
			return errRepositoryHeader
		}
		if len(out) == 0 || key != last {
			if read != expected {
				return errRepositoryHeader
			}
			config, err := decodeRepo(doc)
			if err != nil {
				return err
			}
			config.Password, err = r.s.unseal(purposeRepoPassword, key, password)
			if err != nil {
				return err
			}
			expected, read = 0, 0
			for _, h := range config.CustomHTTPHeaders {
				if h.Sensitive {
					expected++
				}
			}
			out = append(out, config)
			last = key
		}
		if !position.Valid {
			return nil
		}
		c := &out[len(out)-1]
		if position.Int64 < 0 || position.Int64 >= int64(len(c.CustomHTTPHeaders)) {
			return errRepositoryHeader
		}
		header := &c.CustomHTTPHeaders[position.Int64]
		if !name.Valid || !header.Sensitive || header.Name != name.String || header.Value != "" {
			return errRepositoryHeader
		}
		plain, err := r.s.unseal(
			purposeRepoHeader,
			repositoryHeaderID(key, int(position.Int64), name.String),
			value,
		)
		if err != nil {
			return err
		}
		header.Value = plain
		read++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if read != expected {
		return nil, errRepositoryHeader
	}
	return out, nil
}

// Schema version 0016 marks legacy rows for a key-aware data migration. The
// marker and values commit with the redacted document, so a failed or interrupted
// conversion is retried at the next open. No key material enters SQL migrations.
func (s *Store) backfillRepositoryHeaders(ctx context.Context) (int, error) {
	changed := 0
	for {
		var keys []string
		err := s.rows(
			ctx,
			"find legacy repository headers",
			`SELECT key FROM repositories WHERE header_secrets_version = 0 ORDER BY key LIMIT 128`,
			nil,
			func(rows *sql.Rows) error {
				var key string
				if err := rows.Scan(&key); err != nil {
					return err //nolint:wrapcheck // wrapped by rows
				}
				keys = append(keys, key)
				return nil
			},
		)
		if err != nil {
			return changed, err
		}
		if len(keys) == 0 {
			return changed, nil
		}
		for _, key := range keys {
			n, err := s.backfillRepositoryHeader(ctx, key)
			changed += n
			if err != nil {
				return changed, err
			}
		}
	}
}

func (s *Store) backfillRepositoryHeader(ctx context.Context, key string) (int, error) {
	changed := 0
	err := s.Tx(ctx, func(tx context.Context) error {
		var doc string
		var version int
		err := s.q(tx).
			QueryRowContext(tx, s.d.Rebind(`SELECT doc, header_secrets_version FROM repositories WHERE key = ?`+s.lockClause(tx)), key).
			Scan(&doc, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return s.wrap("read legacy repository headers", err)
		}
		if version != 0 {
			return nil
		}
		config, err := decodeRepo(doc)
		if err != nil {
			return err
		}
		config.Key = key
		if err = s.verifyRepositoryHeaderKey(tx, config); err != nil {
			return err
		}
		if err = (repos{s}).writeHeaderSecrets(tx, config); err != nil {
			return err
		}
		redacted, err := redactRepositoryHeaderDocument(doc, config.CustomHTTPHeaders)
		if err != nil {
			return err
		}
		if err = s.execOne(
			tx,
			"redact legacy repository headers",
			`UPDATE repositories SET doc = ?, header_secrets_version = 1 WHERE key = ?`,
			redacted,
			key,
		); err != nil {
			return err
		}
		for _, h := range config.CustomHTTPHeaders {
			if h.Sensitive && h.Value != "" {
				changed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}

func redactRepositoryHeaderDocument(doc string, headers []repo.CustomHTTPHeader) (string, error) {
	if len(headers) == 0 {
		return doc, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &object); err != nil {
		return "", fmt.Errorf("decode legacy repository document: %w", err)
	}
	object["customHttpHeaders"] = json.RawMessage(
		mustJSON(maskedRepositoryHeaders(headers)).(string),
	)
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("redact legacy repository document: %w", err)
	}
	return string(encoded), nil
}

// An existing sealed repository password is evidence of the required key. Do
// not commit legacy header plaintext under an unrelated or accidentally omitted
// key when that evidence is available.
func (s *Store) verifyRepositoryHeaderKey(ctx context.Context, c repo.Config) error {
	hasSecret := false
	for _, header := range c.CustomHTTPHeaders {
		if header.Sensitive && header.Value != "" {
			hasSecret = true
			break
		}
	}
	if !hasSecret {
		return nil
	}
	var password []byte
	err := s.q(ctx).
		QueryRowContext(ctx, s.d.Rebind("SELECT password FROM repository_secrets WHERE repo_key = ?"), c.Key).
		Scan(&password)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return s.wrap("verify legacy repository sealing key", err)
	}
	_, err = s.unseal(purposeRepoPassword, c.Key, password)
	return err
}
