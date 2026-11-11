package debianhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var errDebianVirtualMemberType = errors.New(
	"server: debian virtual member has an incompatible type or layout",
)

var errDebianReleaseDigestMismatch = errors.New(
	"server: debian Packages digest differs from Release",
)

type debianVirtualView struct {
	files map[string][]byte
}

// serveVirtualDebianMetadata composes an apt view from ordered member
// Packages files. Its Release is signed with the virtual repository's key,
// and every advertised Filename resolves through the same member order.
func (s DebianAPI) serveVirtualDebianMetadata(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if len(req.Props) != 0 || r.URL.RawQuery != "" ||
		!acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
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
	parts := strings.Split(req.Path, "/")
	if len(parts) < 3 {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	view, err := s.buildVirtualDebianView(r.Context(), st, c, parts[1], p, acc)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual index", err))
		return
	}
	body, ok := view.files[req.Path]
	if !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, req.Path, time.Time{}, bytes.NewReader(body))
}

func (s DebianAPI) buildVirtualDebianView(
	ctx context.Context, st itemkit.Store, c repo.Config, distribution string,
	p kernel.Principal, acc itemkit.Access,
) (debianVirtualView, error) {
	if len(c.Repositories) == 0 || len(c.Repositories) > 128 {
		return debianVirtualView{}, kernel.ErrNotFound
	}
	indexes := map[string][]debian.PackageRecord{}
	selected := map[string]map[string]bool{}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return debianVirtualView{}, fmt.Errorf("debian virtual member %s: %w", key, err)
		}
		if member.PackageType != repo.Debian || member.Class == repo.Virtual ||
			member.DebianTrivialLayout != nil && *member.DebianTrivialLayout {
			return debianVirtualView{}, fmt.Errorf("%w: %s", errDebianVirtualMemberType, key)
		}
		if err := s.addDebianMemberIndexes(ctx, st, c, member, distribution, p, acc,
			indexes, selected); err != nil {
			return debianVirtualView{}, err
		}
	}
	if len(indexes) == 0 {
		return debianVirtualView{}, kernel.ErrNotFound
	}
	files := map[string][]byte{}
	entries := []debian.ReleaseEntry{}
	components, architectures := map[string]bool{}, map[string]bool{}
	for indexPath, rows := range indexes {
		slices.SortFunc(rows, func(a, b debian.PackageRecord) int {
			if n := strings.Compare(a.Name, b.Name); n != 0 {
				return n
			}
			if n := strings.Compare(a.Version, b.Version); n != 0 {
				return n
			}
			return strings.Compare(a.Filename, b.Filename)
		})
		var plain bytes.Buffer
		for _, row := range rows {
			if plain.Len()+len(row.Raw) > debianIndexLimit {
				return debianVirtualView{}, errDebianIndexTooLarge
			}
			plain.Write(row.Raw)
		}
		compressed, err := debian.GzipIndex(plain.Bytes())
		if err != nil {
			return debianVirtualView{}, fmt.Errorf("debian virtual Packages gzip: %w", err)
		}
		base := "dists/" + distribution + "/"
		for _, file := range []debian.ReleaseEntry{
			{Path: strings.TrimPrefix(indexPath, base), Body: plain.Bytes()},
			{Path: strings.TrimPrefix(indexPath, base) + ".gz", Body: compressed},
		} {
			files[base+file.Path] = file.Body
			entries = append(entries, file)
			sha := sha256.Sum256(file.Body)
			folder := strings.TrimSuffix(indexPath, "/Packages")
			files[folder+"/by-hash/SHA256/"+fmt.Sprintf("%x", sha)] = file.Body
		}
		parts := strings.Split(indexPath, "/")
		components[parts[2]] = true
		architectures[strings.TrimPrefix(parts[3], "binary-")] = true
	}
	release, err := debian.Release(distribution, mapKeys(components), mapKeys(architectures),
		entries, time.Now().UTC())
	if err != nil {
		return debianVirtualView{}, fmt.Errorf("debian virtual Release: %w", err)
	}
	base := "dists/" + distribution + "/"
	files[base+"Release"] = release
	if c.KeyPair != "" {
		pair, err := s.Access.Keys.Keypair(ctx, c.KeyPair)
		if err != nil {
			return debianVirtualView{}, fmt.Errorf("debian virtual signing key: %w", err)
		}
		if pair.Type != accessapi.KeypairGPG {
			return debianVirtualView{}, errDebianVirtualMemberType
		}
		signed, err := debian.SignRelease(release, pair.PrivateKey, pair.Passphrase)
		if err != nil {
			return debianVirtualView{}, fmt.Errorf("debian virtual signing: %w", err)
		}
		files[base+"InRelease"] = signed.InRelease
		files[base+"Release.gpg"] = signed.ReleaseGPG
	}
	return debianVirtualView{files: files}, nil
}

func mapKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

func (s DebianAPI) addDebianMemberIndexes(
	ctx context.Context, st itemkit.Store, virtual, member repo.Config, distribution string,
	p kernel.Principal, acc itemkit.Access,
	indexes map[string][]debian.PackageRecord, selected map[string]map[string]bool,
) error {
	releasePath := "dists/" + distribution + "/Release"
	release, _, err := s.debianMemberBytes(ctx, st, member, releasePath, p, acc)
	if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, source.ErrDenied) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("debian member %s Release: %w", member.Key, err)
	}
	refs, err := debian.ReleasePackageIndexes(distribution, release)
	if err != nil {
		return fmt.Errorf("debian member %s Release: %w", member.Key, err)
	}
	for _, ref := range refs {
		body, checkPermission, err := s.debianMemberBytes(ctx, st, member, ref.Path, p, acc)
		if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, source.ErrDenied) {
			continue
		}
		if err != nil {
			return fmt.Errorf("debian member %s Packages: %w", member.Key, err)
		}
		if int64(len(body)) != ref.Size ||
			fmt.Sprintf("%x", sha256.Sum256(body)) != ref.SHA256 {
			return fmt.Errorf("debian member %s: %w", member.Key, errDebianReleaseDigestMismatch)
		}
		rows, err := debian.DecodePackagesIndex(body)
		if err != nil {
			return fmt.Errorf("debian member %s Packages: %w", member.Key, err)
		}
		base := ref.BasePath()
		if selected[base] == nil {
			selected[base] = map[string]bool{}
		}
		// A row stays when the member's or the virtual repository's patterns
		// reject its file: 7.x merges the members' indexes as they are and
		// refuses only the file's GET (RTFACT-30830; RTFACT-30656 for a
		// virtual RPM repository), which serveVirtualDebianPackage passes on
		// to the next member. So the first member's row for a Filename is
		// listed, with its own Size and SHA256, whatever the policy says.
		for _, row := range rows {
			if selected[base][row.Filename] ||
				!acc.File(member, row.Filename, accessapi.ActionRead) ||
				!acc.File(virtual, row.Filename, accessapi.ActionRead) {
				continue
			}
			if checkPermission != nil {
				if err := checkPermission(ctx, row.Filename); errors.Is(err, source.ErrDenied) {
					continue
				} else if err != nil {
					return fmt.Errorf("debian member %s package authority: %w", member.Key, err)
				}
			}
			selected[base][row.Filename] = true
			indexes[base] = append(indexes[base], row)
		}
	}
	return nil
}

func (s DebianAPI) serveVirtualDebianPackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	if len(req.Props) != 0 || r.URL.RawQuery != "" ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual package download", err))
		return
	}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual member", err))
			return
		}
		if member.PackageType != repo.Debian || member.Class == repo.Virtual ||
			member.DebianTrivialLayout != nil && *member.DebianTrivialLayout {
			httpkit.WriteErr(
				w,
				r,
				s.ItemError(ctx, "Debian virtual member", errDebianVirtualMemberType),
			)
			return
		}
		if !acc.File(member, req.Path, accessapi.ActionRead) ||
			generic.CheckDownload(member, req.Path, false) != nil {
			continue
		}
		if member.Class == repo.Remote {
			if s.serveRemoteVirtualDebianPackage(w, r, member, req, p, acc) {
				return
			}
			continue
		}
		it, err := st.Items().Get(ctx, member.Key, false, req.Path)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian virtual package", err))
			return
		}
		if !it.Folder {
			s.ServeFile(w, r, st, itemkit.Found{Repo: member, Item: it}, p)
			return
		}
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
}

// serveRemoteVirtualDebianPackage returns false only when a member lacks the
// path, allowing ordered fallback. Integrity, permissions and upstream faults
// are terminal so the virtual path never disguises a failed selected member.
func (s DebianAPI) serveRemoteVirtualDebianPackage(
	w http.ResponseWriter, r *http.Request, member repo.Config, req itemkit.ItemRequest,
	p kernel.Principal, acc itemkit.Access,
) bool {
	// The missing service is this server's fault, not the upstream's, so it
	// answers 500 as the index read does rather than SourceError's 502.
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r,
			s.InternalError(r.Context(), "Debian virtual source", source.ErrUnavailable))
		return true
	}
	q := source.Request{
		Repo: member, Subject: accessapi.SubjectOf(p), Access: acc.Decider,
		Project: member.ProjectKey, Resource: itemkit.RemoteResource(r, req.Path),
	}
	q, err := itemkit.DebianSourceRequest(q)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian virtual package", err))
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
	s.ServeFile(w, r, s.Store,
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
			Project: member.ProjectKey,
			Resource: source.Resource{
				Path: path, EscapedPath: httpkit.EscapePath(path), Method: http.MethodGet,
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
				return nil, nil, fmt.Errorf("debian member stream: %w", err)
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
