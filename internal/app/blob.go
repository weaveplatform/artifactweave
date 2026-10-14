package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/azure"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/blob/gcs"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/blob/s3"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// errBlobStore reports a blob store that could not be built from a
// configuration that passed validation, for example unreadable credentials.
var errBlobStore = errors.New("blob store")

// blobTier is one blob configuration section, "blob" or "cold_blob". Upload
// session rows are keyed "<backend>:<section>" or
// "<backend>:<section>.<store>", so the two sections never share a row even
// when they declare stores of the same name.
type blobTier struct {
	section string
	cfg     config.BlobConfig
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
		blobs: st.Archive().ColdBlobs(), replicas: st.Archive().ColdReplicas(),
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
	s, err := openBackend(ctx, t.section, t.cfg.BlobBackendConfig, sessions, t.root)
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
		root := sc.FS.Root
		if root == "" {
			root = filepath.Join(t.storesDir, name)
		}
		label := t.section + "." + name
		sessions := session.InUploads(st.Uploads(), sc.Backend+":"+label)
		s, err := openBackend(ctx, t.section+".stores."+name, sc.BlobBackendConfig, sessions, root)
		if err != nil {
			return nil, err
		}
		members = append(members, mirror.Member{
			Name: name, Zone: sc.Zone, Store: s, WriteEnabled: sc.Writable(), Kind: sc.Backend,
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
	b config.BlobBackendConfig,
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
	b config.BlobBackendConfig,
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
		return gcs.New(api, gcs.Options{ //nolint:wrapcheck // as above
			Prefix: b.GCS.Prefix, PartSize: int64(b.GCS.PartSize), Sessions: sessions,
			Presign: cc.CanSign(),
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
func cloudFront(c config.CloudFrontConfig) (cdn.Signer, error) {
	if !c.Enabled() {
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

// frontDoor builds the Front Door c configures, or none.
func frontDoor(c config.FrontDoorConfig) (*cdn.FrontDoor, error) {
	if !c.Enabled() {
		return nil, nil //nolint:nilnil // no CDN is a valid answer
	}
	fd, err := cdn.NewFrontDoor(cdn.FrontDoorConfig{Endpoint: c.Endpoint, PathPrefix: c.PathPrefix})
	if err != nil {
		return nil, fmt.Errorf("front_door: %w", err)
	}
	return fd, nil
}

// azureClient passes only the secret the chosen credential uses: the SDK
// client prefers a connection string over a key over a SAS whenever several
// are set, which would silently override an explicit credential.
func azureClient(a config.BlobAzureConfig) (azure.ClientConfig, error) {
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
