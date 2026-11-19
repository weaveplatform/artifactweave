package notificationshttp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
)

func TestUnitWebhookHandlerInputPreservesSecretPresence(t *testing.T) {
	for _, tc := range []struct {
		field string
		value *string
	}{
		{}, {field: `,"secret":""`, value: stringPtr("")}, {field: `,"secret":"private-fixture"`, value: stringPtr("private-fixture")},
	} {
		body := []byte(
			`{"handler_type":"webhook","url":"https://receiver.example/hook","proxy":"proxy","custom_http_headers":[{"name":"X-Test","value":"header"}],"use_secret_for_signing":true` + tc.field + `}`,
		)
		var create eventgen.WebhookHandlerInputCreate
		var update eventgen.WebhookHandlerInputUpdate
		if err := json.Unmarshal(body, &create); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &update); err != nil {
			t.Fatal(err)
		}
		for _, parse := range []func() (webhook.HandlerInput, error){
			func() (webhook.HandlerInput, error) { return handlerCreate(create) },
			func() (webhook.HandlerInput, error) { return handlerUpdate(update) },
		} {
			in, err := parse()
			if err != nil {
				t.Fatal(err)
			}
			if (in.Secret == nil) != (tc.value == nil) || (tc.value != nil && *in.Secret != *tc.value) {
				t.Fatalf("secret presence lost: got %v want %v", in.Secret, tc.value)
			}
			if in.Proxy != "proxy" || !in.UseSecretForSigning || len(in.Headers) != 1 {
				t.Fatalf("handler configuration: %+v", in)
			}
		}
	}
}

func stringPtr(value string) *string { return &value }

func TestUnitCustomHandlerInputPreservesNamedValues(t *testing.T) {
	body := []byte(
		`{"handler_type":"custom-webhook","url":"https://receiver.example/{{.secrets.auth}}","payload":"{{json .data}}","http_headers":[{"name":"X-Auth","value":"{{.secrets.auth}}"}],"secrets":[{"name":"auth"},{"name":"empty","value":""},{"name":"rotated","value":"private-fixture"}]}`,
	)
	var create eventgen.WebhookHandlerInputCreate
	var update eventgen.WebhookHandlerInputUpdate
	if err := json.Unmarshal(body, &create); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &update); err != nil {
		t.Fatal(err)
	}
	for _, parse := range []func() (webhook.HandlerInput, error){
		func() (webhook.HandlerInput, error) { return handlerCreate(create) },
		func() (webhook.HandlerInput, error) { return handlerUpdate(update) },
	} {
		in, err := parse()
		if err != nil {
			t.Fatal(err)
		}
		if in.Method != "POST" || in.Secrets[0].Value != nil || *in.Secrets[1].Value != "" || *in.Secrets[2].Value != "private-fixture" || in.Headers[0].Name != "X-Auth" {
			t.Fatalf("custom named-secret presence lost: %+v", in)
		}
	}
}

func TestUnitWebhookHandlerReadBytesNeverContainSecretValues(t *testing.T) {
	for _, configured := range []bool{false, true} {
		view := webhook.HandlerView{
			Type: model.HandlerWebhook, URL: "https://receiver.example/hook", Proxy: "proxy",
			Secret: "private-fixture", SecretConfigured: configured, Headers: []model.Header{{Name: "X-Example", Value: "header"}},
		}
		read, err := handlerRead(view)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(read)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["secret"] != "" || fields["secret_configured"] != configured || strings.Contains(string(body), "private-fixture") {
			t.Fatalf("read wire body leaked a value or lost state: %s", body)
		}
	}
	read, err := handlerRead(webhook.HandlerView{
		Type: model.HandlerCustomWebhook, URL: "https://receiver.example/{{.secrets.auth}}",
		Payload: "{{.data.path}}", Secrets: []webhook.SecretName{{Name: "auth"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"value"`) || strings.Contains(string(body), `"secret"`) || strings.Contains(string(body), `"secret_configured"`) ||
		!strings.Contains(string(body), `"name":"auth"`) {
		t.Fatalf("custom handler did not use the names-only wire model: %s", body)
	}
}

func TestUnitWebhookHandlerErrorsDoNotEchoInput(t *testing.T) {
	for _, body := range []string{`{"handler_type":"private-fixture"}`, `{"handler_type":"webhook","secret":42}`} {
		var create eventgen.WebhookHandlerInputCreate
		var update eventgen.WebhookHandlerInputUpdate
		if err := json.Unmarshal([]byte(body), &create); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(body), &update); err != nil {
			t.Fatal(err)
		}
		for _, parse := range []func() (webhook.HandlerInput, error){
			func() (webhook.HandlerInput, error) { return handlerCreate(create) },
			func() (webhook.HandlerInput, error) { return handlerUpdate(update) },
		} {
			if _, err := parse(); !errors.Is(err, kernel.ErrInvalid) || strings.Contains(err.Error(), "private-fixture") {
				t.Fatalf("handler parse error: %v", err)
			}
		}
	}
	if _, err := handlerRead(webhook.HandlerView{Type: "unknown"}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("unsupported stored handler: %v", err)
	}
}
