package descriptor

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// gcScheduleRef is the collector's schedule: the text of gc.ScheduleKind and
// its key, which a configuration test may not import from artifacts core.
var gcScheduleRef = schedule.Reference{Kind: "storage:gc", Key: "gcConfig"}

func storageDeps(t *testing.T, db *inmem.Store) Deps {
	t.Helper()
	d := depsOf(t, db)
	d.StoragePolicies = support.StoragePolicies(db)
	d.Projects = accesstest.Over(t, db).API().Projects
	return d
}

func applyStorageDoc(t *testing.T, d Deps, doc string, dryRun bool) (configapi.Result, error) {
	t.Helper()
	p, err := Parse([]byte(doc), env(map[string]string{"GC_CRON": "0 0 /6 * * ?"}))
	if err != nil {
		t.Fatal(err)
	}
	return Apply(context.Background(), d, p, now, dryRun)
}

func TestUnitStorageParse(t *testing.T) {
	p, err := Parse([]byte(`gcConfig:
  cronExp: ${GC_CRON}
trashcanConfig:
  enabled: false
  retentionPeriodDays: 0
`), env(map[string]string{"GC_CRON": "0 0 /6 * * ?"}))
	if err != nil {
		t.Fatal(err)
	}
	s := p.Storage
	if s == nil || !s.GC || !s.Trashcan || *s.GCCron != "0 0 /6 * * ?" || *s.TrashEnabled ||
		*s.TrashRetentionDays != 0 || p.Changes != 3 {
		t.Fatalf("parsed %+v", p)
	}
	// One section leaves the other out of the change and the result.
	p, err = Parse([]byte("trashcanConfig:\n  retentionPeriodDays: 3\n"), noEnv)
	if err != nil || p.Storage.GC || p.Storage.GCCron != nil || p.Storage.TrashEnabled != nil {
		t.Fatalf("one section %+v, %v", p.Storage, err)
	}
	// An empty section names the section and changes nothing.
	p, err = Parse([]byte("gcConfig: {}\n"), noEnv)
	if err != nil || !p.Storage.GC || p.Storage.GCCron != nil {
		t.Fatalf("empty section %+v, %v", p.Storage, err)
	}
	for doc, want := range map[string]string{
		"gcConfig: null\n":                                 "gcConfig cannot be null",
		"gcConfig: daily\n":                                "gcConfig must be a mapping",
		"gcConfig:\n  cronExp: null\n":                     "gcConfig.cronExp cannot be null",
		"gcConfig:\n  cronExp: 4\n":                        "gcConfig.cronExp must be a Quartz cron expression string",
		"gcConfig:\n  interval: 4h\n":                      "gcConfig: unknown field interval",
		"trashcanConfig: null\n":                           "trashcanConfig cannot be null",
		"trashcanConfig:\n  enabled: null\n":               "trashcanConfig.enabled cannot be null",
		"trashcanConfig:\n  enabled: 'true'\n":             "trashcanConfig.enabled must be a boolean",
		"trashcanConfig:\n  retentionPeriodDays: null\n":   "trashcanConfig.retentionPeriodDays cannot be null",
		"trashcanConfig:\n  retentionPeriodDays: '14'\n":   "trashcanConfig.retentionPeriodDays must be a whole number of days",
		"trashcanConfig:\n  retentionPeriodDays: 1.5\n":    "trashcanConfig.retentionPeriodDays must be a whole number of days",
		"trashcanConfig:\n  allowPermDeletes: true\n":      "trashcanConfig: unknown field allowPermDeletes",
		"localRepositories: {}\ngcConfig:\n  cronExp: 1\n": "gcConfig.cronExp must be",
	} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", doc, err, want)
		}
	}
}

func TestUnitStorageApplyPlansThenWrites(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	d := storageDeps(t, db)
	const doc = "gcConfig:\n  cronExp: ${GC_CRON}\ntrashcanConfig:\n  enabled: false\n  retentionPeriodDays: 3\n"
	// A dry run names the sections it would update and writes nothing.
	res, err := applyStorageDoc(t, d, doc, true)
	if err != nil || strings.Join(res.Updated, " ") != "gcConfig trashcanConfig" || !res.DryRun {
		t.Fatalf("dry run %+v, %v", res, err)
	}
	if got, _ := d.StoragePolicies.Current(ctx); got.GCCron != "0 0 /4 * * ?" || !got.TrashEnabled || got.TrashRetentionDays != 14 {
		t.Fatalf("dry run wrote %+v", got)
	}
	res, err = applyStorageDoc(t, d, doc, false)
	if err != nil || strings.Join(res.Updated, " ") != "gcConfig trashcanConfig" {
		t.Fatalf("apply %+v, %v", res, err)
	}
	want := artifactsapi.StoragePolicy{GCCron: "0 0 /6 * * ?", TrashEnabled: false, TrashRetentionDays: 3}
	if got, err := d.StoragePolicies.Current(ctx); err != nil || got != want {
		t.Fatalf("current %+v, %v", got, err)
	}
	position, err := db.Schedules().Get(ctx, gcScheduleRef)
	if err != nil || position.Cron != want.GCCron {
		t.Fatalf("schedule %+v, %v", position, err)
	}
	// The same document again changes nothing, the schedule included.
	res, err = applyStorageDoc(t, d, doc, false)
	if err != nil || len(res.Updated) != 0 || strings.Join(res.Unchanged, " ") != "gcConfig trashcanConfig" {
		t.Fatalf("reapply %+v, %v", res, err)
	}
	if again, _ := db.Schedules().Get(ctx, gcScheduleRef); again != position {
		t.Fatalf("reapply rescheduled %+v", again)
	}
	// The owner's refusals name the descriptor's fields and write nothing.
	for doc, msg := range map[string]string{
		"gcConfig:\n  cronExp: '0 */4 * * *'\n":           "gcConfig.cronExp is not a valid Quartz cron expression",
		"gcConfig:\n  cronExp: '0 0 0 1 1 ? 2020'\n":      "gcConfig.cronExp never fires again",
		"trashcanConfig:\n  retentionPeriodDays: -1\n":    "trashcanConfig.retentionPeriodDays must not be negative",
		"trashcanConfig:\n  retentionPeriodDays: 99999\n": "trashcanConfig.retentionPeriodDays goes back too far",
	} {
		_, err := applyStorageDoc(t, d, "localRepositories:\n  refused-local:\n    type: generic\n"+doc, false)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", doc, err, msg)
		}
	}
	if _, err := db.Repos().Get(ctx, "refused-local"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("a refused descriptor created a repository: %v", err)
	}
	if got, _ := d.StoragePolicies.Current(ctx); got != want {
		t.Fatalf("a refused descriptor changed the policy: %+v", got)
	}
	// Without the binding the sections cannot be applied.
	d.StoragePolicies = nil
	if _, err := applyStorageDoc(t, d, doc, false); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("no binding: %v", err)
	}
}

var errAnonymousWrite = errors.New("anonymous access write failed")

func TestUnitStorageRollsBackWithTheCompoundDescriptor(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	d := storageDeps(t, db)
	anonymous := d.AnonymousAccess
	d.AnonymousAccess = failingSwitch{anonymous}
	_, err := applyStorageDoc(t, d, `localRepositories:
  rolled-back:
    type: generic
gcConfig:
  cronExp: '0 0 1 * * ?'
trashcanConfig:
  enabled: false
security:
  anonAccessEnabled: true
`, false)
	if !errors.Is(err, errAnonymousWrite) {
		t.Fatalf("compound apply: %v", err)
	}
	if _, err := db.Repos().Get(ctx, "rolled-back"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("repository kept: %v", err)
	}
	if got, _ := d.StoragePolicies.Current(ctx); got.GCCron != "0 0 /4 * * ?" || !got.TrashEnabled {
		t.Fatalf("policy kept %+v", got)
	}
	if _, err := db.Schedules().Get(ctx, gcScheduleRef); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("schedule kept: %v", err)
	}
}

// failingSwitch fails the anonymous access write, which the patch's
// transaction makes after the storage policy, so the policy must roll back.
type failingSwitch struct{ accessapi.AnonymousAccess }

func (failingSwitch) SetEnabled(context.Context, bool) error { return errAnonymousWrite }

func TestUnitStorageExport(t *testing.T) {
	db := inmem.New()
	d := storageDeps(t, db)
	node, body := exportXML(t, d)
	if xmlText(t, node, "gcConfig/cronExp") != "0 0 /4 * * ?" ||
		xmlText(t, node, "trashcanConfig/enabled") != "true" ||
		xmlText(t, node, "trashcanConfig/retentionPeriodDays") != "14" {
		t.Fatalf("defaults:\n%s", body)
	}
	if _, err := applyStorageDoc(t, d, "gcConfig:\n  cronExp: '0 0 3 * * ?'\ntrashcanConfig:\n  enabled: false\n  retentionPeriodDays: 0\n", false); err != nil {
		t.Fatal(err)
	}
	node, body = exportXML(t, d)
	if xmlText(t, node, "gcConfig/cronExp") != "0 0 3 * * ?" ||
		xmlText(t, node, "trashcanConfig/enabled") != "false" ||
		xmlText(t, node, "trashcanConfig/retentionPeriodDays") != "0" {
		t.Fatalf("applied:\n%s", body)
	}
	// XSD order: repoLayouts, gcConfig, ..., trashcanConfig, ...,
	// downloadRedirectConfig.
	var order []string
	for _, child := range node.Children {
		order = append(order, child.Name.Local)
	}
	joined := strings.Join(order, " ")
	if !strings.Contains(joined, "repoLayouts gcConfig trashcanConfig") {
		t.Fatalf("element order %s", joined)
	}
	// The artifactory_trashcan_config resource (provider 12.11.14) decodes
	// its section with these tags and drops itself from state when it is
	// missing.
	var provider struct {
		Trashcan *struct {
			Enabled             bool `xml:"enabled"`
			RetentionPeriodDays int  `xml:"retentionPeriodDays"`
		} `xml:"trashcanConfig"`
	}
	if err := xml.Unmarshal([]byte(body), &provider); err != nil || provider.Trashcan == nil ||
		provider.Trashcan.Enabled || provider.Trashcan.RetentionPeriodDays != 0 {
		t.Fatalf("provider read %+v, %v", provider.Trashcan, err)
	}
	// Without the binding the export leaves both sections out.
	d.StoragePolicies = nil
	if _, body = exportXML(t, d); strings.Contains(body, "gcConfig") || strings.Contains(body, "trashcanConfig") {
		t.Fatalf("unbound export:\n%s", body)
	}
}

type failingPolicies struct{ artifactsapi.StoragePolicies }

func (failingPolicies) Current(context.Context) (artifactsapi.StoragePolicy, error) {
	return artifactsapi.StoragePolicy{}, kernel.ErrStore
}

func TestUnitStorageExportFailure(t *testing.T) {
	db := inmem.New()
	d := storageDeps(t, db)
	d.StoragePolicies = failingPolicies{d.StoragePolicies}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	body, err := NewAPI(d).Export.XML(ctx)
	if !errors.Is(err, kernel.ErrStore) || body != nil {
		t.Fatalf("export %q, %v", body, err)
	}
}

// failingApply plans as the owner does and fails the write, as a store
// that fails mid-transaction would.
type failingApply struct{ artifactsapi.StoragePolicies }

func (failingApply) Apply(context.Context, artifactsapi.StoragePolicyChange, time.Time) (artifactsapi.StoragePolicyResult, error) {
	return artifactsapi.StoragePolicyResult{}, kernel.ErrStore
}

func TestUnitStorageApplyFailure(t *testing.T) {
	db := inmem.New()
	d := storageDeps(t, db)
	d.StoragePolicies = failingApply{d.StoragePolicies}
	_, err := applyStorageDoc(t, d, "localRepositories:\n  write-failed:\n    type: generic\ntrashcanConfig:\n  enabled: false\n", false)
	if !errors.Is(err, kernel.ErrStore) || errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "gcConfig/trashcanConfig") {
		t.Fatalf("apply: %v", err)
	}
	if _, err := db.Repos().Get(context.Background(), "write-failed"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("repository kept: %v", err)
	}
}
