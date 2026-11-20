package notificationshttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// SubscriptionsAPI is the Event partial strict server for subscription CRUD.
// The composition root supplies current project-aware administration. Test
// delivery and troubleshooting retain their separate generated 501 owners.
type SubscriptionsAPI struct {
	Subscriptions webhook.Administration
}

func subscriptionRead(view webhook.View) (eventgen.WebhookSubscription, error) {
	handlers := make([]eventgen.WebhookHandlerResponse, 0, len(view.Handlers))
	for _, h := range view.Handlers {
		handler, err := handlerRead(h)
		if err != nil {
			return eventgen.WebhookSubscription{}, err
		}
		handlers = append(handlers, handler)
	}
	return eventgen.WebhookSubscription{
		Key: httpkit.PtrTo(view.Key), Description: httpkit.PtrTo(view.Description),
		ProjectKey: optionalString(view.ProjectKey), Enabled: httpkit.PtrTo(view.Enabled), Debug: httpkit.PtrTo(view.Debug),
		EventFilter: &eventgen.EventFilter{Domain: string(view.Domain), EventTypes: kernel.Clone(view.EventTypes), Criteria: criteriaRead(view.Criteria)},
		Handlers:    &handlers,
	}, nil
}

func createInput(body *eventgen.WebhookSubscriptionCreate) (webhook.Input, error) {
	if body == nil {
		return webhook.Input{}, httpkit.BadRequestf("a subscription body is required")
	}
	criteria, err := criteriaInput(body.EventFilter)
	if err != nil {
		return webhook.Input{}, err
	}
	in := webhook.Input{
		Key: body.Key, ProjectKey: kernel.Clone(body.ProjectKey), Description: httpkit.Deref(body.Description),
		Enabled: body.Enabled, Debug: httpkit.Deref(body.Debug),
		Domain: model.WebhookDomain(body.EventFilter.Domain), EventTypes: kernel.Clone(body.EventFilter.EventTypes), Criteria: criteria,
	}
	for _, handler := range body.Handlers {
		h, err := handlerCreate(handler)
		if err != nil {
			return webhook.Input{}, err
		}
		in.Handlers = append(in.Handlers, h)
	}
	return in, nil
}

func updateInput(key string, body *eventgen.WebhookSubscriptionUpdate) (webhook.Input, error) {
	if body == nil {
		return webhook.Input{}, httpkit.BadRequestf("a subscription body is required")
	}
	criteria, err := criteriaInput(body.EventFilter)
	if err != nil {
		return webhook.Input{}, err
	}
	in := webhook.Input{
		Key: key, ProjectKey: kernel.Clone(body.ProjectKey), Description: httpkit.Deref(body.Description),
		Enabled: body.Enabled, Debug: httpkit.Deref(body.Debug),
		Domain: model.WebhookDomain(body.EventFilter.Domain), EventTypes: kernel.Clone(body.EventFilter.EventTypes), Criteria: criteria,
	}
	for _, handler := range body.Handlers {
		h, err := handlerUpdate(handler)
		if err != nil {
			return webhook.Input{}, err
		}
		in.Handlers = append(in.Handlers, h)
	}
	return in, nil
}

func subscriptionError(ctx context.Context, err error) error {
	var response *apierror.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &response):
		return response
	case errors.Is(err, kernel.ErrInvalid), errors.Is(err, kernel.ErrConflict):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, kernel.ErrNotFound):
		return apierror.New(http.StatusNotFound, "Webhook subscription not found")
	default:
		httpkit.NoteCause(ctx, err)
		return apierror.New(http.StatusInternalServerError, "internal error")
	}
}

// GetWebhookSubscriptions returns the complete authorized collection, including an empty array when none match.
func (s SubscriptionsAPI) GetWebhookSubscriptions(ctx context.Context, _ eventgen.GetWebhookSubscriptionsRequestObject) (eventgen.GetWebhookSubscriptionsResponseObject, error) {
	if s.Subscriptions.Service.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	list, err := s.Subscriptions.List(ctx, port.WebhookFilter{})
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	out := make(eventgen.GetWebhookSubscriptions200JSONResponse, 0, len(list))
	for _, view := range list {
		converted, err := subscriptionRead(view)
		if err != nil {
			httpkit.NoteCause(ctx, err)
			return nil, apierror.New(http.StatusInternalServerError, "internal error")
		}
		out = append(out, converted)
	}
	return out, nil
}

// CreateWebhookSubscription returns 201 with the redacted configuration after its transaction commits.
func (s SubscriptionsAPI) CreateWebhookSubscription(
	ctx context.Context,
	req eventgen.CreateWebhookSubscriptionRequestObject,
) (eventgen.CreateWebhookSubscriptionResponseObject, error) {
	if s.Subscriptions.Service.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	in, err := createInput(req.Body)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	view, err := s.Subscriptions.Create(ctx, in)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	body, err := subscriptionRead(view)
	if err != nil {
		httpkit.NoteCause(ctx, err)
		return nil, apierror.New(http.StatusInternalServerError, "internal error")
	}
	return eventgen.CreateWebhookSubscription201JSONResponse(body), nil
}

// GetWebhookSubscription returns 200 with the safe projection of an authorized subscription.
func (s SubscriptionsAPI) GetWebhookSubscription(ctx context.Context, req eventgen.GetWebhookSubscriptionRequestObject) (eventgen.GetWebhookSubscriptionResponseObject, error) {
	if s.Subscriptions.Service.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	view, err := s.Subscriptions.Get(ctx, req.Key)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	body, err := subscriptionRead(view)
	if err != nil {
		httpkit.NoteCause(ctx, err)
		return nil, apierror.New(http.StatusInternalServerError, "internal error")
	}
	return eventgen.GetWebhookSubscription200JSONResponse(body), nil
}

// UpdateWebhookSubscription returns 204 after an authorized replacement and presence-aware secret merge.
func (s SubscriptionsAPI) UpdateWebhookSubscription(
	ctx context.Context,
	req eventgen.UpdateWebhookSubscriptionRequestObject,
) (eventgen.UpdateWebhookSubscriptionResponseObject, error) {
	if s.Subscriptions.Service.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	in, err := updateInput(req.Key, req.Body)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	if err := s.Subscriptions.Update(ctx, req.Key, in); err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return eventgen.UpdateWebhookSubscription204Response{}, nil
}

// DeleteWebhookSubscription returns 204 after deleting the authorized subscription and its owned delivery rows.
func (s SubscriptionsAPI) DeleteWebhookSubscription(
	ctx context.Context,
	req eventgen.DeleteWebhookSubscriptionRequestObject,
) (eventgen.DeleteWebhookSubscriptionResponseObject, error) {
	if s.Subscriptions.Service.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	if err := s.Subscriptions.Delete(ctx, req.Key); err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return eventgen.DeleteWebhookSubscription204Response{}, nil
}
