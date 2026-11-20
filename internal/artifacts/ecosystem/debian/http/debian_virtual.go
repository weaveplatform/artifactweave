package debianhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var (
	errDebianVirtualKey = errors.New("server: debian virtual signing key must be gpg")
	// errDebianNoSources is a remote member read on a server built without
	// a remote source service: this server's fault, which no walk passes
	// over, so the virtual metadata answers 500 as it always has.
	errDebianNoSources = errors.New("server: debian remote member read without a source service")
	// errDebianVirtualMetadata is virtual metadata this server could not
	// encode or decode for the merged metadata cache: its fault, answered
	// 500.
	errDebianVirtualMetadata = errors.New("server: debian virtual metadata")
)

var errDebianReleaseDigestMismatch = errors.New(
	"server: debian Packages digest differs from Release",
)

// defaultDebianArchitectures is the Indexed Remote Architectures of a
// virtual repository that never set debianDefaultArchitectures: JFrog's
// Debian guide gives amd64 as the default ("The default architecture value
// is amd64"), which 7.164.0 made the default for new Debian repositories in
// place of i386 and amd64.
var defaultDebianArchitectures = []string{"amd64"}

// debianProtocol is the protocol whose kit walks a Debian virtual
// repository's members (itemkit.Kit.Packages): the overlay reads each
// member's metadata and files itself, so the sources only name the classes
// the walk keeps, a remote member as its upstream step.
func debianProtocol(k *itemkit.Kit) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:  "deb",
		Types: []repo.PackageType{repo.Debian},
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Remote: pkgroute.Remote{Service: k.RemoteSources},
		},
	}
}

// serveVirtualDebianMetadata serves a file of the apt metadata the virtual
// repository c composes from its members (virtualDebianFiles): a Release,
// its signed forms, a Packages index or a by-hash copy of one.
func (s DebianAPI) serveVirtualDebianMetadata(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "Debian virtual members") {
		return
	}
	// The virtual repository's own patterns rejecting the document leave
	// it no member to serve it from, the 404 a file gets (6.20
	// VirtualRepoDownloadStrategy); 7.x caches virtual Debian metadata as
	// a file (RTDEV-50452).
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual index download", err))
		return
	}
	// 7.x serves this document through its virtual download path, as any
	// other file (the ?trace of RTFACT-26349, 7.25.7), so a query changes
	// nothing and a matrix parameter is matched against its properties. The
	// composed document carries none [I], so only a mandatory key refuses it.
	if !debianMatrix(req).Matches(nil) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	parts := strings.Split(req.Path, "/")
	if len(parts) < 3 {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	files, err := s.virtualDebianFiles(r, st, c, parts[1], p, acc)
	if errors.Is(err, errDebianVirtualMetadata) {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "Debian virtual index", err))
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual index", err))
		return
	}
	body, ok := files[req.Path]
	if !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	itemkit.ServeContent(w, r, req.Path, time.Time{}, bytes.NewReader(body))
}

// virtualDebianFiles is the virtual repository c's apt metadata of
// distribution for the caller, every file by its path in c, kept for c's
// virtualRetrievalCachePeriodSecs, which 7.x carries for Debian (decision
// 0008, R8; pkgroute.Kit.Merged): 7.x stores a virtual repository's
// calculated Debian metadata as a file (RTDEV-50452). The set is kept
// whole, so a Release always lists the indexes served beside it.
// Metadata no member serves drops the kept set and answers not found, as
// it has before.
//
// c's own include and exclude patterns are not asked again at the
// Release: the overlay has checked them at the file the client asked for,
// as 7.x serves its stored virtual metadata through its download path, file
// by file.
func (s DebianAPI) virtualDebianFiles(
	r *http.Request, st itemkit.Store, c repo.Config, distribution string,
	p kernel.Principal, acc itemkit.Access,
) (map[string][]byte, error) {
	k, err := s.Packages(debianProtocol(s.Kit), true)
	if err != nil {
		return nil, fmt.Errorf("debian: virtual metadata: %w", err)
	}
	open := c
	open.IncludesPattern, open.ExcludesPattern = nil, nil
	release, document := "dists/"+distribution+"/Release", "dists/"+distribution+"/"
	e, err := k.Merged(k.Bind(r, open), release, document,
		func(r *http.Request) (virtual.Entry, error) {
			files, err := s.buildVirtualDebian(r, k, st, c, distribution, p, acc, nil)
			if err != nil {
				return virtual.Entry{}, err
			}
			var body bytes.Buffer
			if err := gob.NewEncoder(&body).Encode(files); err != nil {
				return virtual.Entry{}, fmt.Errorf("%w: %s", errDebianVirtualMetadata, err.Error())
			}
			return virtual.Entry{Body: body.Bytes()}, nil
		})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's and the merge's own answers
	}
	var files map[string][]byte
	if err := gob.NewDecoder(bytes.NewReader(e.Body)).Decode(&files); err != nil {
		return nil, fmt.Errorf("%w: %s", errDebianVirtualMetadata, err.Error())
	}
	return files, nil
}

// buildVirtualDebian merges the apt metadata of distribution from the
// members of the virtual repository c that the request r, bound to c by k,
// walks, and signs its Release with c's own key pair when c names one: the
// members' keys are never used. The members are the package handlers'
// walk (pkgroute.Collect) at the distribution's Release: 7.x's order,
// nested virtual repositories flattened with a cycle or a diamond skipped,
// a member of another type passed over (decision 0008, R1, R2 and R11),
// and each member that misses, refuses or fails passed over with the rest
// merged (R3); a failure is logged (Kit.WarnPassedOver) [I].
func (s DebianAPI) buildVirtualDebian(
	r *http.Request, k *pkgroute.Kit, st itemkit.Store, c repo.Config, distribution string,
	p kernel.Principal, acc itemkit.Access, checks *snapshotAuthority,
) (map[string][]byte, error) {
	release := "dists/" + distribution + "/Release"
	parts, err := pkgroute.Collect(k, r, release,
		func(mr *http.Request) ([]debian.MemberIndex, error) {
			indexes, err := s.debianMemberIndexes(mr, st, c, distribution, p, acc, checks)
			k.WarnPassedOver(mr, err)
			return indexes, err
		})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit names the member and its fault
	}
	files, err := debian.Merger{
		Distribution: distribution, Formats: c.OptionalIndexCompressionFormats,
		Date: time.Now().UTC(), Limit: debianIndexLimit,
	}.Merge(r.Context(), parts)
	if err != nil {
		return nil, err //nolint:wrapcheck // the merge's not-found and limit are the answers
	}
	if c.KeyPair == "" {
		return files, nil
	}
	pair, err := s.Access.Keys.Keypair(r.Context(), c.KeyPair)
	if err != nil {
		return nil, fmt.Errorf("debian virtual signing key: %w", err)
	}
	if pair.Type != accessapi.KeypairGPG {
		return nil, errDebianVirtualKey
	}
	base := "dists/" + distribution + "/"
	signed, err := debian.SignRelease(files[base+"Release"], pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return nil, fmt.Errorf("debian virtual signing: %w", err)
	}
	files[base+"InRelease"] = signed.InRelease
	files[base+"Release.gpg"] = signed.ReleaseGPG
	return files, nil
}

// indexedArchitectures is the virtual repository c's Indexed Remote
// Architectures (debianDefaultArchitectures), or the default when it has
// none.
func indexedArchitectures(c repo.Config) []string {
	var out []string
	for _, arch := range strings.Split(c.DebianDefaultArchitectures, ",") {
		if arch = strings.TrimSpace(arch); arch != "" {
			out = append(out, arch)
		}
	}
	if len(out) == 0 {
		return defaultDebianArchitectures
	}
	return out
}

// debianMemberIndexes reads the binary Packages indexes of distribution
// that one member of the virtual repository c lists in its Release,
// through the member request mr, each with the rows the caller may read.
//
//   - A member with the deprecated Trivial layout is passed over: JFrog's
//     Debian guide says only repositories with the Automatic layout can be
//     included in a virtual repository, and 7.x's UI offers no other
//     (GetAvailableRepositories.filterByType, 7.161.26), but nothing
//     refuses one saved through the REST API [I].
//   - A remote member's indexes are read only for c's Indexed Remote
//     Architectures (indexedArchitectures): JFrog's guide says the field
//     "specifies the architectures that will be indexed for the included
//     remote repositories" [I].
//   - A row is listed when the caller may read its file in the member: no
//     permission target can name the virtual repository (decision 0006),
//     so the member that serves a row decides it (decision 0008, R3). A
//     row stays when the member's or the virtual repository's patterns
//     reject its file: 7.x merges the members' indexes as they are and
//     refuses only the file's GET (RTFACT-30830; RTFACT-30656 for a
//     virtual RPM repository), which serveVirtualDebianFile passes on to
//     the next member.
//   - A Release or index the member misses, or the caller may not read, is
//     passed over as not found; any other failure is the member's, which
//     the walk passes over when it is a fault (pkgroute.PassedOver).
func (s DebianAPI) debianMemberIndexes(
	mr *http.Request, st itemkit.Store, c repo.Config, distribution string,
	p kernel.Principal, acc itemkit.Access, checks *snapshotAuthority,
) ([]debian.MemberIndex, error) {
	ctx := mr.Context()
	member := pkgroute.RequestFrom(mr).Repo
	if member.DebianTrivialLayout != nil && *member.DebianTrivialLayout {
		return nil, fmt.Errorf("debian member %s has the Trivial layout: %w", member.Key,
			kernel.ErrNotFound)
	}
	if member.Class == repo.Remote && s.RemoteSources == nil {
		return nil, errDebianNoSources
	}
	release, releaseCheck, err := s.debianMemberBytes(ctx, st, member, "dists/"+distribution+"/Release", p, acc)
	if err != nil {
		return nil, fmt.Errorf("debian member %s Release: %w", member.Key, err)
	}
	refs, err := debian.ReleasePackageIndexes(distribution, release)
	if err != nil {
		return nil, fmt.Errorf("debian member %s Release: %w", member.Key, err)
	}
	architectures := indexedArchitectures(c)
	var out []debian.MemberIndex
	for _, ref := range refs {
		if member.Class == repo.Remote &&
			!slices.Contains(architectures, debian.IndexArchitecture(ref.BasePath())) {
			continue
		}
		body, checkPermission, err := s.debianMemberBytes(ctx, st, member, ref.Path, p, acc)
		if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, source.ErrDenied) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("debian member %s Packages: %w", member.Key, err)
		}
		// A remote member caches its upstream's index as fetched, so one
		// whose digest its Release does not give, or that does not decode,
		// can reach the merge (RTFACT-27303, #1356). It is left out, as a
		// member without the index is [I]: 7.x would have to check it to
		// merge it, and it does not check it before caching. A local or
		// federated member's index is the server's own, so either is a fault.
		matches := int64(len(body)) == ref.Size &&
			fmt.Sprintf("%x", sha256.Sum256(body)) == ref.SHA256
		if !matches && member.Class == repo.Remote {
			continue
		}
		if !matches {
			return nil, fmt.Errorf("debian member %s: %w", member.Key, errDebianReleaseDigestMismatch)
		}
		rows, err := debian.DecodePackagesIndex(ctx, body)
		if err != nil && member.Class == repo.Remote {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("debian member %s Packages: %w", member.Key, err)
		}
		checks.add(member, ref.Path, checkPermission)
		index := debian.MemberIndex{Path: ref.BasePath()}
		for _, row := range rows {
			if !acc.File(member, row.Filename, accessapi.ActionRead) {
				continue
			}
			if checkPermission != nil {
				if err := checkPermission(ctx, row.Filename); errors.Is(err, source.ErrDenied) {
					continue
				} else if err != nil {
					return nil, fmt.Errorf("debian member %s package authority: %w", member.Key, err)
				}
			}
			checks.add(member, row.Filename, checkPermission)
			index.Rows = append(index.Rows, row)
		}
		out = append(out, index)
	}
	if len(out) != 0 {
		checks.add(member, "dists/"+distribution+"/Release", releaseCheck)
	}
	return out, nil
}

// serveVirtualDebianFile serves a binary package, or a file under dists/
// that the virtual repository c does not compose, from the first member, in
// the walk's order, that may serve it and holds it; what names the file in
// the errors the route logs. 7.x calculates a virtual repository's Release
// files, Packages indexes and their by-hash copies (RTFACT-22893,
// RTFACT-31082, RTFACT-29321), and a GET of another dists/ file reaches a
// member as a package's does: a member's component Release (RTFACT-19086)
// and the installer files of JFrog's workaround in RTFACT-30014 come
// through the virtual repository. The members are the package handlers'
// walk at the file's path (pkgroute.Kit.MergeMembers): 7.x's order, nested
// virtual repositories flattened with a cycle skipped, and a member of
// another type, one whose download policy refuses the path, or one the
// caller may read nothing in passed over (decision 0008, R1 to R3, R11). A
// member with the Trivial layout is passed over too, as the metadata's
// walk passes it over (debianMemberIndexes). A member's Sources index is
// read whole, so that it reaches only a caller who may read every file it
// lists.
func (s DebianAPI) serveVirtualDebianFile(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access, what string,
) {
	ctx := r.Context()
	if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "Debian virtual members") {
		return
	}
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual "+what+" download", err))
		return
	}
	k, err := s.Packages(debianProtocol(s.Kit), false)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual members", err))
		return
	}
	// c's own patterns were checked above, at the file the client asked
	// for, so the walk's check of them is left out.
	open := c
	open.IncludesPattern, open.ExcludesPattern = nil, nil
	requests, err := k.MergeMembers(k.Bind(r, open), req.Path)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual members", err))
		return
	}
	for _, mr := range requests {
		member := pkgroute.RequestFrom(mr).Repo
		if member.DebianTrivialLayout != nil && *member.DebianTrivialLayout ||
			!acc.File(member, req.Path, accessapi.ActionRead) {
			continue
		}
		if debian.SourcesIndexPath(req.Path) {
			if s.serveVirtualDebianSources(w, r, st, member, req, p, acc) {
				return
			}
			continue
		}
		if member.Class == repo.Remote {
			if s.serveRemoteVirtualDebianFile(w, r, member, req, p, acc, what) {
				return
			}
			continue
		}
		it, err := st.Items().Get(ctx, member.Key, false, req.Path)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual "+what, err))
			return
		}
		if it.Folder {
			continue
		}
		// A member's file that the matrix parameters do not match does not
		// serve the request, and the walk asks the next member, as 6.20's
		// VirtualRepoDownloadStrategy.processStandard passes over a member
		// whose resource is not found [I].
		if err := matchDebianMatrix(ctx, st, it, req); errors.Is(err, kernel.ErrNotFound) {
			continue
		} else if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual "+what, err))
			return
		}
		s.ServeItemFile(w, r, st, itemkit.Found{Repo: member, Item: it}, p)
		return
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
}

// serveVirtualDebianSources answers member's Sources index through the
// virtual repository the request names, as it is, and reports whether it answered: false,
// for the next member, when the member lacks the index or refuses it. 7.x
// composes no Sources index for a virtual repository: the JFrog guide
// resolves source packages from local and remote repositories only, and in
// RTFACT-7415, filed against a 3.x virtual repository, JFrog support said
// Artifactory treats a Sources file as a regular file. So the index comes
// from the member as any other dists/ file does [I]. As the member's own
// index read does (serveDebianIndex), it reaches only a caller who may read
// every file it lists in the member, whose permissions decide a read
// through the virtual repository, as no permission target can name one
// (decision 0006; decision 0008, R3). A caller who may not is refused, not passed to
// the next member, since this member holds the index the walk selected.
func (s DebianAPI) serveVirtualDebianSources(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, member repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	ctx := r.Context()
	// A member takes the request as its own GET of the index would
	// (serveDebianIndex): a stored index, or a copy a remote member's cache
	// holds, is matched against the matrix parameters, and a member whose
	// file does not match lacks it here. A remote member passes the query
	// (#1349) and the matrix parameters (#1371) to its upstream.
	if len(req.Props) != 0 {
		var err error
		if member.Class == repo.Remote {
			var matches bool
			matches, err = itemkit.RemoteCopyMatches(ctx, st, member, req.Path, req)
			if err == nil && !matches {
				err = kernel.ErrNotFound
			}
		} else {
			var it model.Item
			it, err = st.Items().Get(ctx, member.Key, false, req.Path)
			if err == nil && !it.Folder {
				err = matchDebianMatrix(ctx, st, it, req)
			}
		}
		if errors.Is(err, kernel.ErrNotFound) {
			return false
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual index", err))
			return true
		}
	}
	body, checkPermission, err := s.debianMemberRead(ctx, st, member, req.Path,
		itemkit.RemoteResource(r, req.Path).Query, pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		p, acc)
	if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, source.ErrDenied) {
		return false
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual index", err))
		return true
	}
	if len(body) > debianIndexLimit {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "Debian virtual index", errDebianIndexTooLarge))
		return true
	}
	paths, err := remoteDebianIndexPaths(ctx, req.Path, body, member)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "Debian virtual index", err))
		return true
	}
	if err := s.debianIndexReadable(ctx, paths, checkPermission, p, acc, member); err != nil {
		httpkit.WriteErr(w, r, err)
		return true
	}
	writeDebianIndex(w, r, req.Path, bytes.NewReader(body), nil)
	return true
}

// serveRemoteVirtualDebianFile returns false only when a member lacks the
// path, allowing ordered fallback. Integrity, permissions and upstream faults
// are terminal so the virtual path never disguises a failed selected member.
// what names the file in the errors the route logs.
func (s DebianAPI) serveRemoteVirtualDebianFile(
	w http.ResponseWriter, r *http.Request, member repo.Config, req itemkit.ItemRequest,
	p kernel.Principal, acc itemkit.Access, what string,
) bool {
	// The missing service is this server's fault, not the upstream's, so it
	// answers 500 as the index read does rather than SourceError's 502.
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r,
			s.InternalError(r.Context(), "Debian virtual source", source.ErrUnavailable))
		return true
	}
	// The member gets the request as its own GET would: the query, less
	// the server's own controls such as skipUpdateStats, and the matrix
	// parameters, which a copy its cache holds must match and which its
	// upstream is asked with. 7.x resolves a virtual repository's remote
	// member through that member's own internalGetInfo, which matches only
	// a cached copy and serves the upstream's answer unmatched (7.161.26),
	// so a member whose cached copy does not match is passed over and a
	// stream or a fresh copy is served whatever the parameters.
	if matches, err := itemkit.RemoteCopyMatches(
		r.Context(), s.Store, member, req.Path, req,
	); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual "+what, err))
		return true
	} else if !matches {
		return false
	}
	q := source.Request{
		Repo: member, Subject: accessapi.SubjectOf(p), Access: acc.Decider,
		Project: member.ProjectKey, Resource: itemkit.RemoteResource(r, req.Path),
		Matrix: debianMatrix(req), MatrixParams: pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
	}
	q, err := itemkit.DebianSourceRequest(q)
	if errors.Is(err, kernel.ErrNotFound) && strings.HasPrefix(req.Path, "dists/") {
		// Under dists/ the request refuses only a name shaped like a
		// by-hash file or an index apt reads that is not one, such as a
		// by-hash name that is no SHA-256, without asking the upstream
		// (#1273). The member answers its own GET of it not found, so it
		// lacks the file, and the walk asks the next member, as 7.x passes
		// over a member that does not serve a path (decision 0032, R5). A
		// package path the request refuses keeps its answer from before
		// #1254.
		return false
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual "+what, err))
		return true
	}
	result, err := s.RemoteSources.Read(r.Context(), q)
	if errors.Is(err, kernel.ErrNotFound) {
		return false
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual source",
			pkgroute.SourceError(err, p.Anonymous)))
		return true
	}
	if result.Stream != nil {
		s.ServeRemoteStream(w, r, result, member, req, q.Resource, "", p.Anonymous)
		return true
	}
	if !s.RemoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return true
	}
	if result.Item == nil {
		httpkit.WriteErr(
			w,
			r,
			s.InternalError(r.Context(), "Debian virtual source", source.ErrInvalidResult),
		)
		return true
	}
	s.ServeItemFile(w, r, s.Store,
		itemkit.Found{Repo: member, Item: *result.Item, Used: result.Used}, p)
	return true
}

// debianMemberBytes reads only an authorized, validated member representation.
// Remote metadata uses source.Policy so the selected cache variant retains
// upstream freshness and redirect constraints. For a remote member it also
// returns the read's permission-only check of the packages the document
// lists (source.Result.CheckPermission), and nil for any other.
//
// A path the caller may not read, or that the member's download policy
// refuses, is source.ErrDenied, which a virtual merge leaves the member out
// for. A policy refusal also carries the policy's 404 and message naming
// <member>:<path> (pkgroute.IsRefusal), which a snapshot of the member
// answers with, as a GET of that file would.
func (s DebianAPI) debianMemberBytes(
	ctx context.Context, st itemkit.Store, member repo.Config, path string,
	p kernel.Principal, acc itemkit.Access,
) ([]byte, func(context.Context, string) error, error) {
	return s.debianMemberRead(ctx, st, member, path, "", "", p, acc)
}

// debianMemberRead is debianMemberBytes for a request that carried query
// and the matrix parameters matrix (source.Request.MatrixParams), which a
// remote member passes to its upstream as its own GET would.
func (s DebianAPI) debianMemberRead(
	ctx context.Context, st itemkit.Store, member repo.Config, path, query, matrix string,
	p kernel.Principal, acc itemkit.Access,
) ([]byte, func(context.Context, string) error, error) {
	if !acc.File(member, path, accessapi.ActionRead) {
		return nil, nil, source.ErrDenied
	}
	if err := generic.CheckDownload(member, path, false); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", source.ErrDenied, err)
	}
	if member.Class == repo.Remote {
		if s.RemoteSources == nil {
			return nil, nil, source.ErrUnavailable
		}
		q := source.Request{
			Repo: member, Subject: accessapi.SubjectOf(p), Access: acc.Decider,
			Project: member.ProjectKey, MatrixParams: matrix,
			Resource: source.Resource{
				Path: path, EscapedPath: httpkit.EscapePath(path), Method: http.MethodGet,
				Query: query,
			},
		}
		q, err := itemkit.DebianSourceRequest(q)
		if err != nil {
			return nil, nil, err
		}
		result, err := s.RemoteSources.Read(ctx, q)
		if err != nil {
			if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, source.ErrDenied) {
				return nil, nil, fmt.Errorf("debian member source: %w", err)
			}
			return nil, nil, fmt.Errorf("debian member source: %w",
				pkgroute.SourceError(err, p.Anonymous))
		}
		if result.Stream != nil {
			defer func() { _ = result.Stream.Body.Close() }()
			body, err := io.ReadAll(io.LimitReader(result.Stream.Body, debianIndexLimit+1))
			if err != nil {
				// The member's copy is the server's: a storage fault
				// (#1488).
				return nil, nil, fmt.Errorf("debian member stream: %w",
					pkgroute.ServedReadFault(ctx, err))
			}
			return body, result.CheckPermission, nil
		}
		if result.Item == nil {
			return nil, nil, source.ErrInvalidResult
		}
		body, err := s.readDebianBlob(ctx, result.Item.Actual.SHA256)
		return body, result.CheckPermission, err
	}
	it, err := st.Items().Get(ctx, member.Key, false, path)
	if err != nil {
		return nil, nil, fmt.Errorf("debian member item: %w", err)
	}
	if it.Folder {
		return nil, nil, kernel.ErrNotFound
	}
	body, err := s.readDebianBlob(ctx, it.Actual.SHA256)
	return body, nil, err
}
