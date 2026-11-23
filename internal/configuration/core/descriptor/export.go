package descriptor

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

var _ configapi.Export = exportAPI{}

type exportAPI struct{ d Deps }

// Export must own its unit of work: a plain Runner joins an ambient transaction
// and could return XML before that transaction commits or rolls back.
type exportTransactions interface {
	tx.Runner
	InTx(context.Context) bool
}

// exportDocument is the section extension point. Add a typed field in XSD order
// and collect it inside readExport's guarded transaction; never append raw XML.
// The namespace and names follow 7.161.26's artifactory.xsd, not the YAML aliases.
// Pointer sections retain the distinction between absent and explicitly empty.
type exportDocument struct {
	XMLName          xml.Name       `xml:"http://artifactory.jfrog.org/xsd/3.5.11 config"`
	Mail             *exportFields  `xml:"mailServer,omitempty"`
	Security         exportSecurity `xml:"security"`
	Local            []exportFields `xml:"localRepositories>localRepository"`
	Remote           []exportFields `xml:"remoteRepositories>remoteRepository"`
	Virtual          []exportFields `xml:"virtualRepositories>virtualRepository"`
	Federated        []exportFields `xml:"federatedRepositories>federatedRepository"`
	Proxies          []exportFields `xml:"proxies>proxy"`
	ReverseProxies   []exportFields `xml:"reverseProxies>reverseProxy"`
	PropertySets     []exportFields `xml:"propertySets>propertySet"`
	URLBase          *string        `xml:"urlBase,omitempty"`
	Layouts          []exportFields `xml:"repoLayouts>repoLayout"`
	DownloadRedirect *exportFields  `xml:"downloadRedirectConfig,omitempty"`
}

type exportSecurity struct {
	Anonymous bool           `xml:"anonAccessEnabled"`
	LDAP      []exportFields `xml:"ldapSettings>ldapSetting"`
}

type exportFields struct {
	Fields []exportField `xml:",any"`
}

// Field names are authored projection paths below; resource names are text.
// Keeping text and child elements separate lets encoding/xml escape all data.
type exportField struct {
	Name     string
	Text     string
	Children []exportField
}

func (f exportField) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	content := struct {
		Text     string        `xml:",chardata"`
		Children []exportField `xml:",any"`
	}{f.Text, f.Children}
	// EncodeElement owns the complete balanced element and propagates encoder
	// failures. Resource data never supplies element names.
	return e.EncodeElement(content, xml.StartElement{Name: xml.Name{Local: f.Name}}) //nolint:wrapcheck // preserve the Marshaler protocol's encoder failures
}

func (a exportAPI) XML(ctx context.Context) ([]byte, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	transactions, ok := a.d.Tx.(exportTransactions)
	if !ok {
		return nil, fmt.Errorf("export transaction state unavailable: %w", kernel.ErrStore)
	}
	if transactions.InTx(ctx) {
		return nil, fmt.Errorf("export cannot join an existing transaction: %w", kernel.ErrStore)
	}
	var document exportDocument
	err := transactions.Tx(ctx, func(ctx context.Context) error {
		if a.d.Projects != nil {
			if err := a.d.Projects.Guard(ctx); err != nil {
				return fmt.Errorf("guard configuration export: %w", err)
			}
		}
		if err := a.currentAdmin(ctx); err != nil {
			return err
		}
		var err error
		document, err = readExport(ctx, a.d)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("export configuration: %w", err)
	}
	body, err := xml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}

func (a exportAPI) currentAdmin(ctx context.Context) error {
	if a.d.Authenticator == nil && a.d.Authorizer == nil {
		// Trusted store embeddings establish the administrator before calling XML;
		// HTTP bindings must supply both current identity and effective authority.
		return nil
	}
	if a.d.Authenticator == nil || a.d.Authorizer == nil {
		return fmt.Errorf("incomplete export authority bindings: %w", kernel.ErrStore)
	}
	p, _ := kernel.FromContext(ctx)
	p, err := a.d.Authenticator.Current(ctx, p)
	if err != nil {
		return fmt.Errorf("current configuration caller: %w", err)
	}
	decider, err := a.d.Authorizer.For(ctx, p)
	if err != nil {
		return fmt.Errorf("authorize configuration export: %w", err)
	}
	if !decider.IsAdmin() {
		return kernel.ErrForbidden
	}
	return nil
}

func readExport(ctx context.Context, d Deps) (exportDocument, error) {
	var out exportDocument
	repos, err := d.Repositories.List(ctx, artifactsapi.RepoFilter{})
	if err != nil {
		return out, fmt.Errorf("list export repositories: %w", err)
	}
	for _, c := range repos {
		fields, err := exportRepository(c)
		if err != nil {
			return out, err
		}
		switch c.Class {
		case repo.Local:
			out.Local = append(out.Local, fields)
		case repo.Remote:
			out.Remote = append(out.Remote, fields)
		case repo.Virtual:
			out.Virtual = append(out.Virtual, fields)
		case repo.Federated:
			out.Federated = append(out.Federated, fields)
		}
	}
	for _, section := range []struct {
		kind     configdoc.ConfigKind
		identity string
		fields   []string
		target   *[]exportFields
	}{
		{configdoc.ConfigProxy, "key", []string{"host", "port", "username", "ntHost", "domain", "platformDefault", "redirectedToHosts", "services"}, &out.Proxies},
		{configdoc.ConfigPropertySet, "name", []string{"visible"}, &out.PropertySets},
		{configdoc.ConfigLayout, "name", []string{"artifactPathPattern", "distinctiveDescriptorPathPattern", "descriptorPathPattern", "folderIntegrationRevisionRegExp", "fileIntegrationRevisionRegExp"}, &out.Layouts},
	} {
		docs, err := d.Configs.List(ctx, section.kind)
		if err != nil {
			return out, fmt.Errorf("list export %s: %w", section.kind, err)
		}
		for _, doc := range docs {
			fields, err := publicDocument(doc)
			if err != nil {
				return out, err
			}
			entry := exportFields{Fields: []exportField{{Name: section.identity, Text: doc.Key}}}
			for _, key := range section.fields {
				if value, present := fields[key]; present {
					// Writes accept only the comma-delimited strings that the pinned
					// provider sends (#1839); documents stored before then may hold
					// lists, which get the same XML projection.
					if key == "services" || key == "redirectedToHosts" {
						value = commaList(value)
					}
					entry.Fields = append(entry.Fields, exportValue(key, value))
				}
			}
			if section.kind == configdoc.ConfigPropertySet {
				entry.Fields = append(entry.Fields, exportProperties(fields["properties"]))
			}
			*section.target = append(*section.target, entry)
		}
	}
	if out.Security, err = readExportSecurity(ctx, d); err != nil {
		return out, err
	}
	for _, section := range []struct {
		kind   configdoc.ConfigKind
		key    string
		fields []string
		target **exportFields
	}{
		{configdoc.ConfigMailServer, system.MailServerKey, []string{"enabled", "host", "port", "username", "from", "subjectPrefix", "tls", "ssl", "artifactoryUrl"}, &out.Mail},
		{downloadRedirectKind, downloadRedirectKey, []string{fileMinimumSize}, &out.DownloadRedirect},
	} {
		doc, err := d.Configs.Get(ctx, section.kind, section.key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("read export %s: %w", section.kind, err)
		}
		fields, err := publicDocument(doc)
		if err != nil {
			return out, err
		}
		if section.kind == configdoc.ConfigMailServer {
			// These are effective settings, also used by mail delivery. The provider
			// decodes XML without an XSD, so omitting defaults would read true/25 as
			// false/0. Explicitly empty strings still keep their stored meaning.
			settings := maps.Clone(fields)
			if number, ok := settings["port"].(json.Number); ok {
				port, err := number.Float64()
				if err != nil {
					return out, fmt.Errorf("invalid export mail port: %w", kernel.ErrStore)
				}
				settings["port"] = port
			}
			server, err := system.MailServerFields(settings)
			if err != nil {
				return out, fmt.Errorf("invalid export mail settings: %w", kernel.ErrStore)
			}
			fields["enabled"] = server.Enabled && doc.Enabled
			fields["port"] = server.Port
			fields["subjectPrefix"] = server.SubjectPrefix
			fields["ssl"], fields["tls"] = server.SSL, server.TLS
		}
		entry := &exportFields{}
		for _, key := range section.fields {
			if value, ok := fields[key]; ok {
				entry.Fields = append(entry.Fields, exportValue(key, value))
			}
		}
		*section.target = entry
	}
	if err := readWebServerExport(ctx, d, &out); err != nil {
		return out, err
	}
	return out, nil
}

func publicDocument(doc configdoc.ConfigDocument) (map[string]any, error) {
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(doc.Document))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return nil, fmt.Errorf("decode export %s %s: %w", doc.Kind, doc.Key, err)
	}
	if fields == nil {
		return nil, fmt.Errorf("invalid export %s %s: %w", doc.Kind, doc.Key, kernel.ErrStore)
	}
	return fields, nil
}

func commaList(value any) any {
	if list, ok := value.([]any); ok {
		values := make([]string, 0, len(list))
		for _, item := range list {
			values = append(values, fmt.Sprint(item))
		}
		return strings.Join(values, ",")
	}
	return value
}

func exportProperties(value any) exportField {
	out := exportField{Name: "properties"}
	props, _ := value.(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		fields, _ := props[name].(map[string]any)
		property := exportField{Name: "property", Children: []exportField{{Name: "name", Text: name}}}
		for _, key := range []string{"closedPredefinedValues", "multipleChoice"} {
			if value, ok := fields[key]; ok {
				property.Children = append(property.Children, exportValue(key, value))
			}
		}
		values := exportField{Name: "predefinedValues"}
		predefined, _ := fields["predefinedValues"].([]any)
		// The pinned provider PATCH names values as map keys; YAML descriptors
		// also store lists. Normalize both without treating names as XML tags.
		if named, ok := fields["predefinedValues"].(map[string]any); ok {
			for _, name := range slices.Sorted(maps.Keys(named)) {
				entry := map[string]any{"value": name}
				if fields, ok := named[name].(map[string]any); ok {
					if value, present := fields["defaultValue"]; present {
						entry["defaultValue"] = value
					}
				}
				predefined = append(predefined, entry)
			}
		}
		for _, value := range predefined {
			entry := exportField{Name: "predefinedValue"}
			if fields, ok := value.(map[string]any); ok {
				for _, key := range []string{"value", "defaultValue"} {
					if value, present := fields[key]; present {
						entry.Children = append(entry.Children, exportValue(key, value))
					}
				}
			} else {
				entry.Children = []exportField{exportValue("value", value)}
			}
			values.Children = append(values.Children, entry)
		}
		property.Children = append(property.Children, values)
		out.Children = append(out.Children, property)
	}
	return out
}

func exportValue(name string, value any) exportField {
	field := exportField{Name: name}
	if value == nil {
		return field
	}
	field.Text = fmt.Sprint(value)
	return field
}
