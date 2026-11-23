package quota_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/quota"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type onceHandler struct {
	once *sync.Once
	wg   *sync.WaitGroup
}

func (h onceHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h onceHandler) Handle(context.Context, slog.Record) error {
	h.once.Do(h.wg.Done)
	return nil
}
func (h onceHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h onceHandler) WithGroup(string) slog.Handler      { return h }

func logOnce(wg *sync.WaitGroup) *slog.Logger {
	return slog.New(onceHandler{once: &sync.Once{}, wg: wg})
}

type mailServer struct {
	server configapi.MailServer
	err    error
}

func (m *mailServer) Server(context.Context) (configapi.MailServer, error) { return m.server, m.err }

type sent struct {
	server  mail.Server
	message mail.Message
}

type sender struct {
	sent   []sent
	err    error
	before func()
}

func (s *sender) Send(ctx context.Context, server mail.Server, m mail.Message, beforeData func(context.Context) error) error {
	if s.before != nil {
		s.before()
	}
	if err := beforeData(ctx); err != nil {
		return err
	}
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, sent{server, m})
	return nil
}

type rig struct {
	db       *inmem.Store
	v        *volume
	identity *accessapitest.Identity
	mail     *mailServer
	sender   *sender
	delivery quota.Delivery
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{v: &volume{total: 1000, used: 900}, sender: &sender{}}
	r.db = inmem.NewWithCapacity(r.v)
	r.identity = &accessapitest.Identity{Users: map[string]accessapi.UserInfo{
		"root":     {Name: "root", Email: "Root <root@example.invalid>", Admin: true},
		"ops":      {Name: "ops", Email: "ops@example.invalid", Admin: true, Status: "enabled"},
		"dup":      {Name: "dup", Email: "ops@example.invalid", Admin: true},
		"off":      {Name: "off", Email: "off@example.invalid", Admin: true, Status: "disabled"},
		"bad":      {Name: "bad", Email: "not an address", Admin: true},
		"inject":   {Name: "inject", Email: "x@example.invalid\r\nBcc: y@example.invalid", Admin: true},
		"reader":   {Name: "reader", Email: "reader@example.invalid"},
		"noemail":  {Name: "noemail", Admin: true},
		"stranger": {Name: "stranger", Email: "stranger@example.invalid"},
	}}
	r.mail = &mailServer{server: configapi.MailServer{
		Enabled: true, Host: "smtp.example.invalid", Port: 25, Username: "u", Password: "mail-private-fixture",
		SubjectPrefix: "[AW]", ArtifactoryURL: "https://registry.example.invalid",
	}}
	r.delivery = quota.Delivery{
		Store: r.db, Configs: r.db.Configs(), Settings: r.db.Settings(), Queue: r.db.WorkQueue(),
		Guard: r.db.Projects().Guard, Identity: r.identity, Mail: r.mail, Sender: r.sender,
	}
	putPolicy(t, r.db, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	return r
}

// raise records the current measurement and leases what it queued.
func (r *rig) raise(t *testing.T) []occurrence {
	t.Helper()
	if err := check(t, r.db, r.db.StorageQuota()); err != nil {
		t.Fatal(err)
	}
	return queued(t, r.db)
}

func TestUnitDeliverySendsToCurrentAdministrators(t *testing.T) {
	r := newRig(t)
	o := r.raise(t)
	if len(o) != 1 {
		t.Fatal(o)
	}
	if err := r.delivery.Handle(t.Context(), o[0].item); err != nil {
		t.Fatal(err)
	}
	if len(r.sender.sent) != 1 {
		t.Fatal(r.sender.sent)
	}
	got := r.sender.sent[0]
	if !slices.Equal(got.message.Recipients, []string{"ops@example.invalid", "root@example.invalid"}) ||
		got.server.From != "artifactweave@localhost" || got.server.Password != "mail-private-fixture" ||
		got.message.Subject != "[AW] Storage quota warning" ||
		!strings.Contains(got.message.Text, "85% storage space reached. Your maximum limit is 95%. It is recommended to add disk space.") ||
		!strings.Contains(got.message.Text, "900 of 1000 bytes") || !strings.Contains(got.message.Text, "https://registry.example.invalid") ||
		!got.message.Date.Equal(o[0].item.Enqueued) || !strings.HasSuffix(got.message.ID, "@artifactweave.local") {
		t.Fatalf("%+v", got)
	}
	// A retry of the same occurrence is the same message.
	if err := r.delivery.Handle(t.Context(), o[0].item); err != nil || r.sender.sent[1].message.ID != got.message.ID {
		t.Fatal("retry identity", err)
	}
	r.v.set(1000, 990, nil)
	limit := r.raise(t)
	r.mail.server.From = "quota@example.invalid"
	if err := r.delivery.Handle(t.Context(), limit[0].item); err != nil {
		t.Fatal(err)
	}
	last := r.sender.sent[len(r.sender.sent)-1]
	if last.message.Subject != "[AW] Storage quota limit reached" || last.server.From != "quota@example.invalid" ||
		!strings.Contains(last.message.Text, "95% Storage Space Reached: Your maximum storage limit has been reached. "+
			"New artifact deployments are blocked until more disk space is added.") || last.message.ID == got.message.ID {
		t.Fatalf("%+v", last)
	}
}

func TestUnitDeliverySuppressesObsoleteWarnings(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *rig){
		"rearmed": func(t *testing.T, r *rig) {
			r.v.set(1000, 1, nil)
			r.raise(t)
		},
		"disabled": func(t *testing.T, r *rig) { putPolicy(t, r.db, `{"enabled":false}`) },
		"no administrators": func(_ *testing.T, r *rig) {
			for name, u := range r.identity.Users {
				u.Admin = false
				r.identity.Users[name] = u
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			o := r.raise(t)
			change(t, r)
			if err := r.delivery.Handle(t.Context(), o[0].item); err != nil || len(r.sender.sent) != 0 {
				t.Fatal("an obsolete warning was sent", err, r.sender.sent)
			}
		})
	}
}

func TestUnitDeliveryRetriesTransportFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*rig)
		want   error
	}{
		"mail disabled": {func(r *rig) { r.mail.server.Enabled = false }, kernel.ErrNotFound},
		"mail missing":  {func(r *rig) { r.mail.err = kernel.ErrNotFound }, kernel.ErrNotFound},
		"recipients":    {func(r *rig) { r.identity.Err = errFixture }, errFixture},
		"send":          {func(r *rig) { r.sender.err = mail.ErrDelivery }, mail.ErrDelivery},
		"lease expired": {func(r *rig) { r.delivery.Now = func() time.Time { return time.Now().Add(2 * time.Hour) } }, kernel.ErrConflict},
		"guard":         {func(r *rig) { r.delivery.Guard = func(context.Context) error { return errFixture } }, errFixture},
		"policy store":  {func(r *rig) { r.delivery.Configs = failingConfigs{r.db.Configs()} }, errFixture},
		"state store":   {func(r *rig) { r.delivery.Settings = failingSettings{SettingsStore: r.db.Settings(), get: true} }, errFixture},
		"changed before DATA": {func(r *rig) {
			r.sender.before = func() { r.mail.server.Host = "other.example.invalid" }
		}, kernel.ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			o := r.raise(t)
			tc.change(r)
			if err := r.delivery.Handle(t.Context(), o[0].item); !errors.Is(err, tc.want) || len(r.sender.sent) != 0 {
				t.Fatal(err, r.sender.sent)
			}
		})
	}
	// Obsolete by the time DATA would start: nothing is sent and no retry.
	r := newRig(t)
	o := r.raise(t)
	r.sender.before = func() { putPolicy(t, r.db, `{"enabled":false}`) }
	if err := r.delivery.Handle(t.Context(), o[0].item); err != nil || len(r.sender.sent) != 0 {
		t.Fatal(err)
	}
	// A replaced lease generation is not this run's to send.
	r = newRig(t)
	o = r.raise(t)
	o[0].item.Generation++
	if err := r.delivery.Handle(t.Context(), o[0].item); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal(err)
	}
}

func TestUnitDeliveryRefusesInvalidInvocations(t *testing.T) {
	r := newRig(t)
	o := r.raise(t)
	if err := (quota.Delivery{}).Handle(t.Context(), o[0].item); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	err := r.db.Tx(t.Context(), func(ctx context.Context) error { return r.delivery.Handle(ctx, o[0].item) })
	if !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("delivery joined a caller's transaction", err)
	}
	for name, change := range map[string]func(*work.WorkItem){
		"payload":  func(i *work.WorkItem) { i.Payload = []byte(`[]`) },
		"kind":     func(i *work.WorkItem) { i.Kind = "other" },
		"key":      func(i *work.WorkItem) { i.Key = "warning/other" },
		"level":    func(i *work.WorkItem) { i.Payload = []byte(`{"level":"below","key":"k","total":1}`); i.Key = "below/k" },
		"total":    func(i *work.WorkItem) { i.Payload = []byte(`{"level":"warning","key":"k"}`); i.Key = "warning/k" },
		"enqueued": func(i *work.WorkItem) { i.Enqueued = time.Time{} },
	} {
		item := o[0].item
		change(&item)
		if err := r.delivery.Handle(t.Context(), item); !errors.Is(err, work.ErrPermanent) {
			t.Fatal(name, err)
		}
	}
}
