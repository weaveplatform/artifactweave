package project

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	accessmem "github.com/weaveplatform/artifactweave/internal/access/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
	platformmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type warningMailFunc func(context.Context) (configapi.MailServer, error)

func (f warningMailFunc) Server(ctx context.Context) (configapi.MailServer, error) { return f(ctx) }

type warningSendFunc func(context.Context, mail.Server, mail.Message, func(context.Context) error) error

func (f warningSendFunc) Send(ctx context.Context, server mail.Server, message mail.Message, before func(context.Context) error) error {
	return f(ctx, server, message, before)
}

type warningFixture struct {
	delivery WarningDelivery
	store    *accessmem.Store
	db       *memtx.Store
	item     work.WorkItem
	warning  model.ProjectStorageWarning
	settings configapi.MailServer
	now      time.Time
	sent     []mail.Message
	before   func()
	result   error
}

func newWarningFixture(t *testing.T) *warningFixture {
	t.Helper()
	ctx := t.Context()
	f := &warningFixture{
		db:  memtx.New(),
		now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		settings: configapi.MailServer{
			Enabled:        true,
			Host:           "smtp.example.invalid",
			Port:           25,
			SubjectPrefix:  "[Fixture]",
			ArtifactoryURL: "https://registry.example.invalid/artifactory",
		},
	}
	f.store = accessmem.New(f.db)
	mustWarning(t, f.store.Projects().Create(ctx, model.Project{Key: "alpha", StorageQuotaBytes: 100, QuotaEmail: true}))
	for _, user := range []model.User{
		{Name: "alice", Status: model.UserEnabled, Email: "alice@example.invalid"},
		{Name: "bob", Status: model.UserEnabled, Email: "bob@example.invalid"},
		{Name: "disabled", Status: model.UserDisabled, Email: "disabled@example.invalid"},
		{Name: "invalid", Status: model.UserEnabled, Email: "bad\r\nBcc: injected@example.invalid"},
		{Name: "noemail", Status: model.UserEnabled},
		{Name: "reader", Status: model.UserEnabled, Email: "reader@example.invalid"},
	} {
		mustWarning(t, f.store.Users().Create(ctx, user))
	}
	mustWarning(t, f.store.Groups().Create(ctx, model.Group{Name: "admins", Members: []string{"alice", "bob", "disabled", "invalid", "noemail"}}))
	for _, member := range []model.ProjectMember{
		{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{"Project Admin"}},
		{ProjectKey: "alpha", Type: model.PrincipalGroup, Name: "admins", Roles: []string{"Project Admin"}},
		{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "reader", Roles: []string{"Viewer"}},
	} {
		mustWarning(t, f.store.Projects().PutMember(ctx, member))
	}
	authority, err := f.store.Projects().Authority(ctx, "alpha")
	mustWarning(t, err)
	f.warning = model.ProjectStorageWarning{ProjectKey: "alpha", Incarnation: authority.Incarnation, WarningKey: "occurrence-one", Bytes: 75, Limit: 100}
	mustWarning(t, f.store.Projects().SetStorage(ctx, "alpha", model.ProjectStorage{Bytes: 75, WarningKey: f.warning.WarningKey}))
	queue := platformmem.New(f.db).WorkQueue()
	mustWarning(t, EnqueueStorageWarning(ctx, queue, f.warning, f.now))
	item, took, err := queue.LeaseKey(ctx, StorageWarningKind, storageWarningKey(f.warning), work.WorkLease{Holder: "fixture", Now: f.now, Until: f.now.Add(time.Minute)})
	mustWarning(t, err)
	if !took {
		t.Fatal("warning was not leased")
	}
	f.item = item
	f.delivery = WarningDelivery{
		Store: f.store, Queue: queue, Now: func() time.Time { return f.now },
		Mail: warningMailFunc(func(ctx context.Context) (configapi.MailServer, error) {
			if !f.db.InTx(ctx) {
				t.Error("mail snapshot read outside guarded transaction")
			}
			return f.settings, nil
		}),
		Sender: warningSendFunc(func(ctx context.Context, server mail.Server, message mail.Message, before func(context.Context) error) error {
			if f.db.InTx(ctx) {
				t.Fatal("SMTP retained a metadata transaction")
			}
			if server.From != "artifactweave@localhost" {
				t.Error("optional sender fallback changed")
			}
			if f.before != nil {
				f.before()
			}
			if err := before(ctx); err != nil {
				return err
			}
			f.sent = append(f.sent, message)
			return f.result
		}),
	}
	return f
}

func mustWarning(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnitWarningDeliveryCurrentRecipientsAndIdentity(t *testing.T) {
	f := newWarningFixture(t)
	f.result = mail.ErrDelivery // the receiver may have accepted DATA before disconnecting
	if err := f.delivery.Handle(t.Context(), f.item); !errors.Is(err, mail.ErrDelivery) {
		t.Fatalf("delivery failure was not retryable: %v", err)
	}
	f.result = nil
	mustWarning(t, f.delivery.Handle(t.Context(), f.item))
	if len(f.sent) != 2 || f.sent[0].ID != f.sent[1].ID || !f.sent[0].Date.Equal(f.item.Enqueued) || f.sent[0].Text != f.sent[1].Text {
		t.Fatal("warning retry changed its durable identity or transition facts")
	}
	message := f.sent[1]
	if !slices.Equal(message.Recipients, []string{"alice@example.invalid", "bob@example.invalid"}) ||
		!strings.Contains(message.Text, "75 bytes") || !strings.Contains(message.Text, "100 bytes") ||
		!strings.Contains(message.Text, f.settings.ArtifactoryURL) || !strings.HasPrefix(message.Subject, "[Fixture]") {
		t.Fatal("warning did not retain its authorized recipients and transition content")
	}
	other := f.warning
	other.WarningKey = "occurrence-two"
	if warningMessage(other, f.item.Enqueued, warningSnapshot{}).ID == message.ID {
		t.Fatal("distinct occurrences shared a Message-ID")
	}
}

func TestUnitWarningDeliveryObsoleteSuppression(t *testing.T) {
	for _, change := range []string{"disabled", "below threshold", "rearmed", "deleted", "recreated", "no admins", "missing accounting"} {
		t.Run(change, func(t *testing.T) {
			f := newWarningFixture(t)
			ctx := t.Context()
			apply := func() {
				switch change {
				case "disabled":
					p, err := f.store.Projects().Get(ctx, "alpha")
					mustWarning(t, err)
					p.QuotaEmail = false
					mustWarning(t, f.store.Projects().Update(ctx, p))
				case "below threshold":
					mustWarning(t, f.store.Projects().SetStorage(ctx, "alpha", model.ProjectStorage{Bytes: 74, WarningKey: f.warning.WarningKey}))
				case "rearmed":
					mustWarning(t, f.store.Projects().SetStorage(ctx, "alpha", model.ProjectStorage{Bytes: 80, WarningKey: "occurrence-two"}))
				case "deleted", "recreated", "missing accounting":
					mustWarning(t, f.store.Projects().Delete(ctx, "alpha"))
					if change != "deleted" {
						mustWarning(t, f.store.Projects().Create(ctx, model.Project{Key: "alpha", StorageQuotaBytes: 100, QuotaEmail: true}))
						if change == "missing accounting" {
							authority, err := f.store.Projects().Authority(ctx, "alpha")
							mustWarning(t, err)
							f.warning.Incarnation = authority.Incarnation
							f.item.Key = storageWarningKey(f.warning)
							f.item.Payload, err = json.Marshal(f.warning)
							mustWarning(t, err)
						}
					}
				case "no admins":
					mustWarning(t, f.store.Projects().DeleteMember(ctx, "alpha", model.PrincipalUser, "alice"))
					mustWarning(t, f.store.Projects().DeleteMember(ctx, "alpha", model.PrincipalGroup, "admins"))
				}
			}
			// Refresh after the SMTP envelope too: an obsolete warning must
			// never reach DATA even if its initial snapshot was valid.
			if change == "missing accounting" {
				apply()
			} else {
				f.before = apply
			}
			mustWarning(t, f.delivery.Handle(ctx, f.item))
			if len(f.sent) != 0 {
				t.Fatal("obsolete warning sent a message")
			}
		})
	}
}

func TestUnitWarningDeliveryRefreshesConfigurationAndLease(t *testing.T) {
	for _, change := range []string{"group", "user", "password", "disabled transport", "expired lease", "replaced lease"} {
		t.Run(change, func(t *testing.T) {
			f := newWarningFixture(t)
			ctx := t.Context()
			want := kernel.ErrConflict
			f.before = func() {
				switch change {
				case "group":
					mustWarning(t, f.store.Groups().RemoveMembers(ctx, "admins", "bob"))
				case "user":
					user, err := f.store.Users().Get(ctx, "bob")
					mustWarning(t, err)
					user.Status = model.UserDisabled
					mustWarning(t, f.store.Users().Update(ctx, user))
				case "password":
					f.settings.Password = "rotated-fixture-password"
				case "disabled transport":
					f.settings.Enabled = false
				case "expired lease":
					f.now = f.now.Add(time.Minute)
				case "replaced lease":
					f.now = f.now.Add(2 * time.Minute)
					_, took, err := f.delivery.Queue.LeaseKey(ctx, f.item.Kind, f.item.Key, work.WorkLease{Holder: "next worker", Now: f.now, Until: f.now.Add(time.Minute)})
					mustWarning(t, err)
					if !took {
						t.Fatal("replacement did not take expired lease")
					}
				}
			}
			if change == "disabled transport" {
				want = kernel.ErrNotFound
			}
			if err := f.delivery.Handle(ctx, f.item); !errors.Is(err, want) || len(f.sent) != 0 {
				t.Fatalf("changed delivery was not refused before DATA: %v", err)
			}
			if change == "group" || change == "user" || change == "password" {
				f.before = nil
				mustWarning(t, f.delivery.Handle(ctx, f.item))
				if len(f.sent) != 1 {
					t.Fatal("current retry was not submitted")
				}
				if change != "password" && !slices.Equal(f.sent[0].Recipients, []string{"alice@example.invalid"}) {
					t.Fatal("retry retained revoked recipient")
				}
			}
		})
	}
}

func TestUnitWarningDeliveryInvalidWorkAndMissingTransport(t *testing.T) {
	for _, invalid := range []string{"JSON", "kind", "key", "date", "threshold"} {
		t.Run(invalid, func(t *testing.T) {
			f := newWarningFixture(t)
			switch invalid {
			case "JSON":
				f.item.Payload = []byte("{")
			case "kind":
				f.item.Kind = "other"
			case "key":
				f.item.Key += "/other"
			case "date":
				f.item.Enqueued = time.Time{}
			case "threshold":
				f.warning.Bytes = 74
				var err error
				f.item.Payload, err = json.Marshal(f.warning)
				mustWarning(t, err)
			}
			if err := f.delivery.Handle(t.Context(), f.item); !errors.Is(err, work.ErrPermanent) || len(f.sent) != 0 {
				t.Fatalf("invalid work did not park safely: %v", err)
			}
		})
	}
	f := newWarningFixture(t)
	f.delivery.Mail = warningMailFunc(func(context.Context) (configapi.MailServer, error) { return configapi.MailServer{}, kernel.ErrNotFound })
	if err := f.delivery.Handle(t.Context(), f.item); !errors.Is(err, kernel.ErrNotFound) || len(f.sent) != 0 {
		t.Fatalf("missing mail transport was not retryable: %v", err)
	}
	if err := (WarningDelivery{}).Handle(t.Context(), f.item); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("unbound delivery did not fail closed: %v", err)
	}
	err := f.store.Tx(t.Context(), func(ctx context.Context) error { return f.delivery.Handle(ctx, f.item) })
	if !errors.Is(err, kernel.ErrInvalid) || len(f.sent) != 0 {
		t.Fatalf("delivery accepted a transaction context: %v", err)
	}
}

type warningFaultStore struct {
	WarningStore
	projects port.ProjectStore
	users    port.UserStore
	groups   port.GroupStore
}

func (s warningFaultStore) Projects() port.ProjectStore { return s.projects }
func (s warningFaultStore) Users() port.UserStore       { return s.users }
func (s warningFaultStore) Groups() port.GroupStore     { return s.groups }

type warningFaultProjects struct {
	port.ProjectStore
	stage string
	fault error
}

func (p warningFaultProjects) Guard(ctx context.Context) error {
	if p.stage == "guard" {
		return p.fault
	}
	return p.ProjectStore.Guard(ctx)
}

func (p warningFaultProjects) Authority(ctx context.Context, key string) (model.ProjectAuthority, error) {
	if p.stage == "lifetime" {
		return model.ProjectAuthority{}, p.fault
	}
	return p.ProjectStore.Authority(ctx, key)
}

func (p warningFaultProjects) Get(ctx context.Context, key string) (model.Project, error) {
	if p.stage == "policy" {
		return model.Project{}, p.fault
	}
	return p.ProjectStore.Get(ctx, key)
}

func (p warningFaultProjects) Storage(ctx context.Context, key string) (model.ProjectStorage, bool, error) {
	if p.stage == "occurrence" {
		return model.ProjectStorage{}, false, p.fault
	}
	return p.ProjectStore.Storage(ctx, key)
}

func (p warningFaultProjects) Members(ctx context.Context, key string, typ model.PrincipalType) ([]model.ProjectMember, error) {
	if p.stage == "members" {
		return nil, p.fault
	}
	return p.ProjectStore.Members(ctx, key, typ)
}

func (p warningFaultProjects) Role(ctx context.Context, key, name string) (model.Role, error) {
	if p.stage == "role" {
		return model.Role{}, p.fault
	}
	if p.stage == "missing role" {
		return model.Role{}, kernel.ErrNotFound
	}
	return p.ProjectStore.Role(ctx, key, name)
}

type warningFaultUsers struct {
	port.UserStore
	fault error
}

func (u warningFaultUsers) Get(context.Context, string) (model.User, error) {
	return model.User{}, u.fault
}

type warningFaultGroups struct {
	port.GroupStore
	fault error
}

func (g warningFaultGroups) Get(context.Context, string) (model.Group, error) {
	return model.Group{}, g.fault
}

func TestUnitWarningDeliverySnapshotFailuresDoNotSendPartialRecipients(t *testing.T) {
	for _, stage := range []string{"guard", "lifetime", "policy", "occurrence", "members", "role", "user", "group"} {
		t.Run(stage, func(t *testing.T) {
			f := newWarningFixture(t)
			fault := errors.New("fixture snapshot failure")
			store := warningFaultStore{
				WarningStore: f.store, projects: warningFaultProjects{ProjectStore: f.store.Projects(), stage: stage, fault: fault},
				users: f.store.Users(), groups: f.store.Groups(),
			}
			if stage == "user" {
				store.users = warningFaultUsers{UserStore: store.users, fault: fault}
			}
			if stage == "group" {
				store.groups = warningFaultGroups{GroupStore: store.groups, fault: fault}
			}
			f.delivery.Store = store
			if err := f.delivery.Handle(t.Context(), f.item); !errors.Is(err, fault) || len(f.sent) != 0 {
				t.Fatalf("failed snapshot submitted partial recipients: %v", err)
			}
		})
	}
	for _, stage := range []string{"missing role", "missing user", "missing group"} {
		t.Run(stage, func(t *testing.T) {
			f := newWarningFixture(t)
			store := warningFaultStore{WarningStore: f.store, projects: f.store.Projects(), users: f.store.Users(), groups: f.store.Groups()}
			switch stage {
			case "missing role":
				// A legacy dangling custom role grants no notification access.
				mustWarning(t, f.store.Projects().DeleteMember(t.Context(), "alpha", model.PrincipalGroup, "admins"))
				mustWarning(
					t,
					f.store.Projects().PutMember(t.Context(), model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "alice", Roles: []string{"gone"}}),
				)
			case "missing user":
				store.users = warningFaultUsers{UserStore: store.users, fault: kernel.ErrNotFound}
			case "missing group":
				store.groups = warningFaultGroups{GroupStore: store.groups, fault: kernel.ErrNotFound}
			}
			f.delivery.Store = store
			mustWarning(t, f.delivery.Handle(t.Context(), f.item))
			if stage != "missing group" && len(f.sent) != 0 {
				t.Fatal("missing authority granted a recipient")
			}
			if stage == "missing group" && (len(f.sent) != 1 || !slices.Equal(f.sent[0].Recipients, []string{"alice@example.invalid"})) {
				t.Fatal("missing group changed valid direct membership")
			}
		})
	}
}
