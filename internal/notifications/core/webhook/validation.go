package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"text/template/parse"

	"golang.org/x/net/http/httpguts"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
)

// MaxTemplateBytes bounds each parsed URL, header value and payload. This is
// a local parser resource limit, not an observed vendor delivery limit.
const MaxTemplateBytes = 64 << 10

var subscriptionKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]+$`)

// Validator checks administration input without resolving hosts, rendering
// templates or sending requests. The caller authorizes before it checks named
// repositories, and retains its authority guard through subscription publication.
type Validator struct {
	Repositories artifactsapi.Repositories
}

// Validate checks the documented key, handler and event/criteria contracts.
// The HTTP adapter checks presence of the criteria object, whose empty value
// remains valid. Irrelevant criteria are retained, as the authored API specifies.
func (v Validator) Validate(ctx context.Context, in Input) error {
	if len(in.Key) > 500 || !subscriptionKey.MatchString(in.Key) {
		return fmt.Errorf("invalid subscription key: %w", kernel.ErrInvalid)
	}
	if err := model.ValidateEventTypes(in.Domain, in.EventTypes); err != nil {
		return fmt.Errorf("invalid subscription events: %w: %w", kernel.ErrInvalid, err)
	}
	if len(in.Handlers) != 1 {
		return fmt.Errorf("exactly one handler is required: %w", kernel.ErrInvalid)
	}
	if err := validateHandler(in.Handlers[0]); err != nil {
		return err
	}
	if !repositoryDomain(in.Domain) || len(in.Criteria.RepoKeys) == 0 {
		return nil
	}
	if v.Repositories == nil {
		return fmt.Errorf("webhook repository reader is unavailable: %w", kernel.ErrStore)
	}
	for _, key := range in.Criteria.RepoKeys {
		if _, err := v.Repositories.Get(ctx, key); errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("webhook criteria name an unknown repository: %w", kernel.ErrInvalid)
		} else if err != nil {
			return fmt.Errorf("validate webhook repository: %w", err)
		}
	}
	return nil
}

func repositoryDomain(domain model.WebhookDomain) bool {
	return domain == model.DomainArtifact || domain == model.DomainArtifactProperty ||
		domain == model.DomainDocker || domain == model.DomainArtifactLifecycle
}

func validateHandler(h HandlerInput) error {
	custom := h.Type == model.HandlerCustomWebhook
	if !custom && h.Type != model.HandlerWebhook {
		return fmt.Errorf("unsupported webhook handler: %w", kernel.ErrInvalid)
	}
	address := h.URL
	if custom {
		parsed, err := parseTemplate(address)
		if err != nil {
			return fmt.Errorf("invalid URL template: %w", err)
		}
		// Check an absolute URL shape without evaluating secrets or event data.
		// Delivery must validate the eventual rendered URL again in its owner.
		var shape strings.Builder
		for _, node := range parsed.Root.Nodes {
			if text, ok := node.(*parse.TextNode); ok {
				shape.Write(text.Text)
			} else {
				shape.WriteString("value")
			}
		}
		address = shape.String()
		if err := customMethod(h.Method); err != nil {
			return err
		}
		if _, err := parseTemplate(h.Payload); err != nil {
			return fmt.Errorf("invalid payload template: %w", err)
		}
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return fmt.Errorf("webhook URL must be absolute HTTP or HTTPS: %w", kernel.ErrInvalid)
	}
	seen := make(map[string]bool, len(h.Headers))
	for _, header := range h.Headers {
		name := strings.ToLower(header.Name)
		if !httpguts.ValidHeaderFieldName(header.Name) || !httpguts.ValidHeaderFieldValue(header.Value) || seen[name] {
			return fmt.Errorf("webhook headers must be valid and unique: %w", kernel.ErrInvalid)
		}
		seen[name] = true
		if custom {
			if _, err := parseTemplate(header.Value); err != nil {
				return fmt.Errorf("invalid header template: %w", err)
			}
		}
	}
	// Secret merge owns name, discriminator and presence semantics. Validate
	// them here too so an invalid request never reaches a storage operation.
	_, err = mergeHandler(h, model.WebhookHandler{})
	return err
}

func customMethod(method string) error {
	switch method {
	case "", http.MethodPost, http.MethodPut, http.MethodGet, http.MethodPatch, http.MethodDelete:
		return nil
	default:
		return fmt.Errorf("unsupported custom webhook method: %w", kernel.ErrInvalid)
	}
}

func parseTemplate(source string) (*template.Template, error) {
	if len(source) > MaxTemplateBytes {
		return nil, fmt.Errorf("webhook template exceeds parser limit: %w", kernel.ErrInvalid)
	}
	// This binding checks that the documented json name is accepted; no
	// parsed template is executed by subscription administration.
	parsed, err := template.New("webhook").Funcs(template.FuncMap{"json": json.Marshal}).Parse(source)
	if err != nil {
		// Parser errors can contain input fragments, including an inline
		// credential. Keep the public/logged refusal free of those values.
		return nil, fmt.Errorf("webhook template syntax is invalid: %w", kernel.ErrInvalid)
	}
	return parsed, nil
}
