package descriptor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/quota"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

type fixedCapacity struct{}

func (fixedCapacity) Measure(context.Context) (int64, int64, error) { return 100, 10, nil }

func quotaDeps(t *testing.T, db *inmem.Store, capacity configapi.StorageCapacity) Deps {
	t.Helper()
	d := depsOf(t, db)
	d.Projects = accesstest.Over(t, db).API().Projects
	d.Capacity = capacity
	return d
}

func applyQuotaDoc(t *testing.T, d Deps, doc string, dryRun bool) (configapi.Result, error) {
	t.Helper()
	p, err := Parse([]byte(doc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return Apply(context.Background(), d, p, now, dryRun)
}

func TestUnitQuotaParse(t *testing.T) {
	p, err := Parse([]byte("quotaConfig:\n  enabled: true\n  diskSpaceLimitPercentage: 95\n  diskSpaceWarningPercentage: 85\n"), noEnv)
	if err != nil || len(p.Configs) != 1 || p.Configs[0].Kind != quota.ConfigKind || p.Changes != 3 {
		t.Fatalf("parsed %+v, %v", p, err)
	}
	for doc, want := range map[string]string{
		"quotaConfig: null\n":                                   "quotaConfig cannot be null",
		"quotaConfig: on\n":                                     "quotaConfig must be a mapping",
		"quotaConfig:\n  enabled: null\n":                       "quotaConfig.enabled cannot be null",
		"quotaConfig:\n  enabled: 'true'\n":                     "quotaConfig.enabled must be a boolean",
		"quotaConfig:\n  diskSpaceLimitPercentage: 101\n":       "quotaConfig.diskSpaceLimitPercentage must be a whole percentage from 0 to 100",
		"quotaConfig:\n  diskSpaceLimitPercentage: -1\n":        "must be a whole percentage",
		"quotaConfig:\n  diskSpaceWarningPercentage: 85.5\n":    "quotaConfig.diskSpaceWarningPercentage must be a whole percentage",
		"quotaConfig:\n  diskSpaceWarningPercentage: '85'\n":    "must be a whole percentage",
		"quotaConfig:\n  diskSpaceWarningPercentage: null\n":    "quotaConfig.diskSpaceWarningPercentage cannot be null",
		"quotaConfig:\n  diskSpaceLimitBytes: 1\n":              "quotaConfig: unknown field diskSpaceLimitBytes",
		"localRepositories: {}\nquotaConfig:\n  enabled: 1\n":   "quotaConfig.enabled must be a boolean",
		"quotaConfig:\n  enabled: false\nunknownSection: {}\n":  "unknownSection",
		"quotaConfig:\n  diskSpaceLimitPercentage: [95]\n":      "must be a whole percentage",
		"quotaConfig:\n  diskSpaceWarningPercentage: true\n":    "must be a whole percentage",
		"quotaConfig:\n  diskSpaceLimitPercentage: 100000000\n": "must be a whole percentage",
	} {
		_, err := Parse([]byte(doc), noEnv)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", doc, err, want)
		}
	}
}

func TestUnitQuotaApplyMergesValidatesAndExports(t *testing.T) {
	db := inmem.NewWithCapacity(fixedCapacity{})
	d := quotaDeps(t, db, fixedCapacity{})
	if _, body := exportXML(t, d); strings.Contains(body, "quotaConfig") {
		t.Fatalf("an absent optional section was exported:\n%s", body)
	}
	res, err := applyQuotaDoc(t, d, "quotaConfig:\n  diskSpaceWarningPercentage: 80\n", true)
	if err != nil || !slices.Equal(res.Created, []string{"quotaConfig"}) {
		t.Fatal("dry run", res, err)
	}
	if _, err := db.Configs().Get(t.Context(), quota.ConfigKind, quota.ConfigKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("a dry run stored the policy", err)
	}
	if _, err := applyQuotaDoc(t, d, "quotaConfig:\n  diskSpaceWarningPercentage: 80\n", false); err != nil {
		t.Fatal(err)
	}
	// Omitted fields export their XSD defaults, in QuotaConfigType's order.
	node, body := exportXML(t, d)
	if xmlText(t, node, "quotaConfig/enabled") != "false" || xmlText(t, node, "quotaConfig/diskSpaceLimitPercentage") != "0" ||
		xmlText(t, node, "quotaConfig/diskSpaceWarningPercentage") != "80" {
		t.Fatalf("defaults:\n%s", body)
	}
	res, err = applyQuotaDoc(t, d, "quotaConfig:\n  enabled: true\n  diskSpaceLimitPercentage: 95\n", false)
	if err != nil || !slices.Equal(res.Updated, []string{"quotaConfig"}) {
		t.Fatal("merge", res, err)
	}
	p, stored, err := quota.Read(t.Context(), db.Configs())
	if err != nil || !stored || p != (quota.Policy{Enabled: true, LimitPercent: 95, Warning: 80}) {
		t.Fatal("omitted field was not kept", p, stored, err)
	}
	res, err = applyQuotaDoc(t, d, "quotaConfig:\n  enabled: true\n  diskSpaceLimitPercentage: 95\n", false)
	if err != nil || !slices.Equal(res.Unchanged, []string{"quotaConfig"}) {
		t.Fatal("reapply", res, err)
	}
	node, body = exportXML(t, d)
	order := []string{}
	for _, child := range node.Children {
		if child.Name.Local == "quotaConfig" {
			for _, field := range child.Children {
				order = append(order, field.Name.Local)
			}
		}
	}
	if !slices.Equal(order, quota.Fields) || strings.Index(body, "<repoLayouts>") > strings.Index(body, "<quotaConfig>") {
		t.Fatalf("XSD order %v:\n%s", order, body)
	}
}

func TestUnitQuotaRefusesWhatCannotTakeEffect(t *testing.T) {
	db := inmem.New()
	d := quotaDeps(t, db, nil)
	// Storing a disabled policy needs no measurable volume.
	if _, err := applyQuotaDoc(t, d, "quotaConfig:\n  enabled: false\n  diskSpaceLimitPercentage: 90\n", false); err != nil {
		t.Fatal(err)
	}
	_, err := applyQuotaDoc(t, d, "quotaConfig:\n  enabled: true\n", false)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "quotaConfig.enabled needs a hot blob store") {
		t.Fatal("enabling without a measurable volume", err)
	}
	p, _, err := quota.Read(t.Context(), db.Configs())
	if err != nil || p.Enabled {
		t.Fatal("a refused enable was stored", p, err)
	}
	// A refused section in a mixed document leaves the rest unapplied.
	_, err = applyQuotaDoc(t, d, "proxies:\n  p1:\n    host: proxy.example.invalid\n    port: 8080\nquotaConfig:\n  enabled: true\n", false)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := db.Configs().Get(t.Context(), configdoc.ConfigProxy, "p1"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("a mixed document applied part of itself", err)
	}
	// A stored document that no longer validates fails the export whole.
	if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: quota.ConfigKind, Key: quota.ConfigKey, Document: []byte(`{"enabled":"yes"}`),
	}); err != nil {
		t.Fatal(err)
	}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	if body, err := NewAPI(d).Export.XML(ctx); err == nil || body != nil {
		t.Fatal("a corrupt policy exported", string(body), err)
	}
	if _, err := applyQuotaDoc(t, d, "quotaConfig:\n  diskSpaceLimitPercentage: 90\n", false); !errors.Is(err, ErrInvalid) {
		t.Fatal("a corrupt stored policy merged", err)
	}
}

func TestUnitQuotaModuleCapacityComesFromTheStore(t *testing.T) {
	db := inmem.NewWithCapacity(fixedCapacity{})
	d := quotaDeps(t, db, db.StorageCapacity())
	if _, err := applyQuotaDoc(t, d, "quotaConfig:\n  enabled: true\n  diskSpaceLimitPercentage: 95\n", false); err != nil {
		t.Fatal(err)
	}
}
