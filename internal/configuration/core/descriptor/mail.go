package descriptor

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// SectionMailServer is the documented singleton SMTP descriptor section,
// read by quota delivery and the mail test (system.MailTest).
const SectionMailServer = "mailServer"

func parseMailServer(top map[string]any, p *Patch) error {
	value, present := top[SectionMailServer]
	if !present {
		return nil
	}
	// The pinned artifactory_mail_server resource destroys with
	// `mailServer: ~` ([S] provider 12.11.14 Delete), which 7.x merges as a
	// descriptor without a mail server. The document and its sealed password
	// go; deliveries then find no transport. enabled: false disables instead.
	if value == nil {
		p.Configs = append(p.Configs, ConfigChange{
			Section: SectionMailServer, Kind: configdoc.ConfigMailServer, Key: system.MailServerKey,
			Delete: true, singleton: true,
		})
		return nil
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
		Fields: fields, secrets: []string{"password"}, singleton: true,
	})
	return nil
}
