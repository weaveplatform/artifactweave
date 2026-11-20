package descriptor

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// SectionMailServer is the documented singleton SMTP descriptor section. It
// supplies quota delivery's narrow dependency; full mail administration is P12.
const SectionMailServer = "mailServer"

func parseMailServer(top map[string]any, p *Patch) error {
	value, present := top[SectionMailServer]
	if !present {
		return nil
	}
	// A singleton is disabled explicitly. Whole-section null semantics are not
	// documented, so retain the descriptor's refusal instead of guessing delete.
	if value == nil {
		return nullRefused(SectionMailServer)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: mailServer must be a mapping", ErrInvalid)
	}
	if err := system.CheckMailPatch(fields); err != nil {
		return err //nolint:wrapcheck // the system validator names its field and ErrInvalid
	}
	p.Configs = append(p.Configs, ConfigChange{
		Section: SectionMailServer, Kind: configdoc.ConfigMailServer, Key: system.MailServerKey,
		Fields: fields, secrets: []string{"password"},
	})
	return nil
}
