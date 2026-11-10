package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	appsql "github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/sqlite"
	"github.com/weaveplatform/artifactweave/migration/app/config"
	artifactshttp "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/terraformstate"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/usage"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/azure"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/gcs"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/s3"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/migration/platform/cdn"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// errBlobStore reports a blob store that could not be built from a
// configuration that passed validation, for example unreadable credentials.
var errBlobStore = errors.New("app: blob store")

// blobTier is one blob configuration section, "blob" or "cold_blob". Upload
// session rows are keyed "<backend>:<section>" or
// "<backend>:<section>.<store>", so the two sections never share a row even
// when they declare stores of the same name.
type blobTier struct {
	section string
	cfg     config.Blob
	// root is the fs directory of the section's own backend, and storesDir
	// holds a directory per named fs store without a root: two members
	// writing one directory would each count the other's copies as theirs.
	root      string
	storesDir string
	blobs     store.BlobIndex
	replicas  store.ReplicaIndex
	// lease names the mirror's repair lease. The hot and cold mirrors share
	// a lease store, so they need different names.
	lease string
}

// tiers returns the hot and cold blob tiers of cfg over st.
func tiers(cfg config.Config, st store.Store) (hot, cold blobTier) {
	coldRoot := cfg.ColdBlob.FS.Root
	if coldRoot == "" {
		coldRoot = filepath.Join(cfg.DataDir, "cold-blobs")
	}
	hot = blobTier{
		section: "blob", cfg: cfg.Blob,
		root: cfg.BlobRoot(), storesDir: filepath.Join(cfg.DataDir, "blob-stores"),
		blobs: st.Blobs(), replicas: st.Replicas(), lease: "blob-mirror-repair",
	}
	cold = blobTier{
		section: "cold_blob", cfg: cfg.ColdBlob,
		root: coldRoot, storesDir: filepath.Join(cfg.DataDir, "cold-blob-stores"),
		blobs: st.ColdBlobs(), replicas: st.ColdReplicas(),
		lease: "cold-blob-mirror-repair",
	}
	return hot, cold
}

// openedTier is a built tier: its store (nil when the section configures
// none), and the mirror when it is one, whose repair loop and stragglers the
// runtime manages.
type openedTier struct {
	store  blob.Store
	mirror *mirror.Store
}

// open builds the tier's blob store.
func (t blobTier) open(ctx context.Context, st store.Store, log *slog.Logger) (openedTier, error) {
	switch t.cfg.Backend {
	case "":
		return openedTier{}, nil
	case "mirror":
		// Not returned with err: a nil *mirror.Store in the blob.Store
		// field would compare non-nil.
		m, err := t.openMirror(ctx, st, log)
		if err != nil {
			return openedTier{}, err
		}
		return openedTier{store: m, mirror: m}, nil
	}
	sessions := session.InUploads(st.Uploads(), t.cfg.Backend+":"+t.section)
	s, err := openBackend(ctx, t.section, t.cfg.BlobBackend, sessions, t.root)
	return openedTier{store: s}, err
}

func (t blobTier) openMirror(
	ctx context.Context,
	st store.Store,
	log *slog.Logger,
) (*mirror.Store, error) {
	mc := t.cfg.Mirror
	members := make([]mirror.Member, 0, len(mc.Members))
	for _, name := range mc.Members {
		sc, ok := t.cfg.Stores[name]
		if !ok {
			return nil, fmt.Errorf(
				"%w: %s.mirror.members names %q, which %s.stores does not declare",
				errBlobStore,
				t.section,
				name,
				t.section,
			)
		}
		label := t.section + "." + name
		sessions := session.InUploads(st.Uploads(), sc.Backend+":"+label)
		s, err := openBackend(
			ctx, t.section+".stores."+name, sc.BlobBackend, sessions, t.memberRoot(name, sc),
		)
		if err != nil {
			return nil, err
		}
		members = append(members, mirror.Member{
			Name: name, Zone: sc.Zone, Store: s, WriteEnabled: sc.IsWritable(), Kind: sc.Backend,
		})
	}
	redundancy, quorum := mc.Effective()
	m, err := mirror.New(mirror.Options{
		Members: members, Redundancy: redundancy, WriteQuorum: quorum,
		Zone: mc.Zone, MemberTimeout: mc.MemberTimeout,
		Sessions: session.InUploads(st.Uploads(), "mirror:"+t.section),
		Replicas: t.replicas, Blobs: t.blobs, Leases: st.Leases(),
		RepairLease: t.lease, OnRepair: logRepair(log, t.section),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errBlobStore, t.section, err)
	}
	return m, nil
}

// openBackend builds one fs, s3, gcs or azure store. key is the
// configuration key it came from, for errors. Building makes no request:
// a wrong bucket or key shows on first use, not at startup.
func openBackend(
	ctx context.Context,
	key string,
	b config.BlobBackend,
	sessions session.Store,
	root string,
) (blob.Store, error) {
	s, err := buildBackend(ctx, b, sessions, root)
	if err != nil {
		return nil, fmt.Errorf("%w: %s (%s): %w", errBlobStore, key, b.Backend, err)
	}
	return s, nil
}

func buildBackend(
	ctx context.Context,
	b config.BlobBackend,
	sessions session.Store,
	root string,
) (blob.Store, error) {
	switch b.Backend {
	case "fs":
		return fs.New(root) //nolint:wrapcheck // wrapped with the key by openBackend
	case "s3":
		client, presign, err := s3.NewClient(ctx, s3.ClientConfig{
			Endpoint: b.S3.Endpoint, Region: b.S3.Region, PathStyle: b.S3.PathStyle,
			Profile: b.S3.Profile,
		})
		if err != nil {
			return nil, err //nolint:wrapcheck // wrapped with the key by openBackend
		}
		cdnSigner, err := cloudFront(b.S3.CDN.CloudFront)
		if err != nil {
			return nil, err
		}
		return s3.New(client, s3.Options{ //nolint:wrapcheck // as above
			Bucket: b.S3.Bucket, Prefix: b.S3.Prefix, Sessions: sessions, Presign: presign,
			CDN: cdnSigner,
		})
	case "gcs":
		cc := gcs.ClientConfig{
			Endpoint: b.GCS.Endpoint, CredentialsFile: b.GCS.CredentialsFile,
			GoogleAccessID: b.GCS.GoogleAccessID,
		}
		api, err := gcs.NewAPI(ctx, b.GCS.Bucket, cc)
		if err != nil {
			return nil, err //nolint:wrapcheck // wrapped with the key by openBackend
		}
		cdnSigner, err := cloudCDN(b.GCS.CDN.CloudCDN)
		if err != nil {
			return nil, err
		}
		return gcs.New(api, gcs.Options{ //nolint:wrapcheck // as above
			Prefix: b.GCS.Prefix, PartSize: int64(b.GCS.PartSize), Sessions: sessions,
			Presign: cc.CanSign(), SignsOverNetwork: cc.SignsOverNetwork(), CDN: cdnSigner,
		})
	case "azure":
		cc, err := azureClient(b.Azure)
		if err != nil {
			return nil, err
		}
		api, err := azure.NewAPI(cc)
		if err != nil {
			return nil, err //nolint:wrapcheck // wrapped with the key by openBackend
		}
		fd, err := frontDoor(b.Azure.CDN.FrontDoor)
		if err != nil {
			return nil, err
		}
		return azure.New(api, azure.Options{ //nolint:wrapcheck // as above
			Prefix: b.Azure.Prefix, PartSize: int64(b.Azure.PartSize),
			CopyRangeSize: int64(b.Azure.CopyRangeSize), Concurrency: b.Azure.Concurrency,
			Sessions: sessions, Presign: cc.CanSign(), FrontDoor: fd,
		})
	default:
		return nil, fmt.Errorf("%w %q", config.ErrUnknownBlob, b.Backend)
	}
}

// cloudFront builds the CloudFront signer c configures, or none. The key
// file is read and parsed here, at start, so a wrong path or a key in the
// wrong form fails startup rather than the first download.
func cloudFront(c config.CloudFront) (cdn.Signer, error) {
	if !c.IsEnabled() {
		return nil, nil //nolint:nilnil // no CDN is a valid answer
	}
	key, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("cloudfront private_key_file: %w", err)
	}
	signer, err := cdn.NewCloudFront(cdn.CloudFrontConfig{
		Domain: c.Domain, KeyPairID: c.KeyPairID, PrivateKey: key, Hash: c.Hash,
		PathPrefix: c.PathPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("cloudfront: %w", err)
	}
	return signer, nil
}

// cdnMaxSize is the largest file the hot tier's CDN is sent: CloudFront's
// limit when the store, or any member of a mirror, has a distribution, and
// no limit otherwise. A mirror signs with the first member that holds a
// blob, which may be the CloudFront one, so one such member limits them all.
func cdnMaxSize(b config.Blob) int64 {
	stores := []config.BlobBackend{b.BlobBackend}
	if b.Backend == "mirror" {
		stores = make([]config.BlobBackend, 0, len(b.Mirror.Members))
		for _, name := range b.Mirror.Members {
			stores = append(stores, b.Stores[name].BlobBackend)
		}
	}
	for _, s := range stores {
		if s.Backend == "s3" && s.S3.CDN.CloudFront.IsEnabled() {
			return cdn.CloudFrontMaxSize
		}
	}
	return 0
}

// frontDoor builds the Front Door c configures, or none.
func frontDoor(c config.FrontDoor) (*cdn.FrontDoor, error) {
	if !c.IsEnabled() {
		return nil, nil //nolint:nilnil // no CDN is a valid answer
	}
	fd, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{Endpoint: c.Endpoint, PathPrefix: c.PathPrefix})
	if err != nil {
		return nil, fmt.Errorf("front_door: %w", err)
	}
	return fd, nil
}

// cloudCDN builds the Cloud CDN signer c configures, or none, reading and
// checking the key file at start as cloudFront does.
func cloudCDN(c config.CloudCDN) (cdn.Signer, error) {
	if !c.IsEnabled() {
		return nil, nil //nolint:nilnil // no CDN is a valid answer
	}
	key, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("cloud_cdn key_file: %w", err)
	}
	signer, err := cdn.NewCloudCDN(cdn.CloudCDNConfig{
		Domain: c.Domain, KeyName: c.KeyName, Key: key, PathPrefix: c.PathPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("cloud_cdn: %w", err)
	}
	return signer, nil
}

// azureClient passes only the secret the chosen credential uses: the SDK
// client prefers a connection string over a key over a SAS whenever several
// are set, which would silently override an explicit credential.
func azureClient(a config.BlobAzure) (azure.ClientConfig, error) {
	cc := azure.ClientConfig{ServiceURL: a.Endpoint, Container: a.Container, Account: a.Account}
	switch a.EffectiveCredential() {
	case config.AzureConnectionString:
		cc.ConnectionString = a.ConnectionString
	case config.AzureSharedKey:
		cc.AccountKey = a.AccountKey
	case config.AzureSAS:
		cc.SASToken = a.SASToken
	case config.AzureManagedIdentity:
		cc.ManagedIdentityClientID = a.ManagedIdentityClientID
	case config.AzureWorkloadIdentity:
		// Explicitly, rather than through DefaultAzureCredential's chain,
		// so a pod without the federated token fails at startup instead of
		// falling through to another identity.
		cred, err := azidentity.NewWorkloadIdentityCredential(nil)
		if err != nil {
			return cc, fmt.Errorf("workload identity: %w", err)
		}
		cc.Credential = cred
	}
	return cc, nil
}

// logRepair logs each pass of a mirror's repair loop: a pass that changed
// something at Info, a failed one at Warn, and idle ones at Debug.
func logRepair(log *slog.Logger, section string) func(mirror.RepairReport, error) {
	return func(r mirror.RepairReport, err error) {
		switch {
		case err != nil:
			log.Warn("blob mirror repair failed", "tier", section, "error", err)
		case r.Skipped:
			log.Debug("blob mirror repair skipped: another pass holds the lease", "tier", section)
		case r == (mirror.RepairReport{}):
			log.Debug("blob mirror repair: nothing to do", "tier", section)
		default:
			log.Info("blob mirror repair", "tier", section,
				"copied", r.Copied, "found", r.Found, "failed", r.Failed,
				"deleted", r.Deleted, "deletes_skipped", r.DeletesSkipped,
				"backfilled", r.Backfilled, "drained", r.Drained)
		}
	}
}

// logSigning says at start which download routes the hot blob store signs,
// and warns once for every repository whose redirect flags ask for a route
// it cannot sign, so a misconfigured store shows before downloads quietly
// stream (decision 0029). Repositories created later are not checked.
func logSigning(ctx context.Context, log *slog.Logger, st store.Store, blobs blob.Store) {
	signer, _ := blobs.(blob.Signer)
	signs := func(via blob.Via) bool { return signer != nil && signer.Signs(via) }
	log.InfoContext(ctx, "download redirect routes",
		"storage", signs(blob.ViaStorage), "cdn", signs(blob.ViaCDN))
	repos, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		log.WarnContext(ctx, "download redirect: listing repositories", "error", err)
		return
	}
	for _, r := range repos {
		download, cdn := r.DownloadRedirect.Bool(), r.CDNRedirect.Bool()
		switch {
		case download && !signs(blob.ViaStorage) && !signs(blob.ViaCDN):
			log.WarnContext(ctx, "download redirect: the blob store cannot sign URLs, so "+
				"downloads stream", "repo", r.Key)
		case cdn && !download && !signs(blob.ViaCDN):
			log.WarnContext(ctx, "download redirect: cdnRedirect is set but the blob store "+
				"has no CDN, so downloads stream", "repo", r.Key)
		}
	}
}

// ErrUnknownMigration reports a migrate action other than up, down or
// version.
var ErrUnknownMigration = errors.New("app: unknown migrate action")

// Keyring builds the keyring from master_key and, during a rotation,
// master_key_previous (crypt.ParseVersionedKey). It returns nil without
// error when no master key is configured, for the commands that run without
// one.
func Keyring(cfg config.Config) (*crypt.Keyring, error) {
	if cfg.MasterKey == "" {
		return nil, nil //nolint:nilnil // no key configured is a valid state for migrate
	}
	var keys []crypt.MasterKey
	for _, s := range []string{cfg.MasterKeyPrevious, cfg.MasterKey} {
		if s == "" {
			continue
		}
		mk, err := crypt.ParseVersionedKey(s)
		if err != nil {
			return nil, fmt.Errorf("master key: %w", err)
		}
		keys = append(keys, mk)
	}
	k, err := crypt.NewKeyring(keys...)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	return k, nil
}

// OpenStore opens the configured metadata store. The SQLite file's
// directory is created when missing, so a first start needs nothing but a
// data directory path.
func OpenStore(
	ctx context.Context,
	cfg config.Config,
	o appsql.Options,
) (*appsql.Store, error) {
	if cfg.Database.Driver == "postgres" {
		s, err := postgres.Open(ctx, cfg.Database.DSN, postgres.Options{Options: o})
		if err != nil {
			return nil, fmt.Errorf("open store: %w", err)
		}
		return s, nil
	}
	path := cfg.SQLitePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	s, err := sqlite.Open(ctx, path, sqlite.Options{Options: o})
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return s, nil
}

// Migrate runs `artifactweave migrate <action>` over every module's stream,
// or over module's stream alone when module is set. "up" applies every
// pending migration. "down" reverts the migrations newer than to, which
// needs a module unless to is 0, everything; a negative to reverts one
// step, the newest migration of module's stream or, with no module, of the
// last stream in the canonical order that has any applied. "version"
// reports each stream's applied and latest versions, one line per stream.
// up and down write one line of outcome to w. A module that no stream has
// fails with dialect.ErrUnknownModule.
func Migrate(
	ctx context.Context,
	cfg config.Config,
	action string,
	to int,
	module string,
	w io.Writer,
) error {
	if action != "up" && action != "down" && action != "version" {
		return fmt.Errorf("%w: %q", ErrUnknownMigration, action)
	}
	s, err := OpenStore(ctx, cfg, appsql.Options{SkipMigrate: true})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	db, d, mods := s.DB(), s.Dialect(), appsql.Modules()
	switch action {
	case "up":
		done, err := dialect.Migrate(ctx, db, d, mods, module)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		_, _ = fmt.Fprintf(w, "applied %d migration(s): %v\n", len(done), done)
	case "down":
		if to < 0 {
			if module, to, err = oneStep(ctx, s, mods, module); err != nil {
				return fmt.Errorf("migrate down: %w", err)
			}
		}
		done, err := dialect.Rollback(ctx, db, d, mods, module, to)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		_, _ = fmt.Fprintf(w, "reverted %d migration(s): %v\n", len(done), done)
	default:
		known := slices.ContainsFunc(mods, func(m dialect.Module) bool { return m.Name == module })
		if module != "" && !known {
			return fmt.Errorf("migrate version: %w %q", dialect.ErrUnknownModule, module)
		}
		streams, err := dialect.Version(ctx, db, d, mods)
		if err != nil {
			return fmt.Errorf("migrate version: %w", err)
		}
		for _, v := range streams {
			if module == "" || v.Module == module {
				_, _ = fmt.Fprintf(w, "schema version %d of %d (%s, %s)\n",
					v.Current, v.Latest, d.Name, v.Module)
			}
		}
	}
	return nil
}

// oneStep returns the stream and the target version that revert one step:
// the newest migration of module's stream or, with no module, of the last
// stream in the canonical order that has any applied. With nothing applied
// the target is 0, so the rollback reverts nothing; an unknown module is
// returned as it is, for Rollback to refuse.
func oneStep(
	ctx context.Context,
	s *appsql.Store,
	mods []dialect.Module,
	module string,
) (string, int, error) {
	streams, err := dialect.Version(ctx, s.DB(), s.Dialect(), mods)
	if err != nil {
		return "", 0, err //nolint:wrapcheck // the caller names the action
	}
	last := dialect.StreamVersion{Module: module}
	for _, v := range streams {
		if v.Module == module || module == "" && v.Current > 0 {
			last = v
		}
	}
	return last.Module, max(last.Current-1, 0), nil
}

// RotateKeys runs `artifactweave keys rotate`: it opens the store with the
// configured keyring (master_key and master_key_previous), re-seals every
// sealed value under the newest version, and every sealed Terraform state
// in the blob store too (decision 0041), and reports how many changed.
// Once it has run, master_key_previous can be removed.
func RotateKeys(ctx context.Context, cfg config.Config, w io.Writer) error {
	if err := cfg.RequireMasterKey(); err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	keys, err := Keyring(cfg)
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	s, err := OpenStore(ctx, cfg, appsql.Options{Keyring: keys})
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	defer func() { _ = s.Close() }()
	n, err := s.Reseal(ctx)
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	_, _ = fmt.Fprintf(w, "re-sealed %d value(s) under key version %d\n", n, keys.Active())
	states, err := resealStates(ctx, cfg, s, keys)
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	_, _ = fmt.Fprintf(w, "re-sealed %d Terraform state(s) under key version %d\n",
		states.Resealed, keys.Active())
	for _, p := range states.Unreadable {
		_, _ = fmt.Fprintf(
			w,
			"the Terraform state %s opens under no configured key; it was lost already\n",
			p,
		)
	}
	if cfg.MasterKeyPrevious != "" {
		_, _ = fmt.Fprintln(
			w,
			"every value now opens with master_key alone; remove master_key_previous",
		)
	}
	return nil
}

// resealStates re-seals the Terraform states that an older key version
// sealed. They are blobs, not sealed columns, so the store's Reseal does not
// reach them, and without this a removed master_key_previous would lose
// them. A configuration without a hot blob store holds no states.
func resealStates(
	ctx context.Context,
	cfg config.Config,
	st *appsql.Store,
	keys *crypt.Keyring,
) (terraformstate.ResealReport, error) {
	hot, _ := tiers(cfg, st)
	opened, err := hot.open(ctx, st, slog.New(slog.DiscardHandler))
	if err != nil || opened.store == nil {
		return terraformstate.ResealReport{}, err
	}
	svc := &terraformstate.Service{
		Items: &item.Service{
			Store:   st,
			Blobs:   opened.store,
			Formats: formatSettings(cfg, keys).For,
		},
		Keys: keys,
	}
	rep, err := svc.Reseal(ctx)
	if opened.mirror != nil {
		// A mirror's members may still be writing what Reseal stored.
		err = errors.Join(err, opened.mirror.WaitIdle(ctx))
	}
	return rep, err //nolint:wrapcheck // the services name themselves
}

// mirrorAdmin gives the storage endpoints the runtime's mirrors (#18): the
// optimize trigger and their status.
type mirrorAdmin struct {
	tiers []mirrorTier
}

type mirrorTier struct {
	name    string
	store   *mirror.Store
	trigger chan struct{}
}

// mirrorsAdmin returns the mirrors for RouterConfig, or a nil
// interface when no tier is a mirror (a nil *mirrorAdmin in the interface
// would not be nil).
func (rt *Runtime) mirrorsAdmin() artifactshttp.Mirrors {
	if rt.admin == nil {
		return nil
	}
	return rt.admin
}

// Optimize asks each mirror's repair loop for a pass now. The send never
// blocks: a pass already queued answers this request too.
func (a *mirrorAdmin) Optimize() {
	for _, t := range a.tiers {
		select {
		case t.trigger <- struct{}{}:
		default:
		}
	}
}

func (a *mirrorAdmin) Status(ctx context.Context) ([]artifactshttp.MirrorStatus, error) {
	out := make([]artifactshttp.MirrorStatus, 0, len(a.tiers))
	for _, t := range a.tiers {
		st, err := t.store.Status(ctx)
		if err != nil {
			return nil, err //nolint:wrapcheck // mirror errors name the package
		}
		out = append(out, artifactshttp.MirrorStatus{Tier: t.name, Status: st})
	}
	return out, nil
}

// location describes the tier's blob store for the storage summary. Its type
// is the name of the Artifactory binary provider template that stores blobs
// the way the backend does. The object stores are the -direct templates,
// which write to the bucket with no eventual upload in between, as these
// backends do; JFrog's S3 SaaS reports s3-storage-v3-direct. A mirror
// spreads copies over stores of any kind, which in Artifactory is a sharding
// chain written out in binarystore.xml, and 7.x reports such a chain as
// user-chain (research 15). An object store keeps no local cache, so it
// reports the directory Artifactory gives a chain without one, not its
// bucket.
//
// A mirror reports its first fs member's directory, and that directory's
// volume, as 6.20 reports the first file-system provider of a chain and
// measures it (BinaryServiceImpl.getBinariesDir, FileStoreStorageSummary);
// with no fs member it is a chain without one.
func (t blobTier) location() usage.Location {
	b := t.cfg
	switch b.Backend {
	case "fs":
		return usage.Location{Type: "file-system", Directory: t.root, Root: t.root}
	case "s3":
		return usage.Location{Type: "s3-storage-v3-direct", Directory: usage.NoFilesystem}
	case "gcs":
		return usage.Location{Type: "google-storage-v2-direct", Directory: usage.NoFilesystem}
	case "azure":
		return usage.Location{Type: "azure-blob-storage-v2-direct", Directory: usage.NoFilesystem}
	case "mirror":
		for _, name := range b.Mirror.Members {
			if sc, ok := b.Stores[name]; ok && sc.Backend == "fs" {
				dir := t.memberRoot(name, sc)
				return usage.Location{Type: "user-chain", Directory: dir, Root: dir}
			}
		}
		return usage.Location{Type: "user-chain", Directory: usage.NoFilesystem}
	}
	return usage.Location{}
}

// memberRoot is the directory the mirror member name writes when it is an
// fs store: its own root, or one under storesDir named for it. The other
// backends ignore it.
func (t blobTier) memberRoot(name string, sc config.BlobStore) string {
	if sc.FS.Root != "" {
		return sc.FS.Root
	}
	return filepath.Join(t.storesDir, name)
}
