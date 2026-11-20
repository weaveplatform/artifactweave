package webhook_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
)

func validInput() webhook.Input {
	in := testInput()
	in.Domain, in.EventTypes = model.DomainArtifact, []string{"deployed"}
	return in
}

func TestUnitWebhookValidationRefusesMalformedConfiguration(t *testing.T) {
	for name, change := range map[string]func(*webhook.Input){
		"empty key":       func(in *webhook.Input) { in.Key = "" },
		"one letter":      func(in *webhook.Input) { in.Key = "a" },
		"digit first":     func(in *webhook.Input) { in.Key = "1hook" },
		"path key":        func(in *webhook.Input) { in.Key = "a/hook" },
		"unicode key":     func(in *webhook.Input) { in.Key = "aé" },
		"long key":        func(in *webhook.Input) { in.Key = strings.Repeat("a", 501) },
		"unknown domain":  func(in *webhook.Input) { in.Domain = "unknown" },
		"missing events":  func(in *webhook.Input) { in.EventTypes = nil },
		"wrong event":     func(in *webhook.Input) { in.EventTypes = []string{"pushed"} },
		"no handler":      func(in *webhook.Input) { in.Handlers = nil },
		"two handlers":    func(in *webhook.Input) { in.Handlers = append(in.Handlers, in.Handlers[0]) },
		"unknown handler": func(in *webhook.Input) { in.Handlers[0].Type = "unknown" },
		"relative URL":    func(in *webhook.Input) { in.Handlers[0].URL = "/hook" },
		"other scheme":    func(in *webhook.Input) { in.Handlers[0].URL = "file:///tmp/hook" },
		"no host":         func(in *webhook.Input) { in.Handlers[0].URL = "https:///hook" },
		"bad URL":         func(in *webhook.Input) { in.Handlers[0].URL = "https://[bad" },
		"bad header name": func(in *webhook.Input) { in.Handlers[0].Headers = []model.Header{{Name: "bad header"}} },
		"header injection": func(in *webhook.Input) {
			in.Handlers[0].Headers = []model.Header{{Name: "X-Token", Value: "one\r\nTwo: three"}}
		},
		"duplicate header": func(in *webhook.Input) {
			in.Handlers[0].Headers = []model.Header{{Name: "X-Token"}, {Name: "x-token"}}
		},
		"wrong secrets": func(in *webhook.Input) { in.Handlers[0].Secrets = []webhook.NamedSecretInput{{Name: "token"}} },
	} {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			change(&in)
			if err := (webhook.Validator{}).Validate(t.Context(), in); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("malformed configuration: %v", err)
			}
		})
	}
	in := validInput()
	in.Key = strings.Repeat("a", 500)
	if err := (webhook.Validator{}).Validate(t.Context(), in); err != nil {
		t.Fatalf("maximum key: %v", err)
	}
}

func TestUnitWebhookCustomTemplateValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		url, method, payload, header string
		invalid                      bool
	}{
		"documented":       {url: "https://{{.secrets.host}}/hook?token={{.secrets.token}}", payload: `{"event":{{json .data}},"user":"{{.userContext.id}}"}`, header: "Bearer {{.secrets.token}}"},
		"builtins":         {url: "https://receiver.example", method: "PATCH", payload: `{{if .data}}{{printf "%s" .userContext.id}}{{end}}`},
		"empty body":       {url: "http://receiver.example", method: "DELETE"},
		"bad payload":      {url: "https://receiver.example", payload: "{{.secrets.private", invalid: true},
		"bad URL":          {url: "https://receiver.example/{{.data", invalid: true},
		"bad header":       {url: "https://receiver.example", header: "{{.secrets.token", invalid: true},
		"unknown function": {url: "https://receiver.example", payload: `{{env "PRIVATE"}}`, invalid: true},
		"bad method":       {url: "https://receiver.example", method: "HEAD", invalid: true},
		"large payload":    {url: "https://receiver.example", payload: strings.Repeat("a", webhook.MaxTemplateBytes+1), invalid: true},
		"large header":     {url: "https://receiver.example", header: strings.Repeat("a", webhook.MaxTemplateBytes+1), invalid: true},
		"large URL":        {url: "https://receiver.example/" + strings.Repeat("a", webhook.MaxTemplateBytes), invalid: true},
	} {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			in.Handlers = []webhook.HandlerInput{{Type: model.HandlerCustomWebhook, URL: tc.url, Method: tc.method, Payload: tc.payload}}
			if tc.header != "" {
				in.Handlers[0].Headers = []model.Header{{Name: "Authorization", Value: tc.header}}
			}
			err := (webhook.Validator{}).Validate(t.Context(), in)
			if tc.invalid && !errors.Is(err, kernel.ErrInvalid) || !tc.invalid && err != nil {
				t.Fatalf("invalid=%v, err=%v", tc.invalid, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("template refusal reflected input")
			}
		})
	}
}

type validationRepositories struct {
	artifactsapi.Repositories
	get func(context.Context, string) (repo.Config, error)
}

func (r validationRepositories) Get(ctx context.Context, key string) (repo.Config, error) {
	return r.get(ctx, key)
}

func TestUnitWebhookCriteriaCheckOnlyRelevantRepositories(t *testing.T) {
	in := validInput()
	in.Criteria.RepoKeys = []string{"libs"}
	if err := (webhook.Validator{}).Validate(t.Context(), in); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("absent repository binding: %v", err)
	}
	for _, reply := range []error{nil, kernel.ErrNotFound, errFailure} {
		calls := 0
		v := webhook.Validator{Repositories: validationRepositories{get: func(ctx context.Context, key string) (repo.Config, error) {
			calls++
			if ctx != t.Context() || key != "libs" {
				t.Fatal("repository check lost its context or key")
			}
			return repo.Config{Key: key}, reply
		}}}
		err := v.Validate(t.Context(), in)
		want := reply
		if errors.Is(reply, kernel.ErrNotFound) {
			want = kernel.ErrInvalid
		}
		if calls != 1 || !errors.Is(err, want) {
			t.Fatalf("repository calls=%d, err=%v, want=%v", calls, err, want)
		}
	}
	in.Domain, in.EventTypes = model.DomainBuild, []string{"uploaded"}
	if err := (webhook.Validator{}).Validate(t.Context(), in); err != nil {
		t.Fatalf("irrelevant repository criteria were interpreted: %v", err)
	}
}
