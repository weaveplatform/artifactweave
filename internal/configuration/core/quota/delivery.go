package quota

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdmail "net/mail"
	"slices"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errObsolete = errors.New("storage quota warning no longer applies")

// Sender submits a message outside metadata transactions; beforeData is the
// final authorization check immediately before the body is sent.
type Sender interface {
	Send(ctx context.Context, server mail.Server, message mail.Message, beforeData func(context.Context) error) error
}

// Store is the unit of work the delivery reads its snapshot in. InTx lets
// Handle refuse a caller's transaction, which would carry its locks into SMTP.
type Store interface {
	tx.Runner
	InTx(context.Context) bool
}

// Delivery owns the WarningKind handler. Every reader joins Store's
// transaction; Sender never receives a transaction context.
type Delivery struct {
	Store    Store
	Configs  configdoc.ConfigStore
	Settings settings.SettingsStore
	Queue    work.WorkQueue
	// Guard is the shared configuration/accounting guard, taken first.
	Guard    func(context.Context) error
	Identity accessapi.Identity
	Mail     configapi.Mail
	Sender   Sender
	Now      func() time.Time
}

type snapshot struct {
	server     configapi.MailServer
	recipients []string
}

// Handle delivers one occurrence to the current enabled administrators. It
// rereads the policy, the occurrence and the recipients on every attempt and
// again before DATA, suppressing an occurrence that a disabled policy or a
// rearm has made obsolete. Missing or disabled mail transport fails the
// attempt, so the queue's retry and parking policy applies; acknowledgement
// loss resends the same durable Message-ID.
func (d Delivery) Handle(ctx context.Context, item work.WorkItem) error {
	if d.Store == nil || d.Configs == nil || d.Settings == nil || d.Queue == nil || d.Guard == nil ||
		d.Identity == nil || d.Mail == nil || d.Sender == nil {
		return fmt.Errorf("storage quota warning delivery unavailable: %w", kernel.ErrStore)
	}
	if d.Store.InTx(ctx) {
		return fmt.Errorf("storage quota warning delivery requires a transaction-free context: %w", kernel.ErrInvalid)
	}
	var w warning
	if json.Unmarshal(item.Payload, &w) != nil || item.Kind != WarningKind || w.Key == "" ||
		(w.Level != Warning.String() && w.Level != Limit.String()) || w.Total <= 0 || w.Used < 0 ||
		item.Key != w.queueKey() || item.Enqueued.IsZero() {
		return fmt.Errorf("invalid storage quota warning: %w", work.ErrPermanent)
	}
	snap, err := d.snapshot(ctx, item, w)
	if errors.Is(err, errObsolete) {
		return nil
	}
	if err != nil {
		return err
	}
	server := snap.server
	from := server.From
	if from == "" {
		// The XSD permits an absent sender; project quota mail uses the same
		// local fallback, not a claimed vendor address.
		from = "artifactweave@localhost"
	}
	err = d.Sender.Send(ctx, mail.Server{
		Host: server.Host, Port: server.Port, Username: server.Username, Password: server.Password,
		From: from, TLS: server.TLS, SSL: server.SSL,
	}, message(w, item.Enqueued, snap), func(ctx context.Context) error {
		current, err := d.snapshot(ctx, item, w)
		if err != nil {
			return err
		}
		if current.server != snap.server || !slices.Equal(current.recipients, snap.recipients) {
			return fmt.Errorf("storage quota warning configuration or recipients changed: %w", kernel.ErrConflict)
		}
		return nil
	})
	if errors.Is(err, errObsolete) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deliver storage quota warning: %w", err)
	}
	return nil
}

func (d Delivery) snapshot(ctx context.Context, item work.WorkItem, w warning) (snapshot, error) {
	var snap snapshot
	err := d.Store.Tx(ctx, func(ctx context.Context) error {
		if err := d.Guard(ctx); err != nil {
			return fmt.Errorf("guard storage quota warning: %w", err)
		}
		p, _, err := Read(ctx, d.Configs)
		if err != nil {
			return err
		}
		if !p.Enabled {
			return errObsolete
		}
		st, err := readState(ctx, d.Settings)
		if err != nil {
			return err
		}
		armed := st.Warning
		if w.Level == Limit.String() {
			armed = st.Limit
		}
		if armed != w.Key {
			return errObsolete
		}
		admins, err := d.Identity.Administrators(ctx)
		if err != nil {
			return fmt.Errorf("read storage quota warning recipients: %w", err)
		}
		snap.recipients = recipients(admins)
		if len(snap.recipients) == 0 {
			return errObsolete
		}
		snap.server, err = d.Mail.Server(ctx)
		if err != nil {
			return fmt.Errorf("read storage quota warning mail transport: %w", err)
		}
		if !snap.server.Enabled {
			return fmt.Errorf("storage quota warning mail transport disabled: %w", kernel.ErrNotFound)
		}
		// The queue lock is taken last: a heartbeat or replacement may have
		// moved the expiry while the other locks were acquired.
		until, err := d.Queue.GuardLease(ctx, item.Kind, item.Key, item.Generation)
		if err != nil {
			return fmt.Errorf("guard storage quota warning lease: %w", err)
		}
		now := time.Now
		if d.Now != nil {
			now = d.Now
		}
		if !until.After(now()) {
			return fmt.Errorf("storage quota warning lease expired: %w", kernel.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return snapshot{}, fmt.Errorf("snapshot storage quota warning: %w", err)
	}
	return snap, nil
}

// recipients are the enabled administrators' parseable addresses, sorted and
// deduplicated. 7.x shows its quota messages to administrators only.
func recipients(admins []accessapi.UserInfo) []string {
	var out []string
	for _, u := range admins {
		if !u.Admin || !u.Enabled() || strings.ContainsAny(u.Email, "\r\n\x00") {
			continue
		}
		if address, err := stdmail.ParseAddress(u.Email); err == nil {
			out = append(out, address.Address)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// limitMessage and warningMessage are 7.161.26's StorageQuotaInfo texts,
// which its UI shows administrators and its log records.
func limitMessage(p Policy) string {
	return fmt.Sprintf("%d%% Storage Space Reached: Your maximum storage limit has been reached. "+
		"New artifact deployments are blocked until more disk space is added.", p.LimitPercent)
}

func warningMessage(p Policy) string {
	return fmt.Sprintf("%d%% storage space reached. Your maximum limit is %d%%. It is recommended to add disk space.",
		p.Warning, p.LimitPercent)
}

func message(w warning, date time.Time, snap snapshot) mail.Message {
	p := Policy{Enabled: true, LimitPercent: w.LimitPercent, Warning: w.WarningPercent}
	subject, text := "Storage quota warning", warningMessage(p)
	if w.Level == Limit.String() {
		subject, text = "Storage quota limit reached", limitMessage(p)
	}
	id := sha256.Sum256([]byte(WarningKind + "/" + w.queueKey()))
	body := fmt.Sprintf("%s\n\nAt this transition, the blob storage volume had %d of %d bytes in use.\n",
		text, w.Used, w.Total)
	if snap.server.ArtifactoryURL != "" {
		body += "\nArtifactory: " + snap.server.ArtifactoryURL + "\n"
	}
	return mail.Message{
		ID: hex.EncodeToString(id[:]) + "@artifactweave.local", Date: date,
		Subject:    strings.TrimSpace(snap.server.SubjectPrefix + " " + subject),
		Text:       body,
		Recipients: slices.Clone(snap.recipients),
	}
}
