package webhook

import (
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
)

// SecretName is the only public information about a named custom secret.
type SecretName struct {
	Name string `json:"name"`
}

// HandlerView exposes redacted secret state without copying a stored value.
// Secret is always empty; SecretConfigured is meaningful for webhook handlers.
// The HTTP adapter chooses its generated read variant by Type.
type HandlerView struct {
	Type                model.HandlerType
	URL                 string
	Proxy               string
	Headers             []model.Header
	Method              string
	Payload             string
	UseSecretForSigning bool
	Secret              string `json:"secret"`
	SecretConfigured    bool   `json:"secret_configured"`
	Secrets             []SecretName
}

// View is nonsecret subscription configuration for explicit adapter conversion.
// It does not expose storage timestamps or private handler models.
type View struct {
	Key         string
	ProjectKey  string
	Description string
	Enabled     bool
	Debug       bool
	Domain      model.WebhookDomain
	EventTypes  []string
	Criteria    model.WebhookCriteria
	Handlers    []HandlerView
}

// Project builds a detached nonsecret view of a privileged storage model.
func Project(w model.WebhookSubscription) View {
	out := View{
		Key: w.Key, ProjectKey: w.ProjectKey, Description: w.Description, Enabled: w.Enabled, Debug: w.Debug,
		Domain: w.Domain, EventTypes: kernel.Clone(w.EventTypes), Criteria: kernel.Clone(w.Criteria),
		Handlers: make([]HandlerView, 0, len(w.Handlers)),
	}
	for _, h := range w.Handlers {
		view := HandlerView{
			Type: h.Type, URL: h.URL, Proxy: h.Proxy, Headers: kernel.Clone(h.Headers),
			Method: h.Method, Payload: h.Payload, UseSecretForSigning: h.UseSecretForSigning,
			SecretConfigured: h.Type == model.HandlerWebhook && h.Secret != "",
		}
		for _, secret := range h.Secrets {
			view.Secrets = append(view.Secrets, SecretName{Name: secret.Name})
		}
		out.Handlers = append(out.Handlers, view)
	}
	return out
}
