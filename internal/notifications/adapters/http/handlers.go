package notificationshttp

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

func handlerHeaders(headers *[]eventgen.HttpHeader) []model.Header {
	out := make([]model.Header, 0, len(httpkit.Deref(headers)))
	for _, h := range httpkit.Deref(headers) {
		out = append(out, model.Header{Name: h.Name, Value: h.Value})
	}
	return out
}

func plainHandlerInput(b eventgen.WebhookHandler) webhook.HandlerInput {
	return webhook.HandlerInput{
		Type: model.HandlerWebhook, URL: b.Url, Proxy: httpkit.Deref(b.Proxy),
		Secret: kernel.Clone(b.Secret), UseSecretForSigning: httpkit.Deref(b.UseSecretForSigning),
		Headers: handlerHeaders(b.CustomHttpHeaders),
	}
}

func handlerCreate(in eventgen.WebhookHandlerInputCreate) (webhook.HandlerInput, error) {
	v, err := in.ValueByDiscriminator()
	if err != nil {
		return webhook.HandlerInput{}, fmt.Errorf("invalid webhook handler: %w", kernel.ErrInvalid)
	}
	switch b := v.(type) {
	case eventgen.WebhookHandler:
		return plainHandlerInput(b), nil
	case eventgen.CustomWebhookHandlerCreate:
		out := webhook.HandlerInput{
			Type: model.HandlerCustomWebhook, URL: b.Url, Proxy: httpkit.Deref(b.Proxy),
			Method: string(httpkit.Deref(b.Method)), Payload: httpkit.Deref(b.Payload), Headers: handlerHeaders(b.HttpHeaders),
		}
		if out.Method == "" {
			out.Method = "POST"
		}
		for _, secret := range httpkit.Deref(b.Secrets) {
			out.Secrets = append(out.Secrets, webhook.NamedSecretInput{Name: secret.Name, Value: kernel.Clone(secret.Value)})
		}
		return out, nil
	default:
		return webhook.HandlerInput{}, fmt.Errorf("unsupported webhook handler: %w", kernel.ErrInvalid)
	}
}

func handlerUpdate(in eventgen.WebhookHandlerInputUpdate) (webhook.HandlerInput, error) {
	v, err := in.ValueByDiscriminator()
	if err != nil {
		return webhook.HandlerInput{}, fmt.Errorf("invalid webhook handler: %w", kernel.ErrInvalid)
	}
	switch b := v.(type) {
	case eventgen.WebhookHandler:
		return plainHandlerInput(b), nil
	case eventgen.CustomWebhookHandlerUpdate:
		out := webhook.HandlerInput{
			Type: model.HandlerCustomWebhook, URL: b.Url, Proxy: httpkit.Deref(b.Proxy),
			Method: string(httpkit.Deref(b.Method)), Payload: httpkit.Deref(b.Payload), Headers: handlerHeaders(b.HttpHeaders),
		}
		if out.Method == "" {
			out.Method = "POST"
		}
		for _, secret := range httpkit.Deref(b.Secrets) {
			out.Secrets = append(out.Secrets, webhook.NamedSecretInput{Name: secret.Name, Value: kernel.Clone(secret.Value)})
		}
		return out, nil
	default:
		return webhook.HandlerInput{}, fmt.Errorf("unsupported webhook handler: %w", kernel.ErrInvalid)
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func handlerRead(h webhook.HandlerView) (eventgen.WebhookHandlerResponse, error) {
	var out eventgen.WebhookHandlerResponse
	headers := make([]eventgen.HttpHeader, 0, len(h.Headers))
	for _, header := range h.Headers {
		headers = append(headers, eventgen.HttpHeader{Name: header.Name, Value: header.Value})
	}
	switch h.Type {
	case model.HandlerWebhook:
		body := eventgen.WebhookHandlerRead{
			HandlerType: eventgen.WebhookHandlerReadHandlerType(model.HandlerWebhook), Url: h.URL,
			Proxy: optionalString(h.Proxy), Secret: httpkit.PtrTo(""), SecretConfigured: httpkit.PtrTo(h.SecretConfigured),
			UseSecretForSigning: httpkit.PtrTo(h.UseSecretForSigning), CustomHttpHeaders: &headers,
		}
		if err := out.FromWebhookHandlerRead(body); err != nil {
			return eventgen.WebhookHandlerResponse{}, fmt.Errorf("encode webhook handler view: %w", err)
		}
	case model.HandlerCustomWebhook:
		names := make([]eventgen.NamedSecretRead, 0, len(h.Secrets))
		for _, secret := range h.Secrets {
			names = append(names, eventgen.NamedSecretRead{Name: secret.Name})
		}
		method := eventgen.CustomWebhookHandlerReadMethod(h.Method)
		if method == "" {
			method = "POST"
		}
		body := eventgen.CustomWebhookHandlerRead{
			HandlerType: eventgen.CustomWebhookHandlerReadHandlerType(model.HandlerCustomWebhook), Url: h.URL,
			Proxy: optionalString(h.Proxy), Method: &method, Payload: optionalString(h.Payload), HttpHeaders: &headers, Secrets: &names,
		}
		if err := out.FromCustomWebhookHandlerRead(body); err != nil {
			return eventgen.WebhookHandlerResponse{}, fmt.Errorf("encode custom handler view: %w", err)
		}
	default:
		return eventgen.WebhookHandlerResponse{}, fmt.Errorf("unsupported stored webhook handler: %w", kernel.ErrInvalid)
	}
	return out, nil
}
