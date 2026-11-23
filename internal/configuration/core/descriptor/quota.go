package descriptor

import (
	"context"
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/configuration/core/quota"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// SectionQuota is the global storage quota, <quotaConfig> in the XSD. It is
// one optional mapping whose fields merge into the stored document as
// downloadRedirectConfig's do: a field left out keeps its value.
const SectionQuota = quota.Section

// parseQuota adds the quotaConfig section of top, when present, to p. The
// XSD makes the section and each field optional but types every field
// (boolean, int, int), so a null deletes nothing documented and is refused,
// as on every field that is not a string.
func parseQuota(top map[string]any, p *Patch) error {
	v, ok := top[SectionQuota]
	if !ok {
		return nil
	}
	if v == nil {
		return nullRefused(SectionQuota)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s must be a mapping", ErrInvalid, SectionQuota)
	}
	if _, err := quota.CheckFields(m); err != nil {
		return err //nolint:wrapcheck // names the field and ErrInvalid
	}
	p.Configs = append(p.Configs, ConfigChange{
		Section: SectionQuota, Kind: quota.ConfigKind, Key: quota.ConfigKey, Fields: m,
	})
	return nil
}

// validateQuota checks the merged document. Enabling the quota where the
// hot blob store's volume cannot be measured (object storage) is refused:
// every positive write would then fail closed, and a 200 would claim a limit
// that cannot be applied.
func validateQuota(d Deps, kind configdoc.ConfigKind, doc map[string]any) error {
	if kind != quota.ConfigKind {
		return nil
	}
	p, err := quota.CheckFields(doc)
	if err != nil {
		return err //nolint:wrapcheck // names the field and ErrInvalid
	}
	if p.Enabled && d.Capacity == nil {
		return fmt.Errorf("%w: %s.enabled needs a hot blob store on a local volume whose capacity can be measured",
			ErrInvalid, SectionQuota)
	}
	return nil
}

// readExportQuota renders the stored section with its effective values, the
// XSD defaults filling omitted fields, in QuotaConfigType's order. The
// section is optional in the XSD and is left out while none is stored.
func readExportQuota(ctx context.Context, d Deps, out *exportDocument) error {
	p, stored, err := quota.Read(ctx, d.Configs)
	if err != nil {
		return fmt.Errorf("read export %s: %w", SectionQuota, err)
	}
	if !stored {
		return nil
	}
	out.Quota = &exportFields{Fields: []exportField{
		{Name: quota.FieldEnabled, Text: strconv.FormatBool(p.Enabled)},
		{Name: quota.FieldLimit, Text: strconv.Itoa(p.LimitPercent)},
		{Name: quota.FieldWarning, Text: strconv.Itoa(p.Warning)},
	}}
	return nil
}
