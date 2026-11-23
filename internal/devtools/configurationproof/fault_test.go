package configurationproof

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	platformsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
)

const (
	fixtureMaster   = "configuration-proof-master-key-32-bytes"
	fixturePassword = "proof-private-fixture"
)

type proofFixture struct {
	database string
	evidence string
	db       *sql.DB
}

func newProofFixture(t *testing.T) proofFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sqlite.OpenDB(path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := dialect.Migrate(t.Context(), db, sqlite.Dialect, []dialect.Module{platformsql.Module}, ""); err != nil {
		t.Fatal(err)
	}
	material, err := crypt.ParseVersionedKey(fixtureMaster)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.NewKeyring(material)
	if err != nil {
		t.Fatal(err)
	}
	store := platformsql.New(dialect.New(db, sqlite.Dialect, keys))
	if err := store.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: configdoc.ConfigProxy, Key: "owned", Document: []byte(`{"host":"proxy.invalid","port":3128}`),
		Secrets: map[string]string{"password": fixturePassword},
	}); err != nil {
		t.Fatal(err)
	}
	return proofFixture{database: path, evidence: filepath.Join(t.TempDir(), "proof.json"), db: db}
}

func (f proofFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestAccConfigurationProofRejectsFailuresWithoutPublishingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, phase, master, expected, want, target string
		prepare                                     func(*testing.T, *proofFixture)
	}{
		{name: "unknown phase", phase: "invented", want: "unknown proof phase"},
		{name: "unknown target", target: "backup/owned", want: "unknown proof target"},
		{name: "empty key", target: "mail_server/", want: "unknown proof target"},
		{name: "invalid master", master: "short", want: "invalid fixture master key"},
		{name: "wrong keyring", master: "different-master-key-material-32-bytes", want: "cannot read actual configuration"},
		{name: "missing document", want: "expected configuration is missing", prepare: func(t *testing.T, f *proofFixture) {
			f.exec(t, "DELETE FROM config_documents")
		}},
		{name: "deleted document still present", phase: "deleted", want: "deleted configuration retained document or secret rows"},
		{name: "deleted orphan secret", phase: "deleted", want: "deleted configuration retained document or secret rows", prepare: func(t *testing.T, f *proofFixture) {
			f.db.SetMaxOpenConns(1)
			f.exec(t, "PRAGMA foreign_keys = OFF")
			f.exec(t, "DELETE FROM config_documents")
		}},
		{name: "secret query failure", phase: "deleted", want: "cannot inspect configuration secret storage", prepare: func(t *testing.T, f *proofFixture) {
			f.exec(t, "DELETE FROM config_documents")
			f.exec(t, "DROP TABLE config_secrets")
		}},
		{name: "missing secret", want: "actual server password differs", prepare: func(t *testing.T, f *proofFixture) {
			f.exec(t, "DELETE FROM config_secrets")
		}},
		{name: "clear retained secret", phase: "cleared", want: "password clear retained an actual server secret"},
		{name: "plaintext secret", want: "actual server password is not sealed at rest", prepare: func(t *testing.T, f *proofFixture) {
			f.exec(t, "UPDATE config_secrets SET value = ?", []byte(fixturePassword))
		}},
		{name: "public password field", want: "public configuration document contains secret material", prepare: func(t *testing.T, f *proofFixture) {
			f.exec(t, "UPDATE config_documents SET document = ?", []byte(`{"password":"masked"}`))
		}},
		{name: "public secret value", want: "public configuration document contains secret material", prepare: func(t *testing.T, f *proofFixture) {
			data, err := json.Marshal(map[string]string{"host": fixturePassword})
			if err != nil {
				t.Fatal(err)
			}
			f.exec(t, "UPDATE config_documents SET document = ?", data)
		}},
		{name: "unresolved reference", want: "public configuration document contains secret material", prepare: func(t *testing.T, f *proofFixture) {
			f.exec(t, "UPDATE config_documents SET document = ?", []byte(`{"host":"${UNRESOLVED_FIXTURE}"}`))
		}},
		{name: "invalid evidence", want: "invalid private proof evidence", prepare: func(t *testing.T, f *proofFixture) {
			if err := os.WriteFile(f.evidence, []byte("[invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "evidence read failure", want: "cannot read private proof evidence", prepare: func(t *testing.T, f *proofFixture) {
			f.evidence = t.TempDir()
		}},
		{name: "evidence write failure", want: "cannot retain private proof evidence", prepare: func(t *testing.T, f *proofFixture) {
			f.evidence = filepath.Join(t.TempDir(), "missing", "proof.json")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProofFixture(t)
			if err := Run(t.Context(), f.database, "proxy/owned", "created", f.evidence, fixtureMaster, fixturePassword); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, &f)
			}
			phase, master, expected, target := tc.phase, tc.master, tc.expected, tc.target
			if target == "" {
				target = "proxy/owned"
			}
			if phase == "" {
				phase = "created"
			}
			if master == "" {
				master = fixtureMaster
			}
			if expected == "" {
				expected = fixturePassword
			}
			before, beforeErr := os.ReadFile(f.evidence)
			err := Run(t.Context(), f.database, target, phase, f.evidence, master, expected)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("proof error = %v; want %s", err, tc.want)
			}
			for _, private := range []string{fixturePassword, fixtureMaster, master} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("proof error exposed private fixture material")
				}
			}
			after, afterErr := os.ReadFile(f.evidence)
			if beforeErr == nil && (afterErr != nil || !bytes.Equal(before, after)) {
				t.Fatal("failed proof changed prior evidence")
			}
			if os.IsNotExist(beforeErr) && !os.IsNotExist(afterErr) {
				t.Fatal("failed proof published evidence")
			}
		})
	}
}

func TestAccConfigurationProofAppendsActualSealedRowEvidence(t *testing.T) {
	f := newProofFixture(t)
	for _, phase := range []string{"created", "refreshed", "retained"} {
		if err := Run(t.Context(), f.database, "proxy/owned", phase, f.evidence, fixtureMaster, fixturePassword); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(f.evidence)
	if err != nil {
		t.Fatal(err)
	}
	var records []proof
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := f.db.QueryRowContext(t.Context(), "SELECT value FROM config_secrets WHERE key = ?", "owned").Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(sealed)
	if len(records) != 3 {
		t.Fatalf("proof appended %d records, want 3", len(records))
	}
	for i, phase := range []string{"created", "refreshed", "retained"} {
		record := records[i]
		if record.Phase != phase || !record.DocumentPresent || record.SecretRows != 1 || !record.Sealed ||
			record.CiphertextSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("proof does not describe the actual sealed row in lifecycle order")
		}
	}
	if bytes.Contains(data, []byte(fixturePassword)) || bytes.Contains(data, []byte(fixtureMaster)) {
		t.Fatal("proof evidence exposed credentials")
	}
}
