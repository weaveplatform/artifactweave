package descriptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// SectionSystemMessage is the system message banner's settings. 7.161.26
// moves the section out of the YAML before its descriptor merge
// (PlatformConfigServiceImpl.movePlatformResources) into platform
// configuration, and neither its artifactory.xsd (3.5.11) nor the online
// XSD has the element, so the XML export does not carry it.
const SectionSystemMessage = "systemMessageConfig"

// The section is one document of this package's own kind, like
// downloadRedirectConfig's.
const (
	systemMessageKind configdoc.ConfigKind = "system_message"
	systemMessageKey  string               = "default"
)

// defaultTitleColor is PlatformSystemMessageConfig's builder default.
const defaultTitleColor = "#429f46"

var systemMessageStrings = []string{"message", "title", "titleColor"}

// parseSystemMessage reads the four fields movePlatformResources reads. A
// null string clears it, as that method sets null; a null or non-boolean
// enabled and a whole-section null fail there with a server error, so they
// are refused here. The count of values matches its one change per field.
func parseSystemMessage(top map[string]any, p *Patch) error {
	value, present := top[SectionSystemMessage]
	if !present {
		return nil
	}
	if value == nil {
		return nullRefused(SectionSystemMessage)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s must be a mapping", ErrInvalid, SectionSystemMessage)
	}
	if bad := unknown(fields, append([]string{"enabled"}, systemMessageStrings...)); bad != "" {
		return fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, SectionSystemMessage, bad)
	}
	if v, present := fields["enabled"]; present {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%w: %s.enabled must be a boolean", ErrInvalid, SectionSystemMessage)
		}
	}
	if err := configFieldTypes(fields, systemMessageStrings, nil); err != nil {
		return fmt.Errorf("%s: %w", SectionSystemMessage, err)
	}
	p.Configs = append(p.Configs, ConfigChange{
		Section: SectionSystemMessage, Kind: systemMessageKind, Key: systemMessageKey,
		Fields: fields, singleton: true, keepNulls: true,
	})
	return nil
}

// SystemMessage returns the effective settings: an absent document is the
// default, disabled banner. A cleared field is kept as a stored null, because
// movePlatformResources sets null rather than restoring the builder default:
// a cleared titleColor stays empty while one never written is #429f46. The
// banner itself is the platform UI's (P39).
func SystemMessage(ctx context.Context, configs configdoc.ConfigStore) (configapi.SystemMessage, error) {
	out := configapi.SystemMessage{TitleColor: defaultTitleColor}
	d, err := configs.Get(ctx, systemMessageKind, systemMessageKey)
	if errors.Is(err, kernel.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("descriptor: %s: %w", SectionSystemMessage, err)
	}
	var fields map[string]any
	if json.Unmarshal(d.Document, &fields) != nil {
		return out, fmt.Errorf("descriptor: stored %s: %w", SectionSystemMessage, kernel.ErrStore)
	}
	out.Enabled, _ = fields["enabled"].(bool)
	for name, dst := range map[string]*string{"message": &out.Message, "title": &out.Title, "titleColor": &out.TitleColor} {
		if v, present := fields[name]; present {
			*dst, _ = v.(string)
		}
	}
	return out, nil
}
