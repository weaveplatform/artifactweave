package webhook

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
)

// NamedSecretInput preserves an omitted value separately from an empty value.
// The value is write-only even if an input is accidentally JSON encoded.
type NamedSecretInput struct {
	Name  string
	Value *string `json:"-"`
}

// HandlerInput carries one handler's configuration and presence-aware secrets.
// Secrets is a replacement list of names, not a patch of the old name set.
type HandlerInput struct {
	Type                model.HandlerType
	URL                 string
	Proxy               string
	Headers             []model.Header
	Method              string
	Payload             string
	UseSecretForSigning bool
	Secret              *string `json:"-"`
	Secrets             []NamedSecretInput
}

// Input replaces public configuration. A nil ProjectKey on update keeps the
// stored immutable project; Key is supplied on create or must match the path.
type Input struct {
	Key         string
	ProjectKey  *string
	Description string
	Enabled     bool
	Debug       bool
	Domain      model.WebhookDomain
	EventTypes  []string
	Criteria    model.WebhookCriteria
	Handlers    []HandlerInput
}

// Service performs secret-safe subscription updates in Store's unit of work.
// Its caller authorizes and validates subscription criteria before publication.
type Service struct {
	Store port.Store
	// Now supplies update timestamps; nil uses time.Now.
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func mergeHandler(in HandlerInput, old model.WebhookHandler) (model.WebhookHandler, error) {
	out := model.WebhookHandler{
		Type: in.Type, URL: in.URL, Proxy: in.Proxy, Headers: kernel.Clone(in.Headers),
		Method: in.Method, Payload: in.Payload, UseSecretForSigning: in.UseSecretForSigning,
	}
	if old.Type != in.Type {
		old = model.WebhookHandler{}
	}
	switch in.Type {
	case model.HandlerWebhook:
		if len(in.Secrets) != 0 {
			return model.WebhookHandler{}, fmt.Errorf("named secrets require a custom handler: %w", kernel.ErrInvalid)
		}
		out.Secret = old.Secret
		if in.Secret != nil {
			out.Secret = *in.Secret
		}
	case model.HandlerCustomWebhook:
		if in.Secret != nil {
			return model.WebhookHandler{}, fmt.Errorf("custom handlers use named secrets: %w", kernel.ErrInvalid)
		}
		values := make(map[string]string, len(old.Secrets))
		for _, secret := range old.Secrets {
			values[secret.Name] = secret.Value
		}
		seen := make(map[string]bool, len(in.Secrets))
		for _, secret := range in.Secrets {
			if !secretName.MatchString(secret.Name) || seen[secret.Name] {
				return model.WebhookHandler{}, fmt.Errorf("secret names must be unique identifiers: %w", kernel.ErrInvalid)
			}
			seen[secret.Name] = true
			value := values[secret.Name]
			if secret.Value != nil {
				value = *secret.Value
			}
			out.Secrets = append(out.Secrets, model.NamedSecret{Name: secret.Name, Value: value})
		}
	default:
		return model.WebhookHandler{}, fmt.Errorf("unsupported handler type: %w", kernel.ErrInvalid)
	}
	return out, nil
}

func subscription(key string, in Input, old model.WebhookSubscription, at time.Time) (model.WebhookSubscription, error) {
	if key == "" || (in.Key != "" && in.Key != key) {
		return model.WebhookSubscription{}, fmt.Errorf("subscription key is required and immutable: %w", kernel.ErrInvalid)
	}
	if len(in.Handlers) != 1 {
		return model.WebhookSubscription{}, fmt.Errorf("exactly one webhook handler is required: %w", kernel.ErrInvalid)
	}
	project := old.ProjectKey
	if in.ProjectKey != nil {
		if old.Key != "" && *in.ProjectKey != old.ProjectKey {
			return model.WebhookSubscription{}, fmt.Errorf("subscription project is immutable: %w", kernel.ErrInvalid)
		}
		project = *in.ProjectKey
	}
	var previous model.WebhookHandler
	if len(old.Handlers) != 0 {
		previous = old.Handlers[0]
	}
	handler, err := mergeHandler(in.Handlers[0], previous)
	if err != nil {
		return model.WebhookSubscription{}, err
	}
	created := old.Created
	if old.Key == "" {
		created = at
	}
	return model.WebhookSubscription{
		Key: key, ProjectKey: project, Description: in.Description, Enabled: in.Enabled, Debug: in.Debug,
		Domain: in.Domain, EventTypes: kernel.Clone(in.EventTypes), Criteria: kernel.Clone(in.Criteria),
		Handlers: []model.WebhookHandler{handler}, Created: created, Updated: at,
	}, nil
}

// Create writes a subscription and returns its safe public configuration.
func (s Service) Create(ctx context.Context, in Input) (View, error) {
	w, err := subscription(in.Key, in, model.WebhookSubscription{}, s.now())
	if err != nil {
		return View{}, err
	}
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Store.Webhooks().Create(ctx, w); err != nil {
			return fmt.Errorf("create webhook subscription: %w", err)
		}
		return nil
	})
	if err != nil {
		return View{}, fmt.Errorf("create webhook transaction: %w", err)
	}
	return Project(w), nil
}

// Update merges secrets into a complete replacement inside one transaction.
func (s Service) Update(ctx context.Context, key string, in Input) (View, error) {
	var out View
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		old, err := s.Store.Webhooks().Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read webhook subscription for update: %w", err)
		}
		w, err := subscription(key, in, old, s.now())
		if err != nil {
			return err
		}
		if err := s.Store.Webhooks().Update(ctx, w); err != nil {
			return fmt.Errorf("update webhook subscription: %w", err)
		}
		out = Project(w)
		return nil
	})
	if err != nil {
		return View{}, fmt.Errorf("update webhook transaction: %w", err)
	}
	return out, nil
}

// Get returns a view, never the privileged complete model containing secrets.
func (s Service) Get(ctx context.Context, key string) (View, error) {
	var out View
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		w, err := s.Store.Webhooks().Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read webhook subscription: %w", err)
		}
		out = Project(w)
		return nil
	})
	if err != nil {
		return View{}, fmt.Errorf("read webhook transaction: %w", err)
	}
	return out, nil
}

// List returns safe views in the store's key order.
func (s Service) List(ctx context.Context, filter port.WebhookFilter) ([]View, error) {
	var out []View
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		all, err := s.Store.Webhooks().List(ctx, filter)
		if err != nil {
			return fmt.Errorf("list webhook subscriptions: %w", err)
		}
		for _, w := range all {
			out = append(out, Project(w))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list webhook transaction: %w", err)
	}
	return out, nil
}
