package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/descriptor"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// cdnURLBaseRefusal is system.CheckURLBase's refusal of a urlBase at or above
// a CDN host, which PATCH answers with 400.
const cdnURLBaseRefusal = "urlBase must not be a CDN domain's host or a parent of it: " +
	"clients would send it their credentials"

// urlBaseKind is the configuration document a stored urlBase is kept in
// (descriptor's urlBaseKind), which nothing refused may write.
const urlBaseKind configdoc.ConfigKind = "url_base"

// cdnConfig is a configuration whose hot blob tier names the CDN domain
// cdn.example.com. The CDN is never contacted: only the host it names
// reaches the configuration module's Settings.
func cdnConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.MasterKey = keyOne
	cfg.Bootstrap.AdminPassword = "cdn-url-base-fixture-password"
	cfg.Blob.S3.CDN.CloudFront.Domain = "cdn.example.com"
	return cfg
}

// wantNothingWritten checks that a refused descriptor left cfg's store as it
// was: no repository, no urlBase and no bootstrap marker.
func wantNothingWritten(t *testing.T, cfg config.Config) {
	t.Helper()
	keys, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenStore(t.Context(), cfg, appsql.Options{Keyring: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Repos().Get(t.Context(), "would-create"); !errors.Is(err, kernel.ErrNotFound) {
		t.Error("refused descriptor created its valid repository", err)
	}
	if _, err := db.Configs().Get(t.Context(), urlBaseKind, "default"); !errors.Is(err, kernel.ErrNotFound) {
		t.Error("refused descriptor stored its urlBase", err)
	}
	var marker bootstrapMarker
	if err := db.Settings().Get(t.Context(), settings.SettingBootstrapDescriptor, &marker); !errors.Is(err, kernel.ErrNotFound) {
		t.Error("refused descriptor wrote a bootstrap marker", err)
	}
}

// TestAccDescriptorEntryPointsRefuseCDNURLBase (#1902): PATCH, config apply,
// its dry run and the bootstrap descriptor give one answer for a urlBase at
// or above a configured CDN host, because each is bound with the
// installation's Settings (decision 0009). Before, config apply and the
// bootstrap descriptor were bound without the CDN hosts, and stored such a
// urlBase that PATCH refused. A refused document writes nothing, so its
// valid repository is not created either.
func TestAccDescriptorEntryPointsRefuseCDNURLBase(t *testing.T) {
	for _, bad := range []string{"https://cdn.example.com", "https://example.com/root"} {
		doc := "localRepositories:\n  would-create: {type: generic}\nurlBase: " + bad + "\n"
		t.Run(bad+"/patch", func(t *testing.T) {
			cfg := cdnConfig(t)
			site := openWebServerSite(t, cfg)
			code, body := site.call(http.MethodPatch, "/artifactory/api/system/configuration", "admin", doc,
				"Content-Type", "application/yaml")
			if code != http.StatusBadRequest || !strings.Contains(body, cdnURLBaseRefusal) {
				t.Fatalf("PATCH: %d %s", code, body)
			}
			site.close()
			wantNothingWritten(t, cfg)
		})
		for _, entry := range []string{"dry-run", "apply", "bootstrap"} {
			t.Run(bad+"/"+entry, func(t *testing.T) {
				cfg := cdnConfig(t)
				file := writeFile(t, doc)
				var err error
				if entry == "bootstrap" {
					cfg.Bootstrap.Descriptor = file
					var rt *Runtime
					rt, err = OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), RuntimeOptions{})
					if rt != nil {
						_ = rt.Close(context.Background())
					}
				} else {
					err = ApplyDescriptor(t.Context(), cfg, file, entry == "dry-run", &bytes.Buffer{})
				}
				if !errors.Is(err, descriptor.ErrInvalid) || !strings.Contains(err.Error(), cdnURLBaseRefusal) {
					t.Fatalf("%s: %v", entry, err)
				}
				wantNothingWritten(t, cfg)
			})
		}
	}
	// A host beside the CDN's is still accepted, by the bootstrap
	// descriptor and by config apply.
	cfg := cdnConfig(t)
	cfg.Bootstrap.Descriptor = writeFile(t, "urlBase: https://reg.example.com\n")
	rt, err := OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ApplyDescriptor(t.Context(), cfg, writeFile(t, "urlBase: https://repo.example.com\n"), false, &out); err != nil ||
		!strings.Contains(out.String(), "Updated: urlBase") {
		t.Fatalf("config apply of an unrelated host: %v\n%s", err, out.String())
	}
}
