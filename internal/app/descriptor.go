package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/descriptor"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ApplyDescriptor runs `artifactweave config apply`: it applies the
// descriptor in file to the store the configuration names, as `migrate` and
// `keys rotate` work on the store directly, and writes what changed to w.
// With dryRun it checks the whole descriptor against the current state and
// writes nothing.
//
// It works on the store rather than calling a running server so that it
// needs no admin credentials in the CI job, only the configuration the
// server itself uses, and so that it can prepare a store before the first
// start. It runs the same code as PATCH /api/system/configuration.
func ApplyDescriptor(
	ctx context.Context,
	cfg config.Config,
	file string,
	dryRun bool,
	w io.Writer,
) error {
	// The master key seals the secrets a descriptor may carry (a remote
	// repository's or a proxy's password).
	if err := cfg.RequireMasterKey(); err != nil {
		return fmt.Errorf("config apply: %w", err)
	}
	p, err := readDescriptor(file)
	if err != nil {
		return fmt.Errorf("config apply: %w", err)
	}
	keys, err := Keyring(cfg)
	if err != nil {
		return fmt.Errorf("config apply: %w", err)
	}
	st, err := OpenStore(ctx, cfg, sqlcommon.Options{Keyring: keys})
	if err != nil {
		return fmt.Errorf("config apply: %w", err)
	}
	defer func() { _ = st.Close() }()
	res, err := descriptor.Apply(ctx, st, p, time.Now(), dryRun)
	if err != nil {
		return fmt.Errorf("config apply: %w", err)
	}
	_, _ = io.WriteString(w, res.String())
	return nil
}

func readDescriptor(file string) (*descriptor.Patch, error) {
	doc, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read descriptor: %w", err)
	}
	p, err := descriptor.Parse(doc, os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}

// bootstrapMarker is the SettingBootstrapDescriptor value: which file was
// applied, for an operator wondering why a later edit to it changed nothing.
type bootstrapMarker struct {
	File   string    `json:"file"`
	SHA256 string    `json:"sha256"`
	At     time.Time `json:"at"`
}

// applyBootstrap applies bootstrap.descriptor once, after the principals are
// seeded; the marker is written in the same transaction. Later starts leave
// the configuration to `config apply` and the API, so a stale bootstrap file
// cannot undo what was changed since. A descriptor that cannot be applied
// stops the start, because the server would otherwise run without the
// configuration the operator asked for.
func applyBootstrap(
	ctx context.Context,
	cfg config.Config,
	st store.Store,
	log *slog.Logger,
) error {
	file := cfg.Bootstrap.Descriptor
	if file == "" {
		return nil
	}
	var m bootstrapMarker
	switch err := st.Settings().Get(ctx, types.SettingBootstrapDescriptor, &m); {
	case err == nil:
		return nil
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("bootstrap descriptor: %w", err)
	}
	doc, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("bootstrap descriptor: %w", err)
	}
	p, err := descriptor.Parse(doc, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("bootstrap descriptor %s: %w", file, err)
	}
	sum := sha256.Sum256(doc)
	now := time.Now()
	var res descriptor.Result
	err = st.Tx(ctx, func(ctx context.Context) error {
		var err error
		if res, err = descriptor.Apply(ctx, st, p, now, false); err != nil {
			return err //nolint:wrapcheck // wrapped below
		}
		return st.Settings().
			Put(ctx, types.SettingBootstrapDescriptor, bootstrapMarker{ //nolint:wrapcheck // as above
				File: file, SHA256: hex.EncodeToString(sum[:]), At: now,
			})
	})
	if err != nil {
		return fmt.Errorf("bootstrap descriptor %s: %w", file, err)
	}
	log.Info("applied the bootstrap descriptor", "file", file,
		"created", len(res.Created), "updated", len(res.Updated), "deleted", len(res.Deleted))
	return nil
}
