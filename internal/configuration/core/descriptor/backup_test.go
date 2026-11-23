package descriptor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// providerBackup is the body the pinned artifactory_backup resource sends:
// every BackupAPIModel field, the entry key repeated as key.
const providerBackup = `backups:
  nightly:
    key: nightly
    cronExp: "0 0 2 ? * MON-FRI"
    enabled: true
    retentionPeriodHours: 336
    excludedRepositories: [backup-a, backup-b]
    createArchive: false
    excludeNewRepositories: true
    sendMailOnError: false
    precalculate: true
    exportMissionControl: false
`

func backupDocument(t *testing.T, db *inmem.Store, key string) (configdoc.ConfigDocument, map[string]any) {
	t.Helper()
	doc, err := db.Configs().Get(t.Context(), configdoc.ConfigBackup, key)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(doc.Document, &fields); err != nil {
		t.Fatal(err)
	}
	return doc, fields
}

func TestUnitBackupProviderLifecycleRoundTrips(t *testing.T) {
	db := inmem.New()
	for _, key := range []string{"backup-a", "backup-b", "backup-c"} {
		seedRepo(t, db, repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic})
	}
	if res := mustApply(t, db, providerBackup); !slices.Equal(res.Created, []string{"backups.nightly"}) {
		t.Fatal("backup not created", res)
	}
	doc, fields := backupDocument(t, db, "nightly")
	if !doc.Enabled || fields["key"] != nil || fields["cronExp"] != "0 0 2 ? * MON-FRI" {
		t.Fatal("stored backup lost its fields or kept the repeated key", fields)
	}
	if res := mustApply(t, db, providerBackup); !slices.Equal(res.Unchanged, []string{"backups.nightly"}) {
		t.Fatal("reapplying the provider body changed the backup", res)
	}
	node, _ := exportXML(t, depsOf(t, db))
	if got := xmlText(t, node, "backups/backup[key=nightly]/retentionPeriodHours"); got != "336" {
		t.Fatal("retention not exported", got)
	}
	var names, refs []string
	for _, section := range node.Children {
		if section.Name.Local != "backups" {
			continue
		}
		for _, field := range section.Children[0].Children {
			names = append(names, field.Name.Local)
			if field.Name.Local == "excludedRepositories" {
				for _, ref := range field.Children {
					refs = append(refs, ref.Text)
				}
			}
		}
	}
	want := []string{
		"key", "enabled", "cronExp", "retentionPeriodHours", "createArchive", "excludedRepositories",
		"sendMailOnError", "excludeNewRepositories", "precalculate", "exportMissionControl",
	}
	if !slices.Equal(names, want) || !slices.Equal(refs, []string{"backup-a", "backup-b"}) {
		t.Fatalf("backup elements = %v %v, want BackupType order %v", names, refs, want)
	}
	if res := mustApply(t, db, "backups:\n  nightly:\n    enabled: false\n    excludedRepositories: [backup-c]\n"); !slices.Equal(res.Updated, []string{"backups.nightly"}) {
		t.Fatal("partial update not reported", res)
	}
	doc, fields = backupDocument(t, db, "nightly")
	if doc.Enabled || fields["retentionPeriodHours"] != float64(336) || !slices.Equal(fields["excludedRepositories"].([]any), []any{"backup-c"}) {
		t.Fatal("partial update lost omitted fields or kept the replaced list", fields)
	}
	if res := mustApply(t, db, "backups:\n  nightly: ~\n"); !slices.Equal(res.Deleted, []string{"backups.nightly"}) {
		t.Fatal("named delete not reported", res)
	}
	if res := mustApply(t, db, "backups:\n  nightly: ~\n"); !slices.Equal(res.Unchanged, []string{"backups.nightly"}) {
		t.Fatal("deleting an absent backup was not a no-op", res)
	}
	if node, _ := exportXML(t, depsOf(t, db)); len(node.Children) > 0 {
		for _, section := range node.Children {
			if section.Name.Local == "backups" && len(section.Children) > 0 {
				t.Fatal("deleted backup still exported")
			}
		}
	}
}

func TestUnitBackupDefaultsAndPublishedSample(t *testing.T) {
	db := inmem.New()
	for _, key := range []string{"local1", "local2"} {
		seedRepo(t, db, repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic})
	}
	// JFrog's published 7.x sample, including the inert excludeBuilds and the
	// trailing space inside its cronExp.
	mustApply(t, db, `backups:
  backup-daily:
    cronExp: "0 0 12 * * ? "
    excludedRepositories:
      - local1
      - local2
    retentionPeriodHours: 1000
    createArchive: false
    enabled: true
    excludeBuilds: false
    excludeNewRepositories: false
    sendMailOnError: true
  minimal:
    cronExp: 0 0 1 * * ?
`)
	if _, fields := backupDocument(t, db, "backup-daily"); fields[excludeBuilds] != nil {
		t.Fatal("inert excludeBuilds was stored")
	}
	node, _ := exportXML(t, depsOf(t, db))
	for path, want := range map[string]string{
		"backups/backup[key=minimal]/enabled":              "true",
		"backups/backup[key=minimal]/retentionPeriodHours": "168",
		"backups/backup[key=minimal]/sendMailOnError":      "true",
		"backups/backup[key=minimal]/createArchive":        "false",
		"backups/backup[key=minimal]/exportMissionControl": "false",
		"backups/backup[key=backup-daily]/cronExp":         "0 0 12 * * ? ",
	} {
		if got := xmlText(t, node, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestUnitBackupRepositoryReferencesFollowThePatch(t *testing.T) {
	db := inmem.New()
	seedRepo(t, db, repo.Config{Key: "kept", Class: repo.Local, PackageType: repo.Generic})
	seedRepo(t, db, repo.Config{Key: "gone", Class: repo.Local, PackageType: repo.Generic})
	// A repository the same patch creates may be excluded.
	mustApply(t, db, `localRepositories:
  fresh: {type: generic}
backups:
  b:
    cronExp: 0 0 1 * * ?
    excludedRepositories: [kept, gone, fresh]
`)
	// Deleting one leaves the others; the export lists only existing ones,
	// as 7.x removes a deleted repository from every exclusion list.
	mustApply(t, db, "localRepositories:\n  gone: ~\n")
	node, _ := exportXML(t, depsOf(t, db))
	var refs []string
	for _, section := range node.Children {
		if section.Name.Local == "backups" {
			for _, field := range section.Children[0].Children {
				if field.Name.Local == "excludedRepositories" {
					for _, ref := range field.Children {
						refs = append(refs, ref.Text)
					}
				}
			}
		}
	}
	if !slices.Equal(refs, []string{"kept", "fresh"}) {
		t.Fatal("deleted repository still exported as excluded", refs)
	}
	// Touching the backup prunes the stale stored reference.
	mustApply(t, db, "backups:\n  b:\n    createArchive: true\n")
	if _, fields := backupDocument(t, db, "b"); !slices.Equal(fields["excludedRepositories"].([]any), []any{"kept", "fresh"}) {
		t.Fatal("stale reference kept after an update", fields)
	}
	for _, doc := range []string{
		"backups:\n  b:\n    excludedRepositories: [missing]\n",
		"localRepositories:\n  kept: ~\nbackups:\n  b:\n    excludedRepositories: [kept]\n",
	} {
		_, err := apply(t, db, doc)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "backups.b: ") || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("%q: %v", doc, err)
		}
	}
	if _, err := db.Repos().Get(context.Background(), "kept"); err != nil {
		t.Fatal("refused patch deleted its repository", err)
	}
}

func TestUnitBackupRefusalsWriteNothing(t *testing.T) {
	for name, doc := range map[string]string{
		"section null":      "backups: ~\n",
		"section scalar":    "backups: 3\n",
		"entry scalar":      "backups:\n  b: 3\n",
		"key mismatch":      "backups:\n  b: {key: c, cronExp: '0 0 1 * * ?'}\n",
		"illegal key":       "backups:\n  'a/b': {cronExp: '0 0 1 * * ?'}\n",
		"digit key":         "backups:\n  1b: {cronExp: '0 0 1 * * ?'}\n",
		"blank key":         "backups:\n  ' ': {cronExp: '0 0 1 * * ?'}\n",
		"space in key":      "backups:\n  'a b': {cronExp: '0 0 1 * * ?'}\n",
		"dir":               "backups:\n  b: {cronExp: '0 0 1 * * ?', dir: /tmp/backup}\n",
		"unknown field":     "backups:\n  b: {cronExp: '0 0 1 * * ?', colour: blue}\n",
		"null boolean":      "backups:\n  b: {cronExp: '0 0 1 * * ?', enabled: ~}\n",
		"string boolean":    "backups:\n  b: {cronExp: '0 0 1 * * ?', sendMailOnError: 'true'}\n",
		"excludeBuilds str": "backups:\n  b: {cronExp: '0 0 1 * * ?', excludeBuilds: 'no'}\n",
		"fraction hours":    "backups:\n  b: {cronExp: '0 0 1 * * ?', retentionPeriodHours: 1.5}\n",
		"huge hours":        "backups:\n  b: {cronExp: '0 0 1 * * ?', retentionPeriodHours: 3000000000}\n",
		"text hours":        "backups:\n  b: {cronExp: '0 0 1 * * ?', retentionPeriodHours: '7'}\n",
		"list scalar":       "backups:\n  b: {cronExp: '0 0 1 * * ?', excludedRepositories: kept}\n",
		"list number":       "backups:\n  b: {cronExp: '0 0 1 * * ?', excludedRepositories: [3]}\n",
		"cron number":       "backups:\n  b: {cronExp: 3}\n",
	} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	db := inmem.New()
	seedRepo(t, db, repo.Config{Key: "kept", Class: repo.Local, PackageType: repo.Generic})
	for name, doc := range map[string]string{
		"no cron":      "localRepositories:\n  extra: {type: generic}\nbackups:\n  b: {enabled: true}\n",
		"cleared cron": "localRepositories:\n  extra: {type: generic}\nbackups:\n  b: {cronExp: ~}\n",
		"five fields":  "localRepositories:\n  extra: {type: generic}\nbackups:\n  b: {cronExp: '0 1 * * *'}\n",
		"bad quartz":   "localRepositories:\n  extra: {type: generic}\nbackups:\n  b: {cronExp: '0 0 25 * * ?'}\n",
	} {
		_, err := apply(t, db, doc)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "backups.b") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := db.Repos().Get(context.Background(), "extra"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("a refused backup left the mixed patch's repository", err)
	}
	if docs, err := db.Configs().List(context.Background(), configdoc.ConfigBackup); err != nil || len(docs) != 0 {
		t.Fatal("a refused backup was stored", err)
	}
}

// TestUnitMixedDescriptorFinalWriteFaultRollsBackEverySection: a failing last
// write rolls back the repository, backup, mail (with its sealed password)
// and system message written before it.
func TestUnitMixedDescriptorFinalWriteFaultRollsBackEverySection(t *testing.T) {
	db := inmem.New()
	p, err := Parse([]byte(`localRepositories:
  mixed: {type: generic}
mailServer: {host: smtp.invalid, password: mixed-private}
backups:
  b: {cronExp: '0 0 1 * * ?', excludedRepositories: [mixed]}
systemMessageConfig: {enabled: true, message: maintenance}
security:
  anonAccessEnabled: true
`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), depsOf(t, faulty{Store: db, fail: "setting put"}), p, now, false); !errors.Is(err, errBoom) {
		t.Fatal("final write fault was not reported", err)
	}
	if _, err := db.Repos().Get(context.Background(), "mixed"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("repository survived", err)
	}
	for _, target := range []struct {
		kind configdoc.ConfigKind
		key  string
	}{{configdoc.ConfigBackup, "b"}, {configdoc.ConfigMailServer, system.MailServerKey}, {systemMessageKind, systemMessageKey}} {
		if _, err := db.Configs().Get(context.Background(), target.kind, target.key); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("configuration document survived the rollback", target.kind, err)
		}
	}
}
