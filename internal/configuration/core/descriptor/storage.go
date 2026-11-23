package descriptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The garbage collection schedule and the trash can policy: singleton
// sections whose effects the artifacts module owns (decision 0010), applied
// through its StoragePolicies so configuration never bypasses collection or
// deletion.
const (
	SectionGC       = "gcConfig"
	SectionTrashcan = "trashcanConfig"
)

// StorageChange is the gcConfig and trashcanConfig sections of a patch, with
// which of the two the document named, so the result reports each section it
// was given and no other.
type StorageChange struct {
	artifactsapi.StoragePolicyChange
	GC, Trashcan bool
}

// storageNames spells the owner's fields as the descriptor does.
var storageNames = map[string]string{
	artifactsapi.StoragePolicyGCCron:             SectionGC + ".cronExp",
	artifactsapi.StoragePolicyTrashRetentionDays: SectionTrashcan + ".retentionPeriodDays",
}

// parseStorage reads both sections. Each is a required singleton of the XSD
// (minOccurs 1, as are cronExp, enabled and retentionPeriodDays), so neither
// a whole section nor one of its fields can be deleted with null; a field
// left out keeps its value. allowPermDeletes, which the XSD also has, is not
// in the documented YAML and artifactweave has no such deletion, so it is
// refused rather than stored for nothing.
func parseStorage(top map[string]any, p *Patch) error {
	var change StorageChange
	gc, named, err := storageSection(top, SectionGC, []string{"cronExp"})
	if err != nil {
		return err
	}
	if named {
		change.GC = true
		if v, present := gc["cronExp"]; present {
			s, ok := v.(string)
			switch {
			case v == nil:
				return nullRefused(SectionGC + ".cronExp")
			case !ok:
				return fmt.Errorf("%w: %s.cronExp must be a Quartz cron expression string", ErrInvalid, SectionGC)
			}
			change.GCCron = &s
		}
	}
	trash, named, err := storageSection(top, SectionTrashcan, []string{"enabled", "retentionPeriodDays"})
	if err != nil {
		return err
	}
	if named {
		change.Trashcan = true
		if v, present := trash["enabled"]; present {
			b, ok := v.(bool)
			switch {
			case v == nil:
				return nullRefused(SectionTrashcan + ".enabled")
			case !ok:
				return fmt.Errorf("%w: %s.enabled must be a boolean", ErrInvalid, SectionTrashcan)
			}
			change.TrashEnabled = &b
		}
		if v, present := trash["retentionPeriodDays"]; present {
			// YAML decodes a whole number as int; "14" or 1.5 would leave
			// the period to a guess.
			n, ok := v.(int)
			switch {
			case v == nil:
				return nullRefused(SectionTrashcan + ".retentionPeriodDays")
			case !ok:
				return fmt.Errorf("%w: %s.retentionPeriodDays must be a whole number of days", ErrInvalid, SectionTrashcan)
			}
			change.TrashRetentionDays = &n
		}
	}
	if change.GC || change.Trashcan {
		p.Storage = &change
	}
	return nil
}

// storageSection returns a section's fields and whether the document names it.
func storageSection(top map[string]any, name string, fields []string) (map[string]any, bool, error) {
	v, present := top[name]
	if !present {
		return nil, false, nil
	}
	if v == nil {
		return nil, false, nullRefused(name)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%w: %s must be a mapping", ErrInvalid, name)
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !slices.Contains(fields, k) {
			return nil, false, fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, name, k)
		}
	}
	return m, true, nil
}

// planStorage checks the sections through the owner and names each section
// the document gave: updated when it alters the effective policy, unchanged
// otherwise. The policy always exists, with Artifactory's defaults, so
// neither section is ever created.
func planStorage(ctx context.Context, d Deps, p *Patch, now time.Time, res *configapi.Result) error {
	if p.Storage == nil {
		return nil
	}
	if d.StoragePolicies == nil {
		return fmt.Errorf("%s/%s: no storage policy binding: %w", SectionGC, SectionTrashcan, kernel.ErrStore)
	}
	r, err := d.StoragePolicies.Plan(ctx, p.Storage.StoragePolicyChange, now)
	if err != nil {
		return storageError(err)
	}
	for _, section := range []struct {
		name           string
		named, changed bool
	}{{SectionGC, p.Storage.GC, r.GCChanged}, {SectionTrashcan, p.Storage.Trashcan, r.TrashChanged}} {
		switch {
		case !section.named:
		case section.changed:
			res.Updated = append(res.Updated, section.name)
		default:
			res.Unchanged = append(res.Unchanged, section.name)
		}
	}
	return nil
}

// applyStorage writes the sections in the patch's transaction: a later
// write that fails rolls back the trash can settings and the collector's
// schedule with everything else.
func applyStorage(ctx context.Context, d Deps, p *Patch, now time.Time) error {
	if p.Storage == nil {
		return nil
	}
	if _, err := d.StoragePolicies.Apply(ctx, p.Storage.StoragePolicyChange, now); err != nil {
		return fmt.Errorf("%s/%s: %w", SectionGC, SectionTrashcan, storageError(err))
	}
	return nil
}

func storageError(err error) error {
	var refused *artifactsapi.StoragePolicyInvalid
	if errors.As(err, &refused) {
		return fmt.Errorf("%w: %s %s", ErrInvalid, storageNames[refused.Field], refused.Reason)
	}
	return err
}

// readExportStorage renders the effective policy, defaults included: both
// sections are required by the XSD, and the artifactory_trashcan_config
// resource drops itself from state when its section is missing.
func readExportStorage(ctx context.Context, d Deps, out *exportDocument) error {
	if d.StoragePolicies == nil {
		return nil
	}
	policy, err := d.StoragePolicies.Current(ctx)
	if err != nil {
		return fmt.Errorf("read export storage policy: %w", err)
	}
	out.GC = &exportFields{Fields: []exportField{{Name: "cronExp", Text: policy.GCCron}}}
	out.Trashcan = &exportFields{Fields: []exportField{
		{Name: "enabled", Text: strconv.FormatBool(policy.TrashEnabled)},
		{Name: "retentionPeriodDays", Text: strconv.Itoa(policy.TrashRetentionDays)},
	}}
	return nil
}
