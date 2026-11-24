package rpmhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

const rpmMetadataLimit = 36 << 20

var (
	errRPMVirtualKey       = errors.New("server: rpm: virtual signing key must be gpg")
	errRPMMetadataTooLarge = errors.New("server: rpm: metadata too large")
	// errRPMNoSources is a remote member read on a server built without a
	// remote source service: this server's fault, which no walk passes
	// over, so the virtual repodata answers 500 as it always has.
	errRPMNoSources = errors.New("server: rpm: remote member read without a source service")
	// errRPMNoMember is virtual repodata no member serves: every member
	// missed, refused or was passed over. It is not found to the merged
	// metadata cache, which drops the virtual repository's entry
	// (virtual.Cache.Get), and the overlay answers it with empty repodata.
	errRPMNoMember = fmt.Errorf("server: rpm: no member serves the repodata: %w",
		kernel.ErrNotFound)
	// errRPMVirtualMetadata is virtual repodata this server could not
	// encode or sign: its fault, answered 500 whatever the key store said.
	errRPMVirtualMetadata = errors.New("server: rpm: virtual repodata")
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
	if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "RPM virtual members") {
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
	files, err := s.virtualRPMFiles(r, st, c, root, p, acc, false)
	if err != nil {
		httpkit.WriteErr(w, r, s.virtualRPMError(r.Context(), "RPM virtual metadata", err))
		return
	}
	body, ok := files["repodata/"+name]
	if !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	itemkit.ServeContent(sw, r, name, time.Time{}, bytes.NewReader(body))
	_ = sw.Complete(r)
}

func signVirtualRPM(
	ctx context.Context,
	keys accessapi.Keys,
	c repo.Config,
	files map[string][]byte,
) error {
	pair, signs, err := accessapi.MetadataSigningPair(ctx, keys, c.KeyPair)
	if err != nil {
		return fmt.Errorf("rpm: virtual signing key: %w", err)
	}
	if !signs {
		return nil
	}
	if pair.Type != accessapi.KeypairGPG {
		return errRPMVirtualKey
	}
	if err := keymaterial.CheckPublic(keymaterial.GPG, pair.PublicKey); err != nil {
		return fmt.Errorf("rpm: virtual public key: %w", err)
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
	if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "RPM virtual members") {
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
	candidates, err := s.virtualRPMCandidates(r, st, c, root, req.Path, p, acc)
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
				s.ServeItemFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
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
	s.ServeItemFile(w, r, s.Store, itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}

// rpmProtocol is the protocol whose kit walks an RPM virtual repository's
// members (itemkit.Kit.Packages): the overlay reads each member's repodata
// itself, so the sources only name the classes the walk keeps, a remote
// member as its upstream step.
func rpmProtocol(k *itemkit.Kit) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:  "rpm",
		Types: []repo.PackageType{repo.RPM},
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Remote: pkgroute.Remote{Service: k.RemoteSources},
		},
	}
}

// virtualRPMFiles is the virtual repository c's repodata below root for
// the caller, every file by its path below root: its members' packages
// (rpm.Merger) encoded as one set, so repomd.xml always names the
// primary.xml.gz served beside it, and signed with c's own GPG key pair
// when it names one (signVirtualRPM). The members are the package
// handlers' walk (pkgroute.Collect) at root's repomd.xml: 7.x's order,
// nested virtual repositories flattened with a cycle or a diamond
// skipped, a member of another type passed over (decision 0008, R1, R2
// and R11), and each member that misses, refuses or fails passed over
// with the rest listed (R3): RTFACT-30422's YUM virtual still serves
// repomd.xml, without the packages of a remote member that fails [I].
// The set is kept for c's virtualRetrievalCachePeriodSecs, which 7.x
// carries for RPM (R8, pkgroute.Kit.Merged), unless fresh asks for a new
// one (the YUM calculation). Repodata no member serves drops the kept set
// and answers empty, as it has before [I].
//
// c's own include and exclude patterns are not asked again at repomd.xml:
// the overlay has checked them at the file the client asked for, as 7.x
// serves its stored virtual repodata through its download path, file by
// file (RTFACT-14054), and the calculation never asked them.
func (s RPMAPI) virtualRPMFiles(
	r *http.Request, st itemkit.Store, c repo.Config, root string,
	p kernel.Principal, acc itemkit.Access, fresh bool,
) (map[string][]byte, error) {
	k, err := s.Packages(rpmProtocol(s.Kit), true)
	if err != nil {
		return nil, fmt.Errorf("rpm: virtual repodata: %w", err)
	}
	open := c
	open.IncludesPattern, open.ExcludesPattern = nil, nil
	bound := k.Bind(r, open)
	repomd, document := root+"repodata/repomd.xml", root+"repodata/"
	if fresh {
		k.EvictMerged(bound, document)
	}
	e, err := k.Merged(bound, repomd, document, func(r *http.Request) (virtual.Entry, error) {
		parts, err := pkgroute.Collect(k, r, repomd, func(mr *http.Request) ([]rpm.Package, error) {
			rows, err := s.memberRPMPackages(mr, st, root, "", p, acc)
			k.WarnPassedOver(mr, err)
			return rows, err
		})
		if err != nil {
			return virtual.Entry{}, err //nolint:wrapcheck // the kit names the member and its fault
		}
		if len(parts) == 0 {
			return virtual.Entry{}, errRPMNoMember
		}
		packages, _ := rpm.Merger{}.Merge(r.Context(), parts)
		files, err := s.signedRPMFiles(r.Context(), c, root, packages)
		if err != nil {
			return virtual.Entry{}, err
		}
		var body bytes.Buffer
		if err := gob.NewEncoder(&body).Encode(files); err != nil {
			return virtual.Entry{}, fmt.Errorf("%w: %s", errRPMVirtualMetadata, err.Error())
		}
		return virtual.Entry{Body: body.Bytes()}, nil
	})
	if errors.Is(err, errRPMNoMember) {
		return s.signedRPMFiles(r.Context(), c, root, nil)
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's and the merge's own answers
	}
	var files map[string][]byte
	if err := gob.NewDecoder(bytes.NewReader(e.Body)).Decode(&files); err != nil {
		return nil, fmt.Errorf("%w: %s", errRPMVirtualMetadata, err.Error())
	}
	if key, ok := files["repodata/repomd.xml.key"]; ok {
		if err := keymaterial.CheckPublic(keymaterial.GPG, string(key)); err != nil {
			return nil, fmt.Errorf("%w: invalid public key material", errRPMVirtualMetadata)
		}
	}
	return files, nil
}

// signedRPMFiles encodes packages as the virtual repository c's repodata
// below root, signed with c's key pair (signVirtualRPM).
func (s RPMAPI) signedRPMFiles(
	ctx context.Context, c repo.Config, root string, packages []rpm.Package,
) (map[string][]byte, error) {
	meta, err := rpm.EncodeMetadata(packages, root, time.Unix(0, 0))
	if err == nil {
		err = signVirtualRPM(ctx, s.Access.Keys, c, meta.Files)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errRPMVirtualMetadata, err.Error())
	}
	return meta.Files, nil
}

// virtualRPMError answers err from the virtual repodata: 500 for repodata
// this server could not encode or sign, and the item answers otherwise.
func (s RPMAPI) virtualRPMError(ctx context.Context, op string, err error) error {
	if errors.Is(err, errRPMVirtualMetadata) {
		return s.InternalError(ctx, op, err)
	}
	return s.ItemError(ctx, op, err)
}

// virtualRPMCandidates are the members of the virtual repository c whose
// repodata below root lists the package at path, in the walk's order,
// each with its row, so a GET whose matrix parameters the first member's
// file does not match passes over to the next (serveVirtualRPMPackage).
// The walk is the package handlers' at the package's own path
// (pkgroute.Collect), so each member's policy is checked at that path,
// never at the repodata read to find it, as 6.20's
// VirtualRepoDownloadStrategy and the patterns FAQ have it ("artifacts
// that match an exclude pattern are not resolved from that repository"):
// a member whose patterns reject only its repodata still serves the
// package, and one that rejects the package takes no part.
func (s RPMAPI) virtualRPMCandidates(
	r *http.Request, st itemkit.Store, c repo.Config, root, path string,
	p kernel.Principal, acc itemkit.Access,
) ([]rpmCandidate, error) {
	k, err := s.Packages(rpmProtocol(s.Kit), true)
	if err != nil {
		return nil, fmt.Errorf("rpm: virtual package: %w", err)
	}
	parts, err := pkgroute.Collect(k, k.Bind(r, c), path, func(mr *http.Request) ([]rpm.Package, error) {
		rows, err := s.memberRPMPackages(mr, st, root, path, p, acc)
		k.WarnPassedOver(mr, err)
		return rows, err
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit names the member and its fault
	}
	var out []rpmCandidate
	for _, part := range parts {
		for _, pkg := range part.Doc {
			out = append(out, rpmCandidate{repo: part.Member, pkg: pkg})
		}
	}
	return out, nil
}

// memberRPMPackages reads the packages one member of a virtual repository
// lists in its repodata below root, through the member request mr,
// filtered by the caller's Read in that member alone: no permission
// target can name the virtual repository (decision 0006), so the member
// that serves a row decides it (decision 0008, R3). A member whose
// repodata the caller may not read is passed over as not found. For the
// virtual repodata (pkg "") a row whose file the patterns reject stays,
// as 7.x keeps such a package in a virtual repository's primary.xml.gz and
// refuses only its GET (RTFACT-30656, RTFACT-5000); for a package GET
// (pkg its path) only that package's row is kept, and the repodata is
// only the lookup that finds it, so only the member's blackout refuses it
// (rpmMemberRefusesMetadata).
func (s RPMAPI) memberRPMPackages(
	mr *http.Request, st itemkit.Store, root, pkg string, p kernel.Principal, acc itemkit.Access,
) ([]rpm.Package, error) {
	m := pkgroute.RequestFrom(mr).Repo
	repomd := root + "repodata/repomd.xml"
	if !acc.Folder(m, repomd, accessapi.ActionRead) || !acc.File(m, repomd, accessapi.ActionRead) {
		return nil, kernel.ErrForbidden
	}
	if m.Class == repo.Remote && s.RemoteSources == nil {
		return nil, errRPMNoSources
	}
	download := pkg != ""
	packages, err := s.rpmMemberPackages(mr.Context(), st, m, root, p, acc, download)
	if err != nil {
		return nil, err
	}
	var rows []rpm.Package
	for _, row := range packages {
		if (!download || row.Path == pkg) && acc.File(m, row.Path, accessapi.ActionRead) {
			rows = append(rows, row)
		}
	}
	return rows, nil
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
	manifest, err := s.rpmMemberBytes(ctx, st, c, root+"repodata/repomd.xml", p, acc, download)
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
	primary, err := s.rpmMemberBytes(ctx, st, c, primaryPath, p, acc, download)
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
	p kernel.Principal, acc itemkit.Access, download bool,
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
		// A package lookup checks patterns at the package, while metadata
		// delivery checks the repodata file. Both retain source fences.
		q.Resource.BlackoutOnly = download
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
		// 7.x calculates a virtual repository's repodata on this call and
		// stores it for later reads (Calculate YUM Repository Metadata:
		// "Supported by local, virtual, and Federated"; RTFACT-14054), so
		// the kept set is dropped and built again, and the next read gets
		// the new one [I].
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)
		if err != nil {
			return nil, s.InternalError(ctx, "RPM virtual calculation", err)
		}
		if _, err := s.virtualRPMFiles(r, st, c, root, p, acc, true); err != nil {
			return nil, s.virtualRPMError(ctx, "RPM virtual calculation", err)
		}
		// [I] The archived sample answers a local calculation only (#216).
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
