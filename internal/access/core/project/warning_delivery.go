package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	stdmail "net/mail"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errObsoleteWarning = errors.New("project storage warning no longer applies")

// WarningSender submits a message outside metadata transactions, with a final
// short authorization check immediately before its body is sent.
type WarningSender interface {
	Send(context.Context, mail.Server, mail.Message, func(context.Context) error) error
}

// WarningStore can reject an invocation that already holds a transaction,
// preventing the delivery handler from carrying its caller's locks into SMTP.
type WarningStore interface {
	port.Store
	InTx(context.Context) bool
}

// WarningDelivery owns the project storage notification handler. The runtime
// registers Handle before starting its queue. All stores and Mail share one
// metadata transaction runner; Sender never receives a transaction context.
type WarningDelivery struct {
	Store  WarningStore
	Queue  work.WorkQueue
	Mail   configapi.Mail
	Sender WarningSender
	Now    func() time.Time
}

type warningSnapshot struct {
	server     configapi.MailServer
	recipients []string
}

// Handle refreshes current project lifetime, warning occurrence and authorized
// recipients on every attempt and again before DATA. It suppresses obsolete
// warnings; missing/disabled mail transport retries under the existing queue
// policy. SMTP acknowledgement loss can resend the same durable Message-ID.
func (d WarningDelivery) Handle(ctx context.Context, item work.WorkItem) error {
	if d.Store == nil || d.Queue == nil || d.Mail == nil || d.Sender == nil {
		return fmt.Errorf("project warning delivery unavailable: %w", kernel.ErrStore)
	}
	if d.Store.InTx(ctx) {
		return fmt.Errorf("project warning delivery requires a transaction-free context: %w", kernel.ErrInvalid)
	}
	var warning model.ProjectStorageWarning
	if json.Unmarshal(item.Payload, &warning) != nil || item.Kind != StorageWarningKind ||
		warning.ProjectKey == "" || warning.Incarnation == "" || warning.WarningKey == "" ||
		warning.Limit <= 0 || !StorageWarning(model.Project{QuotaEmail: true, StorageQuotaBytes: warning.Limit}, warning.Bytes) ||
		item.Key != storageWarningKey(warning) || item.Enqueued.IsZero() {
		return fmt.Errorf("invalid project storage warning: %w", work.ErrPermanent)
	}
	snapshot, err := d.snapshot(ctx, item, warning)
	if errors.Is(err, errObsoleteWarning) {
		return nil
	}
	if err != nil {
		return err
	}
	message := warningMessage(warning, item.Enqueued, snapshot)
	server := snapshot.server
	from := server.From
	if from == "" {
		// The primary XSD permits an absent sender. This is a local fallback,
		// not a claimed vendor-generated address or an administrator identity.
		from = "artifactweave@localhost"
	}
	err = d.Sender.Send(ctx, mail.Server{
		Host: server.Host, Port: server.Port, Username: server.Username, Password: server.Password,
		From: from, TLS: server.TLS, SSL: server.SSL,
	}, message, func(ctx context.Context) error {
		current, err := d.snapshot(ctx, item, warning)
		if err != nil {
			return err
		}
		if current.server != snapshot.server || !slices.Equal(current.recipients, snapshot.recipients) {
			return fmt.Errorf("project warning configuration or recipients changed: %w", kernel.ErrConflict)
		}
		return nil
	})
	if errors.Is(err, errObsoleteWarning) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deliver project storage warning: %w", err)
	}
	return nil
}

func (d WarningDelivery) snapshot(ctx context.Context, item work.WorkItem, warning model.ProjectStorageWarning) (warningSnapshot, error) {
	var snapshot warningSnapshot
	err := d.Store.Tx(ctx, func(ctx context.Context) error {
		ps := d.Store.Projects()
		if err := ps.Guard(ctx); err != nil {
			return fmt.Errorf("guard project warning: %w", err)
		}
		authority, err := ps.Authority(ctx, warning.ProjectKey)
		if errors.Is(err, kernel.ErrNotFound) {
			return errObsoleteWarning
		}
		if err != nil {
			return fmt.Errorf("read project warning lifetime: %w", err)
		}
		if authority.Incarnation != warning.Incarnation {
			return errObsoleteWarning
		}
		project, err := ps.Get(ctx, warning.ProjectKey)
		if err != nil {
			return fmt.Errorf("read project warning policy: %w", err)
		}
		state, initialized, err := ps.Storage(ctx, warning.ProjectKey)
		if err != nil {
			return fmt.Errorf("read project warning occurrence: %w", err)
		}
		if !initialized || state.WarningKey != warning.WarningKey || !StorageWarning(project, state.Bytes) {
			return errObsoleteWarning
		}
		snapshot.recipients, err = d.recipients(ctx, warning.ProjectKey)
		if err != nil {
			return err
		}
		if len(snapshot.recipients) == 0 {
			return errObsoleteWarning
		}
		snapshot.server, err = d.Mail.Server(ctx)
		if err != nil {
			return fmt.Errorf("read project warning mail transport: %w", err)
		}
		if !snapshot.server.Enabled {
			return fmt.Errorf("project warning mail transport disabled: %w", kernel.ErrNotFound)
		}
		// Take the queue lock last. A heartbeat or replacement may have
		// changed the expiry while domain/configuration locks were acquired.
		until, err := d.Queue.GuardLease(ctx, item.Kind, item.Key, item.Generation)
		if err != nil {
			return fmt.Errorf("guard project warning lease: %w", err)
		}
		now := time.Now
		if d.Now != nil {
			now = d.Now
		}
		if !until.After(now()) {
			return fmt.Errorf("project warning lease expired: %w", kernel.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return warningSnapshot{}, fmt.Errorf("snapshot project storage warning: %w", err)
	}
	return snapshot, nil
}

func (d WarningDelivery) recipients(ctx context.Context, key string) ([]string, error) {
	users := map[string]bool{}
	for _, typ := range []model.PrincipalType{model.PrincipalUser, model.PrincipalGroup} {
		members, err := d.Store.Projects().Members(ctx, key, typ)
		if err != nil {
			return nil, fmt.Errorf("read project warning members: %w", err)
		}
		for _, member := range members {
			admin := false
			for _, name := range member.Roles {
				role, err := ResolveRole(ctx, d.Store.Projects(), key, name)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("resolve project warning administrator: %w", err)
				}
				admin = admin || role.Type == model.RoleAdmin
			}
			if !admin {
				continue
			}
			if typ == model.PrincipalUser {
				users[member.Name] = true
				continue
			}
			group, err := d.Store.Groups().Get(ctx, member.Name)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read project warning administrator group: %w", err)
			}
			for _, name := range group.Members {
				users[name] = true
			}
		}
	}
	addresses := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(users)) {
		user, err := d.Store.Users().Get(ctx, name)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read project warning administrator: %w", err)
		}
		if user.Status != model.UserEnabled || strings.ContainsAny(user.Email, "\r\n\x00") {
			continue
		}
		address, err := stdmail.ParseAddress(user.Email)
		if err == nil {
			addresses[address.Address] = true
		}
	}
	return slices.Sorted(maps.Keys(addresses)), nil
}

func warningMessage(warning model.ProjectStorageWarning, date time.Time, snapshot warningSnapshot) mail.Message {
	id := sha256.Sum256([]byte(storageWarningKey(warning)))
	subject := strings.TrimSpace(snapshot.server.SubjectPrefix + " Project storage quota warning: " + warning.ProjectKey)
	body := fmt.Sprintf("Project %s reached the 75%% storage warning threshold.\n\nAt this transition, logical owned storage was %d bytes and the configured quota was %d bytes.\n",
		warning.ProjectKey, warning.Bytes, warning.Limit)
	if snapshot.server.ArtifactoryURL != "" {
		body += "\nArtifactory: " + snapshot.server.ArtifactoryURL + "\n"
	}
	return mail.Message{
		ID: hex.EncodeToString(id[:]) + "@artifactweave.local", Date: date,
		Subject: subject, Text: body, Recipients: slices.Clone(snapshot.recipients),
	}
}
