package mavenhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TryMavenMetadataItem runs the 7.x virtual interceptor for every package type.
// Indexer internals and direct repositories retain the ordinary item lookup.
func TryMavenMetadataItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	_ bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	if c.Class != repo.Virtual || req.IsFolder || strings.HasPrefix(req.Path, ".index/") {
		return false
	}
	owner, sidecar := req.Path, ""
	for _, alg := range []string{"sha1", "md5", "sha256"} {
		if strings.HasSuffix(owner, "."+alg) {
			owner = strings.TrimSuffix(owner, "."+alg)
			sidecar = alg
			break
		}
	}
	if path.Base(owner) != "maven-metadata.xml" {
		return false
	}
	delivery := &metadataDelivery{}
	doc, err := mergeMetadata(k, r, st, c, owner, p, acc, delivery)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return true
	}
	body := doc.Bytes()
	hasher := blob.NewHasher()
	_, _ = hasher.Write(body)
	digest := hasher.Sum()
	result := source.Result{Revalidate: delivery.revalidate, Used: delivery.used, Stream: &source.Stream{
		Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
		Representation: source.Representation{
			Size: int64(len(body)), Modified: doc.Modified,
			Checksums: kernel.Checksums{SHA1: digest.SHA1, MD5: digest.MD5, SHA256: digest.SHA256, SHA512: digest.SHA512},
		},
	}}
	k.ServeRemoteStream(w, r, result, c, req, source.Resource{}, sidecar, p.Anonymous)
	return true
}

func mergeMetadata(
	k *itemkit.Kit,
	r *http.Request,
	st itemkit.Store,
	c repo.Config,
	owner string,
	p kernel.Principal,
	acc itemkit.Access,
	delivery *metadataDelivery,
) (maven.MetadataDocument, error) {
	if err := generic.CheckDownload(c, owner, false); err != nil {
		return maven.MetadataDocument{}, fmt.Errorf("maven metadata: %w", err)
	}
	items := k.Items(st).ReadSession()
	translate := itemkit.ReadTranslator(items, c)
	members, err := virtual.Members(
		r.Context(),
		st.Repos(),
		c,
		virtual.Options{Order: virtual.Package, Path: owner, Translate: translate, NoUpstream: remote.NoOriginUpstream(r.Context(), c)},
	)
	if err != nil {
		return maven.MetadataDocument{}, k.ItemError(r.Context(), "Maven metadata members", err)
	}
	var parts []virtual.Part[maven.MetadataDocument]
	var resolution virtual.Resolution
	priorityFound := false
	for _, member := range members {
		mc := member.Repo
		memberPath, err := translate(r.Context(), mc, owner)
		if err != nil {
			return maven.MetadataDocument{}, k.ItemError(r.Context(), "Maven metadata member path", err)
		}
		if priorityFound && !mc.PriorityResolution.Bool() {
			break
		}
		if maven.SnapshotMetadata(owner) && mc.HandleSnapshots != nil && !*mc.HandleSnapshots {
			continue
		}
		key := mc.Key
		if mc.Class == repo.Remote {
			key += "-cache"
		}
		if !acc.File(mc, memberPath, accessapi.ActionRead) || !acc.Folder(mc, memberPath, accessapi.ActionRead) {
			resolution.Deny(virtual.Denial{Member: key, Path: memberPath, User: p.Name})
			continue
		}
		if err := generic.CheckDownload(mc, memberPath, false); err != nil {
			continue
		}
		doc, err := readMetadataMember(k, r, st, items, member, memberPath, p, acc, delivery)
		if err != nil {
			var api *apierror.Error
			var upstream *source.HTTPError
			var lookup *metadataLookupFault
			switch {
			case errors.As(err, &lookup):
				// Lookup and authority failures are not absent metadata. Content
				// acquisition/read failures retain the interceptor's pass-over.
				return maven.MetadataDocument{}, k.InternalError(r.Context(), "Maven metadata storage", err)
			case errors.Is(err, source.ErrDenied):
				resolution.Deny(virtual.Denial{Member: key, Path: memberPath, User: p.Name})
			case errors.As(err, &api) && api.Status == http.StatusForbidden, errors.As(err, &upstream) && upstream.Status == http.StatusForbidden:
				resolution.Deny(virtual.Denial{Member: key, Path: memberPath, User: p.Name})
			case errors.Is(err, kernel.ErrNotFound):
			default:
				k.Log().WarnContext(r.Context(), fmt.Sprintf("Metadata retrieval failed on repo '%s': %s", mc.Key, err), "member", mc.Key, "type", mc.PackageType)
			}
			continue
		}
		parts = append(parts, virtual.Part[maven.MetadataDocument]{Member: mc, Doc: doc})
		priorityFound = priorityFound || mc.PriorityResolution.Bool()
	}
	if len(parts) == 0 {
		if resolution.Forbidden != nil {
			//nolint:wrapcheck // the virtual walk owns the exact HTTP answer
			return maven.MetadataDocument{}, virtual.Answer(c, owner, resolution, p.Anonymous, false)
		}
		return maven.MetadataDocument{}, apierror.New(http.StatusNotFound, fmt.Sprintf("Maven metadata not found for '%s'.; Path: '%s:%s'", owner, c.Key, owner))
	}
	v3 := k.FormatSettings.Maven.MetadataVersion3Enabled
	//nolint:wrapcheck // the pure merger names its own errors
	return (maven.Merger{SnapshotVersions: (v3 == nil || *v3) && maven.SnapshotMetadata(owner) && maven.SupportsSnapshotVersions(r.Header.Values("User-Agent"))}).Merge(
		r.Context(),
		parts,
	)
}

// Metadata retrieval has distinct lookup and content phases. 7.161.26's
// MavenMetadataInterceptor tolerates IO/StorageException while opening/reading
// content, but resource lookup precedes that catch. A content reader may itself
// return ErrStore, so its error must not acquire this marker merely by cause.
type metadataLookupFault struct{ error }

func (e *metadataLookupFault) Unwrap() error { return e.error }

func metadataLookupError(ctx context.Context, err error) error {
	var content *source.ContentOpenError
	if errors.As(err, &content) {
		return err
	}
	if ctx.Err() == nil && (errors.Is(err, kernel.ErrStore) || errors.Is(err, format.ErrStorageFault)) {
		return &metadataLookupFault{err}
	}
	return err
}

func readMetadataMember(
	k *itemkit.Kit,
	r *http.Request,
	st itemkit.Store,
	items *item.Service,
	member virtual.Member,
	owner string,
	p kernel.Principal,
	acc itemkit.Access,
	delivery *metadataDelivery,
) (maven.MetadataDocument, error) {
	c := member.Repo
	var result source.Result
	var body io.ReadCloser
	var modified time.Time
	if c.Class == repo.Remote && k.RemoteSources != nil {
		var err error
		result, err = k.RemoteSources.Read(r.Context(), source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey, CacheOnly: !member.Upstream,
			Resource: source.Resource{Kind: source.ResourceKindMetadata, Path: owner, Method: http.MethodGet},
		})
		if err != nil {
			return maven.MetadataDocument{}, fmt.Errorf("maven metadata: %w", metadataLookupError(r.Context(), err))
		}
		if result.Stream == nil {
			return maven.MetadataDocument{}, source.ErrInvalidResult
		}
		body, modified = result.Stream.Body, result.Stream.Modified
		defer body.Close()
		if result.Revalidate != nil {
			if err := result.Revalidate(r.Context()); err != nil {
				return maven.MetadataDocument{}, fmt.Errorf("maven metadata: %w", metadataLookupError(r.Context(), err))
			}
		}
	} else {
		resolved, err := items.Resolve(r.Context(), c, format.Download{Path: owner})
		if err != nil {
			return maven.MetadataDocument{}, fmt.Errorf("maven metadata: %w", metadataLookupError(r.Context(), err))
		}
		it, err := st.Items().Get(r.Context(), c.Key, c.Class == repo.Remote, resolved)
		if err != nil {
			return maven.MetadataDocument{}, fmt.Errorf("maven metadata: %w", metadataLookupError(r.Context(), err))
		}
		if it.Folder {
			return maven.MetadataDocument{}, kernel.ErrNotFound
		}
		body, _, err = k.Blobs.Open(r.Context(), it.Actual.SHA256)
		if err != nil {
			return maven.MetadataDocument{}, fmt.Errorf("maven metadata: %w", err)
		}
		defer body.Close()
		modified = it.Modified
	}
	data, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil {
		return maven.MetadataDocument{}, fmt.Errorf("read maven metadata: %w", err)
	}
	doc, err := maven.ParseMetadata(data, modified)
	if err == nil && result.Stream != nil {
		delivery.reads = append(delivery.reads, result)
	}
	if err != nil {
		return maven.MetadataDocument{}, fmt.Errorf("maven member metadata: %w", err)
	}
	return doc, nil
}

// Member source authority is checked again immediately before delivery; only a
// complete answer records use, through the item kit's normal stream serving.
type metadataDelivery struct{ reads []source.Result }

func (d *metadataDelivery) revalidate(ctx context.Context) error {
	for _, read := range d.reads {
		if read.Revalidate != nil {
			if err := read.Revalidate(ctx); err != nil {
				return fmt.Errorf("maven metadata authority: %w", err)
			}
		}
	}
	return nil
}

func (d *metadataDelivery) used(ctx context.Context) error {
	for _, read := range d.reads {
		if read.Used != nil {
			if err := read.Used(ctx); err != nil {
				return fmt.Errorf("maven metadata use: %w", err)
			}
		}
	}
	return nil
}
