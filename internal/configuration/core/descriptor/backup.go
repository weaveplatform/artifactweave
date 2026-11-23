package descriptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

// SectionBackups is the named backup definitions. A definition is
// administered configuration only: P36 owns running, retaining and restoring
// backups, so storing one is not evidence that any backup ran.
const SectionBackups = "backups"

// Backup fields in 7.161.26's BackupType order (XSD propOrder). dir is left
// out: BackupDescriptor marks it @DiffIgnore, so the YAML PATCH cannot set
// it, and it names a host directory that only P36 execution would use.
var backupFields = []string{
	"enabled", "cronExp", "retentionPeriodHours", "createArchive", "excludedRepositories",
	"sendMailOnError", "excludeNewRepositories", "precalculate", "exportMissionControl",
}

// backupBooleans holds each boolean's XSD default, which the export states
// explicitly: the pinned provider decodes the XML without the XSD, so an
// omitted sendMailOnError would read back as false.
var backupBooleans = map[string]bool{
	"enabled": true, "createArchive": false, "sendMailOnError": true,
	"excludeNewRepositories": false, "precalculate": false, "exportMissionControl": false,
}

// defaultRetentionHours is BackupDescriptor.DEFAULT_RETENTION_PERIOD_HOURS.
const defaultRetentionHours = 168

// excludeBuilds appears in JFrog's 7.x YAML sample, documented as relevant
// only to Artifactory 6.6 and below; 7.161.26's BackupDescriptor has no such
// field. It is checked and dropped, as the unstored repository block fields
// are, so the published sample applies without storing an inert value.
const excludeBuilds = "excludeBuilds"

// backupNameForbidden are NameValidator's forbidden characters (7.161.26).
const backupNameForbidden = `/\:|?*"<>`

// parseBackups adds each backups entry to p. An entry set to null deletes
// the definition, as the artifactory_backup resource's destroy sends; a
// whole-section null is refused like every other named section's.
func parseBackups(top map[string]any, p *Patch) error {
	entries, err := section(top, SectionBackups)
	if err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		where := SectionBackups + "." + key
		if err := checkBackupKey(key); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalid, where, err)
		}
		c := ConfigChange{Section: SectionBackups, Kind: configdoc.ConfigBackup, Key: key}
		switch entry := entries[key].(type) {
		case nil:
			c.Delete = true
		case map[string]any:
			// The pinned provider repeats the entry key as key in every body.
			if v := entry["key"]; v != nil && v != key {
				return fmt.Errorf("%w: %s: key %v does not match the entry key", ErrInvalid, where, v)
			}
			delete(entry, "key")
			if v, present := entry[excludeBuilds]; present {
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("%w: %s.%s must be a boolean", ErrInvalid, where, excludeBuilds)
				}
				delete(entry, excludeBuilds)
			}
			if bad := unknown(entry, backupFields); bad != "" {
				return fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, bad)
			}
			for _, name := range slices.Sorted(maps.Keys(entry)) {
				// Only a string clears; cronExp is the one string, and the
				// merged check then asks for it again.
				if entry[name] == nil && name != "cronExp" {
					return nullRefused(where + "." + name)
				}
			}
			if err := backupFieldTypes(entry); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			c.Fields = entry
		default:
			return fmt.Errorf("%w: %s must be a mapping or null", ErrInvalid, where)
		}
		p.Configs = append(p.Configs, c)
	}
	return nil
}

// checkBackupKey applies DescriptorUtils.assertNoSpecialCharactersAndValidXmlName:
// the key is the backup's xs:ID, so it must be an XML name, and it may not
// hold NameValidator's forbidden characters.
func checkBackupKey(key string) error {
	if strings.TrimSpace(key) == "" || key == "." || key == ".." || key == "&" ||
		strings.ContainsAny(key, backupNameForbidden) {
		return errBackupKeyIllegal
	}
	for i, r := range key {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_'
		digit := r >= '0' && r <= '9'
		if i == 0 && !letter || !letter && !digit && r != '-' && r != '.' {
			return errBackupKeyName
		}
	}
	return nil
}

var (
	errBackupKeyIllegal = errors.New("illegal backup key")
	errBackupKeyName    = errors.New("the backup key must be an XML name")
)

// backupFieldTypes checks the supplied or merged fields' types.
func backupFieldTypes(doc map[string]any) error {
	if err := configFieldTypes(doc, []string{"cronExp"}, slices.Sorted(maps.Keys(backupBooleans))); err != nil {
		return err
	}
	if v, present := doc["retentionPeriodHours"]; present {
		// xs:int; a value of 0 or less turns retention cleanup off in 7.x.
		n, ok := number(v)
		if !ok || n != math.Trunc(n) || n < math.MinInt32 || n > math.MaxInt32 {
			return fmt.Errorf("%w: retentionPeriodHours must be a whole number", ErrInvalid)
		}
	}
	if v, present := doc["excludedRepositories"]; present {
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%w: excludedRepositories must be a list of repository keys", ErrInvalid)
		}
		for _, item := range list {
			if key, ok := item.(string); !ok || key == "" {
				return fmt.Errorf("%w: excludedRepositories must be a list of repository keys", ErrInvalid)
			}
		}
	}
	return nil
}

// validateBackup checks a merged definition: a Quartz cronExp, as 7.x
// schedules it, and field types. Repository references are checked by
// checkBackupRepositories against the state the whole patch leaves.
func validateBackup(doc map[string]any) error {
	cron, _ := doc["cronExp"].(string)
	if cron == "" {
		return fmt.Errorf("%w: a backup needs a cronExp", ErrInvalid)
	}
	if _, err := schedule.Parse(cron, ""); err != nil {
		return fmt.Errorf("%w: cronExp is not a valid Quartz cron expression", ErrInvalid)
	}
	return backupFieldTypes(doc)
}

// checkBackupRepositories refuses an excluded repository that the patch
// names and that will not exist once it is applied. A stored reference the
// patch does not repeat is dropped instead when its repository is gone:
// 7.161.26's CentralConfigDescriptorImpl removes a deleted repository from
// every backup's exclusions, and the export never lists a missing one.
func checkBackupRepositories(c ConfigChange, doc map[string]any, repositories map[string]bool) error {
	list, _ := doc["excludedRepositories"].([]any)
	if list == nil {
		return nil
	}
	_, supplied := c.Fields["excludedRepositories"]
	kept := make([]any, 0, len(list))
	for _, item := range list {
		key, _ := item.(string)
		if repositories[key] {
			kept = append(kept, item)
			continue
		}
		if supplied {
			return fmt.Errorf("%w: excludedRepositories: repository %s does not exist", ErrInvalid, key)
		}
	}
	doc["excludedRepositories"] = kept
	return nil
}

// exportBackup projects one stored definition in BackupType order, with
// every default stated and only existing excluded repositories.
func exportBackup(key string, fields map[string]any, repositories map[string]bool) exportFields {
	entry := exportFields{Fields: []exportField{{Name: "key", Text: key}}}
	boolean := func(name string) exportField {
		value, ok := fields[name].(bool)
		if !ok {
			value = backupBooleans[name]
		}
		return exportValue(name, value)
	}
	entry.Fields = append(entry.Fields, boolean("enabled"), exportValue("cronExp", fields["cronExp"]))
	retention := any(defaultRetentionHours)
	if v, present := fields["retentionPeriodHours"]; present {
		retention = v
	}
	entry.Fields = append(entry.Fields, exportValue("retentionPeriodHours", retention), boolean("createArchive"))
	excluded := exportField{Name: "excludedRepositories"}
	list, _ := fields["excludedRepositories"].([]any)
	for _, item := range list {
		if key, ok := item.(string); ok && repositories[key] {
			excluded.Children = append(excluded.Children, exportField{Name: "repositoryRef", Text: key})
		}
	}
	entry.Fields = append(entry.Fields, excluded)
	for _, name := range []string{"sendMailOnError", "excludeNewRepositories", "precalculate", "exportMissionControl"} {
		entry.Fields = append(entry.Fields, boolean(name))
	}
	return entry
}

// BackupExclusions is the artifacts module's RepositoryLifecycle for backup
// definitions: an excludedRepositories entry is a repositoryRef naming the
// repository (xs:string in 7.161.26's BackupType; 6.20's typed it an IDREF),
// so it goes with the repository. Deleting
// a repository outside the descriptor (the REST API) would otherwise leave
// its key stored, and a repository created again under that key would come
// back excluded without anyone asking.
//
// Artifacts calls ResetRepository in the repository change's transaction,
// which Configs joins through ctx, so a failure rolls the change back too.
// It runs on creation as well as deletion; a descriptor patch creates its
// repositories before it writes its backups, so an exclusion the same patch
// names is kept.
type BackupExclusions struct {
	Configs configdoc.ConfigStore
}

// ResetRepository removes key from every backup's stored exclusions.
func (b BackupExclusions) ResetRepository(ctx context.Context, key string) error {
	docs, err := b.Configs.List(ctx, configdoc.ConfigBackup)
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	for _, doc := range docs {
		fields, err := publicDocument(doc)
		if err != nil {
			return err
		}
		list, _ := fields["excludedRepositories"].([]any)
		kept := slices.DeleteFunc(slices.Clone(list), func(item any) bool { return item == key })
		if len(kept) == len(list) {
			continue
		}
		fields["excludedRepositories"] = kept
		if doc.Document, err = json.Marshal(fields); err != nil {
			return fmt.Errorf("backups.%s: %w", doc.Key, err)
		}
		if err := b.Configs.Put(ctx, doc); err != nil {
			return fmt.Errorf("backups.%s: %w", doc.Key, err)
		}
	}
	return nil
}

// RefreshRepository changes nothing: an update keeps the repository's key.
func (BackupExclusions) RefreshRepository(context.Context, string) error { return nil }
