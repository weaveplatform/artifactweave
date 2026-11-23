package configurationhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/descriptor"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const typedConfigSeed = `localRepositories:
  gone: {type: generic}
  kept: {type: terraform, description: before}
proxies:
  corp:
    host: before.invalid
    port: 3128
    platformDefault: true
    password: ${DESCRIPTOR_PRIVATE_FIXTURE}
propertySets:
  build:
    visible: true
    properties:
      release:
        closedPredefinedValues: true
        multipleChoice: true
        predefinedValues: {ready: {defaultValue: true}, waiting: {defaultValue: false}}
repoLayouts:
  custom:
    artifactPathPattern: "[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"
mailServer:
  host: before.invalid
  password: ${DESCRIPTOR_PRIVATE_FIXTURE}
downloadRedirectConfig:
  fileMinimumSize: 1
security:
  anonAccessEnabled: false
`

const typedConfigMixed = `localRepositories:
  gone: null
  created: {type: generic}
  kept: {terraformType: module, description: after}
proxies:
  corp:
    host: after.invalid
    password: ${DESCRIPTOR_PRIVATE_FIXTURE}
%PROXY%
propertySets:
  build:
    visible: false
%PROPERTY%
repoLayouts:
  custom:
    artifactPathPattern: "changed/[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"
mailServer:
  host: after.invalid
  password: ${DESCRIPTOR_PRIVATE_FIXTURE}
downloadRedirectConfig:
  fileMinimumSize: 2
security:
  anonAccessEnabled: true
`

func TestAccPatchConfigurationRejectsTypedFieldsBeforeWrites(t *testing.T) {
	t.Setenv("DESCRIPTOR_PRIVATE_FIXTURE", "initial-private-fixture")
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := typedConfigSQLStore(t, engine)
			access := accesstest.Over(t, db).API()
			writes := &typedConfigWrites{ConfigStore: db.Configs()}
			api := descriptor.NewAPI(descriptor.Deps{
				Tx: db, Repositories: support.Repositories(db), Layouts: support.Layouts(),
				Configs: writes, AnonymousAccess: access.AnonymousAccess, Projects: access.Projects,
			})
			var logs bytes.Buffer
			svc := New(Deps{Store: db, Configuration: api, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			patch := func(t *testing.T, body string, status int) {
				t.Helper()
				ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
				req := httptest.NewRequestWithContext(ctx, http.MethodPatch, configPath, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/yaml")
				rec := httptest.NewRecorder()
				svc.PatchConfiguration(rec, req)
				if rec.Code != status {
					t.Fatalf("PATCH status %d, expected %d: %s", rec.Code, status, rec.Body)
				}
				for _, private := range []string{"initial-private-fixture", "replacement-private-fixture", "${DESCRIPTOR_PRIVATE_FIXTURE}"} {
					if strings.Contains(rec.Body.String()+logs.String(), private) {
						t.Fatal("descriptor response or log exposed a credential or unresolved reference")
					}
				}
			}
			patch(t, typedConfigSeed, http.StatusOK)
			before := typedConfigState(t, db)
			patch(t, typedConfigSeed, http.StatusOK)
			if typedConfigState(t, db) != before {
				t.Fatal("repeated provider-shaped map descriptor rewrote stored state")
			}
			t.Setenv("DESCRIPTOR_PRIVATE_FIXTURE", "replacement-private-fixture")
			for _, tc := range []struct{ name, proxy, property string }{
				{"proxy", "  broken: {host: proxy.invalid, port: 3128, platformDefault: {unexpected: true}}", ""},
				{"multiple choice", "", "  broken: {properties: {release: {multipleChoice: {unexpected: true}}}}"},
				{"default value", "", "  broken: {properties: {release: {predefinedValues: {ready: {defaultValue: {unexpected: true}}}}}}"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					standalone := "propertySets:\n" + tc.property + "\n"
					if tc.proxy != "" {
						standalone = "proxies:\n" + tc.proxy + "\n"
					}
					body := strings.NewReplacer("%PROXY%", tc.proxy, "%PROPERTY%", tc.property).Replace(typedConfigMixed)
					writes.calls = 0
					patch(t, body, http.StatusBadRequest)
					if writes.calls != 0 || typedConfigState(t, db) != before {
						t.Fatal("invalid mixed HTTP patch reached storage or changed persisted state")
					}
					for _, dryRun := range []bool{true, false} {
						plan, err := api.Descriptor.Parse([]byte(body), func(string) (string, bool) { return "replacement-private-fixture", true })
						if err != nil {
							t.Fatal(err)
						}
						writes.calls = 0
						if _, err := api.Descriptor.Apply(t.Context(), plan, time.Now(), dryRun); !errors.Is(err, configapi.ErrInvalid) {
							t.Fatal("shared apply boundary did not classify invalid input", err)
						}
						if writes.calls != 0 || typedConfigState(t, db) != before {
							t.Fatal("invalid apply reached secret/document storage or changed state")
						}
					}
					patch(t, standalone, http.StatusBadRequest)
					if writes.calls != 0 || typedConfigState(t, db) != before {
						t.Fatal("invalid HTTP patch reached storage or changed persisted state")
					}
				})
			}
			const update = `proxies:
  corp: {platformDefault: false}
propertySets:
  build:
    visible: false
    properties:
      release:
        closedPredefinedValues: false
        multipleChoice: false
        predefinedValues: [ready, {value: waiting, defaultValue: false}]
`
			patch(t, update, http.StatusOK)
			proxy, err := db.Configs().Get(t.Context(), configdoc.ConfigProxy, "corp")
			if err != nil || proxy.Secrets["password"] != "initial-private-fixture" || !bytes.Contains(proxy.Document, []byte(`"platformDefault":false`)) {
				t.Fatal("false update lost the omitted proxy password or flag", err)
			}
			before = typedConfigState(t, db)
			patch(t, update, http.StatusOK)
			if typedConfigState(t, db) != before {
				t.Fatal("repeated list descriptor rewrote stored state")
			}
			patch(t, "proxies:\n  corp: {password: null, platformDefault: null}\n", http.StatusOK)
			proxy, err = db.Configs().Get(t.Context(), configdoc.ConfigProxy, "corp")
			if err != nil || len(proxy.Secrets) != 0 || bytes.Contains(proxy.Document, []byte("platformDefault")) {
				t.Fatal("explicit null did not clear password and optional flag", err)
			}
			before = typedConfigState(t, db)
			patch(t, "proxies:\n  corp: {password: ''}\n", http.StatusOK)
			if typedConfigState(t, db) != before {
				t.Fatal("empty password clear was not idempotent")
			}
			// The companion edits must be valid on their own: otherwise an
			// unrelated refusal could hide missing typed-field validation.
			validMixed := strings.NewReplacer("%PROXY%", "", "%PROPERTY%", "").Replace(typedConfigMixed)
			patch(t, validMixed, http.StatusOK)
			before = typedConfigState(t, db)
			patch(t, validMixed, http.StatusOK)
			if typedConfigState(t, db) != before {
				t.Fatal("otherwise valid mixed descriptor was not idempotent")
			}
		})
	}
}

type typedConfigWrites struct {
	configdoc.ConfigStore
	calls int
}

func (w *typedConfigWrites) Put(ctx context.Context, doc configdoc.ConfigDocument) error {
	w.calls++
	return w.ConfigStore.Put(ctx, doc) //nolint:wrapcheck // observe the real store boundary
}

func (w *typedConfigWrites) Delete(ctx context.Context, kind configdoc.ConfigKind, key string) error {
	w.calls++
	return w.ConfigStore.Delete(ctx, kind, key) //nolint:wrapcheck // observe the real store boundary
}

func typedConfigSQLStore(t *testing.T, engine string) *appstoretest.SQLStore {
	t.Helper()
	keys, err := crypt.NewKeyring(crypt.MasterKey{Version: 1, Material: bytes.Repeat([]byte{0x42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	options := appstoretest.Options{Keyring: keys}
	var db *appstoretest.SQLStore
	if engine == "sqlite" {
		db, err = appstoretest.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "descriptor.db"), appstoretest.SQLiteOptions{Options: options})
	} else {
		dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		db, err = appstoretest.OpenPostgres(t.Context(), dsn, appstoretest.PostgresOptions{Options: options})
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// Read only the state this fixture's mixed descriptor could change. Keeping
// ciphertext bytes proves rejection happens before resealing, as well as before
// publication of the document, repository, queue, event and security changes.
func typedConfigState(t *testing.T, db *appstoretest.SQLStore) string {
	t.Helper()
	var state [][]any
	for _, query := range []string{
		"SELECT * FROM config_documents ORDER BY kind, key",
		"SELECT * FROM config_secrets ORDER BY kind, key, name",
		"SELECT * FROM repositories ORDER BY key",
		"SELECT * FROM repository_secrets ORDER BY repo_key",
		"SELECT * FROM settings ORDER BY key",
		"SELECT * FROM work_items ORDER BY kind, key",
		"SELECT * FROM outbox ORDER BY origin, stream, seq",
		"SELECT * FROM outbox_sequences ORDER BY origin, stream",
		"SELECT * FROM trash ORDER BY id",
	} {
		rows, err := db.DB().QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if strings.Contains(query, "FROM config_secrets") {
				value, ok := values[len(values)-1].([]byte)
				if !ok || !crypt.IsSealed(value) {
					_ = rows.Close()
					t.Fatal("fixture credential was not sealed")
				}
			}
			state = append(state, values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-fixture")) || bytes.Contains(encoded, []byte("${DESCRIPTOR_PRIVATE_FIXTURE}")) {
		t.Fatal("credential or unresolved reference reached public persisted state")
	}
	return string(encoded)
}
