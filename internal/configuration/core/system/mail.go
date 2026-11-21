package system

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// MailServerKey is the private singleton key within ConfigMailServer.
const MailServerKey = "mailserver"

// Mail reads the primary-documented mailServer descriptor fields from the
// existing sealed configuration store. It performs only local metadata reads.
type Mail struct {
	Configs configdoc.ConfigStore
	Tx      tx.Runner
	Guard   func(context.Context) error
}

// Server returns current settings with its password opened separately from the
// public document. Invalid stored settings fail without echoing their contents.
func (m Mail) Server(ctx context.Context) (configapi.MailServer, error) {
	if m.Configs == nil || m.Tx == nil || m.Guard == nil {
		return configapi.MailServer{}, fmt.Errorf("mail configuration snapshot unavailable: %w", kernel.ErrStore)
	}
	var server configapi.MailServer
	err := m.Tx.Tx(ctx, func(ctx context.Context) error {
		if err := m.Guard(ctx); err != nil {
			return fmt.Errorf("guard mail configuration: %w", err)
		}
		var err error
		server, err = m.read(ctx)
		return err
	})
	if err != nil {
		return configapi.MailServer{}, fmt.Errorf("snapshot mail configuration: %w", err)
	}
	return server, nil
}

func (m Mail) read(ctx context.Context) (configapi.MailServer, error) {
	doc, err := m.Configs.Get(ctx, configdoc.ConfigMailServer, MailServerKey)
	if err != nil {
		return configapi.MailServer{}, fmt.Errorf("read mail configuration: %w", err)
	}
	var fields map[string]any
	if json.Unmarshal(doc.Document, &fields) != nil || fields == nil {
		return configapi.MailServer{}, fmt.Errorf("invalid stored mail configuration: %w", kernel.ErrStore)
	}
	if _, present := fields["password"]; present {
		return configapi.MailServer{}, fmt.Errorf("unsealed stored mail password: %w", kernel.ErrStore)
	}
	server, err := MailServerFields(fields)
	if err != nil {
		return configapi.MailServer{}, fmt.Errorf("invalid stored mail settings: %w", kernel.ErrStore)
	}
	server.Enabled = server.Enabled && doc.Enabled
	server.Password = doc.Secrets["password"]
	return server, nil
}

// CheckMailPatch validates supplied fields before opening a store. Partial
// patches may omit the host; complete validation follows the merge at Apply.
func CheckMailPatch(fields map[string]any) error {
	_, err := mailFields(fields, false)
	return err
}

// MailServerFields validates a complete merged document. Defaults follow the
// primary MailServerType XSD: enabled, port25, [JFrog], no TLS or SSL.
func MailServerFields(fields map[string]any) (configapi.MailServer, error) {
	return mailFields(fields, true)
}

func mailFields(fields map[string]any, complete bool) (configapi.MailServer, error) {
	server := configapi.MailServer{Enabled: true, Port: 25, SubjectPrefix: "[JFrog]"}
	stringsTo := map[string]*string{
		"host": &server.Host, "username": &server.Username, "password": &server.Password,
		"from": &server.From, "subjectPrefix": &server.SubjectPrefix, "artifactoryUrl": &server.ArtifactoryURL,
	}
	boolsTo := map[string]*bool{"enabled": &server.Enabled, "tls": &server.TLS, "ssl": &server.SSL}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		value := fields[key]
		if dst, ok := stringsTo[key]; ok {
			if value == nil {
				continue // null clears a string when the patch is merged
			}
			text, ok := value.(string)
			if !ok || !utf8.ValidString(text) || key != "password" && strings.ContainsAny(text, "\r\n\x00") {
				return configapi.MailServer{}, invalidMailField(key)
			}
			*dst = text
			continue
		}
		if dst, ok := boolsTo[key]; ok {
			flag, ok := value.(bool)
			if !ok {
				return configapi.MailServer{}, invalidMailField(key)
			}
			*dst = flag
			continue
		}
		if key != "port" {
			return configapi.MailServer{}, invalidMailField(key)
		}
		var port float64
		switch v := value.(type) {
		case int:
			port = float64(v)
		case float64:
			port = v
		default:
			return configapi.MailServer{}, invalidMailField(key)
		}
		if port < 1 || port > 65535 || math.IsNaN(port) || math.Trunc(port) != port {
			return configapi.MailServer{}, invalidMailField(key)
		}
		server.Port = int(port)
	}
	if err := validateMailServer(server, complete); err != nil {
		return configapi.MailServer{}, err
	}
	return server, nil
}

func validateMailServer(server configapi.MailServer, complete bool) error {
	if complete && server.Enabled && server.Host == "" {
		return invalidMailField("host")
	}
	if strings.ContainsAny(server.Host, "/\\@?#[]") || strings.IndexFunc(server.Host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		strings.Contains(server.Host, ":") && net.ParseIP(server.Host) == nil {
		return invalidMailField("host")
	}
	if server.From != "" {
		if _, err := mail.ParseAddress(server.From); err != nil {
			return invalidMailField("from")
		}
	}
	if server.ArtifactoryURL != "" {
		u, err := url.Parse(server.ArtifactoryURL)
		if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil {
			return invalidMailField("artifactoryUrl")
		}
	}
	return nil
}

func invalidMailField(key string) error {
	return fmt.Errorf("%w: invalid mailServer.%s", configapi.ErrInvalid, key)
}
