package rpmhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

const rpmMetadataLimit = 36 << 20

var (
	errRPMVirtualKey       = errors.New("server: rpm: virtual signing key must be gpg")
	errRPMVirtualCycle     = errors.New("server: rpm: virtual repository cycle")
	errRPMMemberType       = errors.New("server: rpm: incompatible virtual member")
	errRPMMetadataTooLarge = errors.New("server: rpm: metadata too large")
)

type rpmCandidate struct {
	repo repo.Config
	pkg  rpm.Package
}

// RPMAPI is the RPM ecosystem's partial strict server: the YUM metadata
// operation, and the item overlay that serves the RPM virtual repositories'
// metadata and packages, over the item kit.
type RPMAPI struct{ *itemkit.Kit }

// TryRPMItem is the overlay (itemkit.Overlay) that serves a virtual RPM
// repository's repodata and packages: a path that contains "repodata/" or
// ends ".rpm", neither cached nor a folder. It reports whether it
// answered; every other request it leaves to the overlays after it and the
// generic lookup.
func TryRPMItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := RPMAPI{k}
	if c.PackageType != repo.RPM || c.Class != repo.Virtual || cached || req.IsFolder {
		return false
	}
	if strings.Contains(req.Path, "repodata/") {
		s.serveVirtualRPMMetadata(w, r, st, c, req, p, acc)
		return true
	}
	if strings.HasSuffix(req.Path, ".rpm") {
		s.serveVirtualRPMPackage(w, r, st, c, req, p, acc)
		return true
	}
	return false
}

func (s RPMAPI) serveVirtualRPMMetadata(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	root, name, ok := strings.Cut(req.Path, "repodata/")
	if !ok || name == "" || strings.Contains(name, "/") {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// 7.x stores a virtual repository's repodata in the virtual repository
	// (RTFACT-14054, RTFACT-30422), so its own patterns rejecting the file
	// leave no repository to serve it from, the 404 a file gets (6.20
	// VirtualRepoDownloadStrategy), once read permission is settled.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "RPM virtual metadata download", err))
		return
	}
	// 7.x serves the stored repodata through its virtual download path, as
	// any other file (#1350): a query, such as skipUpdateStats
	// (RTFACT-25636, 7.21.3), changes nothing, and matrix parameters "make
	// resolution conditional on properties" (JFrog's guide, Using
	// Properties in Deployment and Resolution). The composed repodata
	// carries none [I], so only a mandatory key refuses it.
	if !req.Matrix().Matches(nil) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	candidates, err := s.rpmCandidates(r.Context(), st, c, root, p, acc, false, map[string]bool{})
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "RPM virtual members", err))
		return
	}
	packages := make([]rpm.Package, 0, len(candidates))
	for _, candidate := range candidates {
		packages = append(packages, candidate.pkg)
	}
	meta, err := rpm.EncodeMetadata(packages, root, time.Unix(0, 0))
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "RPM virtual metadata", err))
		return
	}
	if err := signVirtualRPM(r.Context(), s.Access.Keys, c, meta.Files); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "RPM virtual signature", err))
		return
	}
	body, ok := meta.Files["repodata/"+name]
	if !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, name, time.Time{}, bytes.NewReader(body))
	_ = sw.Complete(r)
}

func signVirtualRPM(
	ctx context.Context,
	keys accessapi.Keys,
	c repo.Config,
	files map[string][]byte,
) error {
	if c.KeyPair == "" {
		return nil
	}
	pair, err := keys.Keypair(ctx, c.KeyPair)
	if err != nil {
		return fmt.Errorf("rpm: virtual signing key: %w", err)
	}
	if pair.Type != accessapi.KeypairGPG {
		return errRPMVirtualKey
	}
	sig, err := rpm.SignRepomd(files["repodata/repomd.xml"], pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return fmt.Errorf("rpm: sign virtual repomd: %w", err)
	}
	files["repodata/repomd.xml.asc"] = sig
	files["repodata/repomd.xml.key"] = []byte(pair.PublicKey)
	return nil
}

func (s RPMAPI) serveVirtualRPMPackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "RPM virtual package download", err))
		return
	}
	root := ""
	if c.YumRootDepth != nil && *c.YumRootDepth > 0 {
		parts := strings.Split(req.Path, "/")
		if len(parts) <= *c.YumRootDepth {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
		root = strings.Join(parts[:*c.YumRootDepth], "/") + "/"
	}
	candidates, err := s.rpmCandidates(r.Context(), st, c, root, p, acc, true, map[string]bool{})
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "RPM virtual members", err))
		return
	}
	// A query changes nothing, and a member's file that the matrix
	// parameters do not match does not serve the GET (#1350): the next
	// member that lists the package is asked, as 7.x's virtual download
	// passes over a member that does not find the file
	// (ArtifactoryVirtualDownloadStrategy.processStandard, 7.161.26), a
	// property mismatch among the reasons (DbStoringRepoMixin.getInfo) [I].
	// A remote member's copy is matched only when its cache holds one.
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.Path {
			continue
		}
		var matches bool
		if candidate.repo.Class == repo.Remote {
			matches, err = itemkit.RemoteCopyMatches(r.Context(), st, candidate.repo, req.Path, req)
		} else {
			var it model.Item
			it, err = st.Items().Get(r.Context(), candidate.repo.Key, false, req.Path)
			if err != nil {
				httpkit.WriteErr(w, r, s.ItemError(r.Context(), "RPM virtual package", err))
				return
			}
			if it.Folder || it.Actual.SHA256 != candidate.pkg.SHA256 {
				httpkit.WriteErr(w, r, itemkit.ItemNotFound())
				return
			}
			matches, err = itemkit.MatchesMatrix(r.Context(), st, it, req)
			if err == nil && matches {
				s.ServeFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
				return
			}
		}
		if err != nil {
			httpkit.WriteErr(
				w,
				r,
				s.InternalError(r.Context(), "RPM virtual package properties", err),
			)
			return
		}
		if !matches {
			continue
		}
		s.serveVirtualRemoteRPMPackage(w, r, candidate.repo, req, candidate.pkg.SHA256, p, acc)
		return
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
}

func (s RPMAPI) serveVirtualRemoteRPMPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, req itemkit.ItemRequest,
	wantSHA string, p kernel.Principal, acc itemkit.Access,
) {
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	// The upstream is asked with the client's query, under Propagate Query
	// Params, and its matrix parameters, as for any remote file (#1371).
	q := source.Request{
		Repo:         c,
		Subject:      accessapi.SubjectOf(p),
		Access:       acc.Decider,
		Project:      c.ProjectKey,
		MatrixParams: pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		Resource: source.Resource{
			Kind:   source.ResourceKindArtifact,
			Path:   req.Path,
			Method: r.Method,
			Query:  itemkit.RemoteResource(r, req.Path).Query,
		},
	}
	q, err := itemkit.RPMSourceRequest(q)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "RPM source", err))
		return
	}
	validate := q.Resource.Validate
	q.Resource.Variant = "rpm-virtual-package-v1-" + wantSHA
	q.Resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := validate(ctx, body, size); err != nil {
			return err
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return itemkit.RPMSourceIntegrity()
		}
		h := sha256.New()
		read, err := io.Copy(h, io.LimitReader(body, size))
		if err != nil || read != size || fmt.Sprintf("%x", h.Sum(nil)) != wantSHA {
			return itemkit.RPMSourceIntegrity()
		}
		return nil
	}
	result, err := s.RemoteSources.Read(r.Context(), q)
	if err != nil {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(r.Context(), "RPM source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return
	}
	if result.Stream != nil {
		s.ServeRemoteStream(w, r, result, c, req, q.Resource, "", p.Anonymous)
		return
	}
	if !s.RemoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return
	}
	if result.Item == nil || result.Item.Actual.SHA256 != wantSHA {
		httpkit.WriteErr(w, r, pkgroute.SourceError(itemkit.RPMSourceIntegrity(), p.Anonymous))
		return
	}
	s.ServeFile(w, r, s.Store, itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}

// rpmCandidates returns the packages the members of the virtual repository
// c list below root, the first member's row for each path; for a package GET
// (download true) the later members' rows for it follow, so a GET whose
// matrix parameters the first member's file does not match can pass over to
// the next (serveVirtualRPMPackage). A member whose
// download policy refuses its repodata takes no part in the repodata, and
// only a blacked-out one in a package GET (rpmMemberRefusesMetadata). With
// download false, for repodata, a row stays when the member's or the
// virtual repository's patterns reject its file: 7.x keeps such a package
// in a virtual repository's primary.xml.gz and refuses only its GET
// (RTFACT-30656, RTFACT-5000). With download true, for a package GET, a
// row whose file the policy refuses is left out, so the GET passes over
// that member to the next one that lists the path.
func (s RPMAPI) rpmCandidates(
	ctx context.Context, st itemkit.Store, c repo.Config, root string,
	p kernel.Principal, acc itemkit.Access, download bool, seen map[string]bool,
) ([]rpmCandidate, error) {
	if seen[c.Key] || len(seen) >= 8 {
		return nil, errRPMVirtualCycle
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	selected := map[string]bool{}
	var out []rpmCandidate
	indexPath := root + "repodata/repomd.xml"
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rpm: member %s: %w", key, err)
		}
		if member.PackageType != repo.RPM {
			return nil, fmt.Errorf("%w: %s", errRPMMemberType, key)
		}
		if !acc.Folder(member, indexPath, accessapi.ActionRead) ||
			!acc.File(member, indexPath, accessapi.ActionRead) ||
			rpmMemberRefusesMetadata(member, indexPath, download) {
			continue
		}
		var entries []rpmCandidate
		if member.Class == repo.Virtual {
			entries, err = s.rpmCandidates(ctx, st, member, root, p, acc, download, seen)
		} else {
			var packages []rpm.Package
			packages, err = s.rpmMemberPackages(ctx, st, member, root, p, acc, download)
			for _, pkg := range packages {
				entries = append(entries, rpmCandidate{repo: member, pkg: pkg})
			}
		}
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rpm: member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if !download && selected[entry.pkg.Path] ||
				!acc.File(c, entry.pkg.Path, accessapi.ActionRead) ||
				!acc.File(entry.repo, entry.pkg.Path, accessapi.ActionRead) {
				continue
			}
			if download && (generic.CheckDownload(c, entry.pkg.Path, false) != nil ||
				generic.CheckDownload(entry.repo, entry.pkg.Path, false) != nil) {
				continue
			}
			selected[entry.pkg.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// rpmMemberRefusesMetadata reports whether a virtual repository's member
// takes no part because its download policy refuses the repodata file at
// path. The virtual repodata leaves out a member that refuses the
// document. A package GET checks each member at the package's own path, as
// 6.20's VirtualRepoDownloadStrategy does and as the patterns FAQ puts it
// ("artifacts that match an exclude pattern are not resolved from that
// repository"), so there the repodata is only the lookup that finds the
// file: a blacked-out member, which refuses every file, takes no part, and
// patterns that reject only the repodata leave the member's packages
// served.
func rpmMemberRefusesMetadata(member repo.Config, path string, download bool) bool {
	if !download {
		return generic.CheckDownload(member, path, false) != nil
	}
	return member.Class != repo.Virtual && generic.CheckBlackedOut(member, path) != nil
}

// rpmMemberPackages reads the packages a local or remote member's repodata
// lists below root, refusing the primary file as rpmCandidates refuses the
// member's repomd.xml (rpmMemberRefusesMetadata).
func (s RPMAPI) rpmMemberPackages(
	ctx context.Context, st itemkit.Store, c repo.Config, root string,
	p kernel.Principal, acc itemkit.Access, download bool,
) ([]rpm.Package, error) {
	manifest, err := s.rpmMemberBytes(ctx, st, c, root+"repodata/repomd.xml", p, acc)
	if err != nil {
		return nil, fmt.Errorf("rpm: parse member repomd: %w", err)
	}
	primaryPath, checksum, err := rpm.DecodeRepomd(manifest, root)
	if err != nil {
		return nil, fmt.Errorf("rpm: parse member repomd: %w", err)
	}
	if !acc.File(c, primaryPath, accessapi.ActionRead) ||
		rpmMemberRefusesMetadata(c, primaryPath, download) {
		return nil, kernel.ErrNotFound
	}
	primary, err := s.rpmMemberBytes(ctx, st, c, primaryPath, p, acc)
	if err != nil {
		return nil, err
	}
	packages, err := rpm.DecodePrimary(primary, root, checksum)
	if err != nil {
		return nil, fmt.Errorf("rpm: parse member primary: %w", err)
	}
	return packages, nil
}

func (s RPMAPI) rpmMemberBytes(
	ctx context.Context, st itemkit.Store, c repo.Config, path string,
	p kernel.Principal, acc itemkit.Access,
) ([]byte, error) {
	if c.Class == repo.Remote {
		if s.RemoteSources == nil {
			return nil, source.ErrUnavailable
		}
		q := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			Resource: source.Resource{Path: path, Method: http.MethodGet},
		}
		q, err := itemkit.RPMSourceRequest(q)
		if err != nil {
			return nil, err
		}
		result, err := s.RemoteSources.Read(ctx, q)
		if err != nil {
			return nil, fmt.Errorf(
				"rpm: remote metadata source: %w",
				pkgroute.SourceError(err, p.Anonymous),
			)
		}
		// The source owns a response body even when a later authority fence
		// fails. Close it before returning from any branch below.
		if result.Stream != nil {
			defer func() { _ = result.Stream.Body.Close() }()
		}
		if result.Revalidate != nil {
			if err := result.Revalidate(ctx); err != nil {
				return nil, fmt.Errorf(
					"rpm: remote metadata revalidate: %w",
					pkgroute.SourceError(err, p.Anonymous),
				)
			}
		}
		// Read permission on the document itself. Its download policy is the
		// caller's to apply (rpmMemberRefusesMetadata): the repodata settles
		// the member's whole policy before this read, and a package GET only
		// its blackout, which the source has already checked.
		if result.CheckPermission != nil {
			if err := result.CheckPermission(ctx, path); err != nil {
				return nil, fmt.Errorf(
					"rpm: remote metadata read authority: %w",
					pkgroute.SourceError(err, p.Anonymous),
				)
			}
		}
		var body []byte
		switch {
		case result.Stream != nil:
			body, err = boundedRPMMetadata(result.Stream.Body)
		case result.Item != nil:
			body, err = s.rpmBlob(ctx, result.Item.Actual.SHA256)
		default:
			return nil, source.ErrInvalidResult
		}
		if err != nil {
			return nil, err
		}
		if result.Used != nil {
			_ = result.Used(ctx)
		}
		return body, nil
	}
	if c.Class != repo.Local && c.Class != repo.Federated {
		return nil, kernel.ErrNotFound
	}
	it, err := st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		return nil, fmt.Errorf("rpm: member metadata item: %w", err)
	}
	if it.Folder {
		return nil, kernel.ErrNotFound
	}
	return s.rpmBlob(ctx, it.Actual.SHA256)
}

func (s RPMAPI) rpmBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("rpm: metadata blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return boundedRPMMetadata(rc)
}

func boundedRPMMetadata(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, rpmMetadataLimit+1))
	if err != nil {
		return nil, fmt.Errorf("rpm: read metadata: %w", err)
	}
	if len(body) > rpmMetadataLimit {
		return nil, errRPMMetadataTooLarge
	}
	return body, nil
}

// CalculateYumRepositoryMetadata rebuilds hosted repodata from stored RPMs,
// or verifies a virtual repository's current composed metadata view.
func (s RPMAPI) CalculateYumRepositoryMetadata(
	ctx context.Context,
	req artgen.CalculateYumRepositoryMetadataRequestObject,
) (artgen.CalculateYumRepositoryMetadataResponseObject, error) {
	st, c, _, err := s.OperationRepo(ctx, repo.RPM, itemkit.ReindexOps[repo.RPM], req.RepoKey)
	if err != nil {
		return nil, err
	}
	if c.Class == repo.Virtual {
		root, err := yumVirtualRoot(req.Params.Path)
		if err != nil {
			return nil, err
		}
		p, err := httpkit.Principal(ctx)
		if err != nil {
			return nil, err
		}
		d, _ := accessapi.DeciderFrom(ctx)
		acc, err := itemkit.AccessFor(ctx, s.Access.Authorizer, p, d)
		if err != nil {
			return nil, s.InternalError(ctx, "RPM virtual rights", err)
		}
		candidates, err := s.rpmCandidates(ctx, st, c, root, p, acc, false, map[string]bool{})
		if err != nil {
			return nil, s.ItemError(ctx, "RPM virtual calculation", err)
		}
		packages := make([]rpm.Package, 0, len(candidates))
		for _, candidate := range candidates {
			packages = append(packages, candidate.pkg)
		}
		meta, err := rpm.EncodeMetadata(packages, root, time.Unix(0, 0))
		if err != nil {
			return nil, s.InternalError(ctx, "RPM virtual metadata", err)
		}
		if err := signVirtualRPM(ctx, s.Access.Keys, c, meta.Files); err != nil {
			return nil, s.InternalError(ctx, "RPM virtual metadata signing", err)
		}
		return artgen.CalculateYumRepositoryMetadata200TextResponse(
			fmt.Sprintf("YUM metadata for virtual repository %s was calculated.", c.Key)), nil
	}
	if req.Params.Path != nil && *req.Params.Path != "" {
		return nil, httpkit.BadRequestf(
			"RPM metadata path calculation is only valid on a virtual repository",
		)
	}
	msg, err := s.Reindex(
		ctx,
		repo.RPM,
		req.RepoKey,
		"",
		req.Params.Async == nil || !*req.Params.Async,
	)
	return artgen.CalculateYumRepositoryMetadata200TextResponse(msg), err
}

func yumVirtualRoot(value *string) (string, error) {
	if value == nil || *value == "" || *value == "repodata" {
		return "", nil
	}
	p := strings.TrimSuffix(*value, "/")
	if path.Clean(p) != p || strings.HasPrefix(p, "/") || !strings.HasSuffix(p, "/repodata") {
		return "", httpkit.BadRequestf(
			"YUM path must name a repository-relative repodata directory",
		)
	}
	return strings.TrimSuffix(p, "repodata"), nil
}
