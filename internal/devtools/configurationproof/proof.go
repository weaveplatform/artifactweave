package configurationproof

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	platformsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
)

var (
	errPhase           = errors.New("unknown proof phase")
	errTarget          = errors.New("unknown proof target; use proxy/<key> or mail_server/mailserver")
	errMasterKey       = errors.New("invalid fixture master key")
	errKeyring         = errors.New("cannot build fixture keyring")
	errDatabasePath    = errors.New("cannot resolve fixture database")
	errDatabaseOpen    = errors.New("cannot open fixture database read-only")
	errStoreRead       = errors.New("cannot read actual configuration with the server keyring")
	errSecretRead      = errors.New("cannot inspect configuration secret storage")
	errDeleted         = errors.New("deleted configuration retained document or secret rows")
	errMissing         = errors.New("expected configuration is missing")
	errClear           = errors.New("password clear retained an actual server secret")
	errPassword        = errors.New("actual server password differs from the expected fixture password")
	errUnsealed        = errors.New("actual server password is not sealed at rest")
	errPublicDocument  = errors.New("public configuration document contains secret material or an unresolved reference")
	errEvidenceRead    = errors.New("cannot read private proof evidence")
	errEvidenceInvalid = errors.New("invalid private proof evidence")
	errEvidenceEncode  = errors.New("cannot encode private proof evidence")
	errEvidenceWrite   = errors.New("cannot retain private proof evidence")
)

type proof struct {
	Phase            string `json:"phase"`
	DocumentPresent  bool   `json:"document_present"`
	SecretRows       int    `json:"secret_rows"`
	Sealed           bool   `json:"sealed"`
	CiphertextSHA256 string `json:"ciphertext_sha256,omitempty"`
}

// Run verifies one lifecycle phase of target, proxy/<key> or
// mail_server/mailserver, against the actual owned fixture store and
// appends credential-free proof evidence only after every assertion passes.
// It opens path read-only with the canonical keyring, never migrates or writes
// the database, and returns redacted errors without credentials or stored data.
// The caller owns path, evidence, the fixture secrets and phase ordering; Run
// must not run concurrently against the same evidence file.
func Run(ctx context.Context, path, target, phase, evidence, master, expected string) error {
	if !slices.Contains([]string{"created", "updated", "refreshed", "retained", "cleared", "deleted"}, phase) {
		return errPhase
	}
	// The two descriptor sections whose password the pinned providers write.
	kind, key, _ := strings.Cut(target, "/")
	if kind != string(configdoc.ConfigProxy) && kind != string(configdoc.ConfigMailServer) || key == "" {
		return errTarget
	}
	material, err := crypt.ParseVersionedKey(master)
	if err != nil {
		return errMasterKey
	}
	keys, err := crypt.NewKeyring(material)
	if err != nil {
		return errKeyring
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errDatabasePath
	}
	// mode=ro refuses a missing file and prevents the checker from migrating,
	// creating or changing the application's store while the server runs.
	u := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return errDatabaseOpen
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	store := platformsql.New(dialect.New(db, sqlite.Dialect, keys))
	doc, getErr := store.Configs().Get(ctx, configdoc.ConfigKind(kind), key)
	result := proof{Phase: phase, DocumentPresent: getErr == nil}
	if getErr != nil && !errors.Is(getErr, kernel.ErrNotFound) {
		return errStoreRead
	}
	var sealed []byte
	err = db.QueryRowContext(ctx,
		"SELECT value FROM config_secrets WHERE kind = ? AND key = ? AND name = ?",
		kind, key, "password").Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errSecretRead
	}
	if err == nil {
		result.SecretRows = 1
		result.Sealed = crypt.IsSealed(sealed)
		sum := sha256.Sum256(sealed)
		result.CiphertextSHA256 = hex.EncodeToString(sum[:])
	}
	switch {
	case phase == "deleted":
		if result.DocumentPresent || result.SecretRows != 0 {
			return errDeleted
		}
	case !result.DocumentPresent:
		return errMissing
	case phase == "cleared":
		if result.SecretRows != 0 || len(doc.Secrets) != 0 {
			return errClear
		}
	default:
		if expected == "" || doc.Secrets["password"] != expected {
			return errPassword
		}
		if result.SecretRows != 1 || !result.Sealed {
			return errUnsealed
		}
	}
	if bytes.Contains(doc.Document, []byte(`"password"`)) ||
		(expected != "" && bytes.Contains(doc.Document, []byte(expected))) || bytes.Contains(doc.Document, []byte("${")) {
		return errPublicDocument
	}
	return retainEvidence(evidence, result)
}

func retainEvidence(evidence string, result proof) error {
	records := make([]proof, 0, 1)
	data, err := os.ReadFile(evidence)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errEvidenceRead
	}
	if err == nil && json.Unmarshal(data, &records) != nil {
		return errEvidenceInvalid
	}
	records = append(records, result)
	data, err = json.MarshalIndent(records, "", "  ")
	if err != nil {
		return errEvidenceEncode
	}
	if err := os.WriteFile(evidence, append(data, '\n'), 0o600); err != nil {
		return errEvidenceWrite
	}
	return nil
}
