package agentplugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// KindMarketplace is the work kind that rebuilds a repository's marketplace
// files, keyed by the repository. The persistent work queue stores it with
// each item, so it never changes.
const KindMarketplace = "agentplugins.marketplace"

// derives reports whether c keeps marketplace files of its own: a local
// repository's tree does, and nothing else, as only local Agent Plugins
// repositories exist.
func derives(c repo.Config, cached bool) bool { return !cached && c.Class == repo.Local }

// queue asks for the marketplace work of c. Only a local repository holds
// Agent Plugins, so the hooks that call it need not check c's class. The
// queue's own errors name the work, so they are returned as they are.
func queue(ctx context.Context, env format.Env, c repo.Config) error {
	return env.Enqueue(ctx, KindMarketplace, c.Key, nil) //nolint:wrapcheck
}

// WorkKinds names the format's one work kind, KindMarketplace.
func (Handler) WorkKinds() []string { return []string{KindMarketplace} }

// Work rebuilds the marketplace files of one repository from its package
// index and stores each at the repository root, as 7.x does after an
// upload ("rebuilds marketplace JSON at the repository root", the Agent
// Plugins Repositories docs). 7.x builds them after the upload has
// answered, which JFrog CLI's own Agent Plugins tests wait for, so this
// runs from the persistent work queue too (decision 0033). Every file is
// kept, so each answers whether or not a plugin is listed in it [I]. A
// file whose content has not changed is left as it is, so its dates and
// checksums change only with what it lists. A folder at a file's name is
// left in place, and that file is not stored. A removed repository no
// longer owes this work.
func (Handler) Work(ctx context.Context, env format.Env, it work.WorkItem) error {
	if it.Kind != KindMarketplace {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, it.Key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("agentplugins: repository %s: %w", it.Key, err)
	case !Serves(c.PackageType) || !derives(c, false):
		return nil
	}
	files, err := marketplaces(ctx, env.Store, c.Key)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := store(ctx, env, c, name, files[name]); err != nil {
			return err
		}
	}
	return nil
}

// store writes data as the marketplace file name of c, unless the file
// already holds it or a folder stands at its name.
func store(ctx context.Context, env format.Env, c repo.Config, name string, data []byte) error {
	sum := sha256.Sum256(data)
	current, err := env.Store.Items().Get(ctx, c.Key, false, name)
	switch {
	case err == nil && current.Folder:
		// Deriving over a folder would fail every run until the queue
		// parked the work, and the other files with it.
		return nil
	case err == nil && strings.EqualFold(current.Actual.SHA256, hex.EncodeToString(sum[:])):
		return nil
	case err != nil && !errors.Is(err, kernel.ErrNotFound):
		return fmt.Errorf("agentplugins: read %s: %w", name, err)
	}
	if _, err := env.Derive(ctx, format.Derived{
		Repo: c, Path: name, Body: bytes.NewReader(data), By: "_system_",
	}); err != nil {
		return fmt.Errorf("agentplugins: store %s: %w", name, err)
	}
	return nil
}

// Owed names the marketplace work that a download of a marketplace file of
// a local repository must see done first, so a client never reads a file
// that lags a completed write; other downloads owe nothing.
func (Handler) Owed(c repo.Config, cached bool, path string) []format.WorkRef {
	if derives(c, cached) && IsMarketplace(path) {
		return []format.WorkRef{{Kind: KindMarketplace, Key: c.Key}}
	}
	return nil
}

// Resolve leaves every path as it is. When a download asks for a
// marketplace file of a local repository that holds neither the file nor
// queued marketplace work, such as a repository no plugin has reached yet,
// it queues the work, which Owed then makes the download wait for, so the
// file is stored and served. A store or work queue fault fails the
// download.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	if !derives(c, d.Cached) || !IsMarketplace(d.Path) {
		return d.Path, nil
	}
	_, err := env.Store.Items().Get(ctx, c.Key, false, d.Path)
	if err == nil {
		return d.Path, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return "", fmt.Errorf("agentplugins: marketplace: %w", err)
	}
	_, err = env.WorkQueue.Get(ctx, KindMarketplace, c.Key)
	if err == nil {
		return d.Path, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return "", fmt.Errorf("agentplugins: marketplace work: %w", err)
	}
	return d.Path, queue(ctx, env, c)
}

// Classify classes the marketplace files as derived, so replication skips
// them and each repository builds its own, and every other path as unique
// content.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if IsMarketplace(path) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

var (
	_ format.Worker     = Handler{}
	_ format.Resolver   = Handler{}
	_ format.Classifier = Handler{}
)
