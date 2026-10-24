package server

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

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errDebianVirtualMemberType = errors.New(
	"debian virtual member has an incompatible type or layout",
)
var errDebianReleaseDigestMismatch = errors.New("debian Packages digest differs from Release")

type debianVirtualView struct {
	files map[string][]byte
}

// serveVirtualDebianMetadata composes an apt view from ordered member
// Packages files. Its Release is signed with the virtual repository's key,
// and every advertised Filename resolves through the same member order.
func (s artifactoryService) serveVirtualDebianMetadata(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) {
	if len(req.props) != 0 || r.URL.RawQuery != "" ||
		!acc.folder(c, req.path, types.ActionRead) ||
		!acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	parts := strings.Split(req.path, "/")
	if len(parts) < 3 {
		writeErr(w, r, itemNotFound())
		return
	}
	view, err := s.buildVirtualDebianView(r.Context(), st, c, parts[1], p, acc)
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Debian virtual index", err))
		return
	}
	body, ok := view.files[req.path]
	if !ok {
		writeErr(w, r, itemNotFound())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, req.path, time.Time{}, bytes.NewReader(body))
}

func (s artifactoryService) buildVirtualDebianView(
	ctx context.Context, st store.Store, c repo.Config, distribution string,
	p auth.Principal, acc access,
) (debianVirtualView, error) {
	if len(c.Repositories) == 0 || len(c.Repositories) > 128 {
		return debianVirtualView{}, store.ErrNotFound
	}
	indexes := map[string][]debian.PackageRecord{}
	selected := map[string]map[string]bool{}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
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
		return debianVirtualView{}, store.ErrNotFound
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
		pair, err := st.Keys().Keypair(ctx, c.KeyPair)
		if err != nil {
			return debianVirtualView{}, fmt.Errorf("debian virtual signing key: %w", err)
		}
		if pair.Type != types.KeypairGPG {
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

func (s artifactoryService) addDebianMemberIndexes(
	ctx context.Context, st store.Store, virtual, member repo.Config, distribution string,
	p auth.Principal, acc access,
	indexes map[string][]debian.PackageRecord, selected map[string]map[string]bool,
) error {
	releasePath := "dists/" + distribution + "/Release"
	release, _, err := s.debianMemberBytes(ctx, st, member, releasePath, p, acc)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, source.ErrDenied) {
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
		body, checkRead, err := s.debianMemberBytes(ctx, st, member, ref.Path, p, acc)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, source.ErrDenied) {
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
		for _, row := range rows {
			if selected[base][row.Filename] ||
				!acc.file(member, row.Filename, types.ActionRead) ||
				!acc.file(virtual, row.Filename, types.ActionRead) ||
				generic.CheckDownload(member, row.Filename, false) != nil ||
				generic.CheckDownload(virtual, row.Filename, false) != nil {
				continue
			}
			if checkRead != nil {
				if err := checkRead(ctx, row.Filename); errors.Is(err, source.ErrDenied) {
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

func (s artifactoryService) serveVirtualDebianPackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) {
	ctx := r.Context()
	if len(req.props) != 0 || r.URL.RawQuery != "" ||
		!acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			writeErr(w, r, s.itemError(ctx, "Debian virtual member", err))
			return
		}
		if member.PackageType != repo.Debian || member.Class == repo.Virtual ||
			member.DebianTrivialLayout != nil && *member.DebianTrivialLayout {
			writeErr(w, r, s.itemError(ctx, "Debian virtual member", errDebianVirtualMemberType))
			return
		}
		if !acc.file(member, req.path, types.ActionRead) ||
			generic.CheckDownload(member, req.path, false) != nil {
			continue
		}
		if member.Class == repo.Remote {
			if s.serveRemoteVirtualDebianPackage(w, r, member, req, p, acc) {
				return
			}
			continue
		}
		it, err := st.Items().Get(ctx, member.Key, false, req.path)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			writeErr(w, r, s.itemError(ctx, "Debian virtual package", err))
			return
		}
		if !it.Folder {
			s.serveFile(w, r, st, found{repo: member, item: it}, p)
			return
		}
	}
	writeErr(w, r, itemNotFound())
}

// serveRemoteVirtualDebianPackage returns false only when a member lacks the
// path, allowing ordered fallback. Integrity, permissions and upstream faults
// are terminal so the virtual path never disguises a failed selected member.
func (s artifactoryService) serveRemoteVirtualDebianPackage(
	w http.ResponseWriter, r *http.Request, member repo.Config, req itemRequest,
	p auth.Principal, acc access,
) bool {
	if s.cfg.RemoteSources == nil {
		writeErr(w, r, s.itemError(r.Context(), "Debian virtual source",
			pkgroute.SourceError(source.ErrUnavailable, p.Anonymous)))
		return true
	}
	q := source.Request{
		Repo: member, Subject: p.Subject(), Access: acc.d, Project: member.ProjectKey,
		Resource: remoteResource(r, req.path),
	}
	q, err := debianSourceRequest(q)
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Debian virtual package", err))
		return true
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), q)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Debian virtual source",
			pkgroute.SourceError(err, p.Anonymous)))
		return true
	}
	if result.Stream != nil {
		s.serveRemoteStream(w, r, result, member, req, q.Resource, "", p.Anonymous)
		return true
	}
	if !s.remoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return true
	}
	if result.Item == nil {
		writeErr(
			w,
			r,
			s.internalError(r.Context(), "Debian virtual source", source.ErrInvalidResult),
		)
		return true
	}
	s.serveFile(w, r, s.cfg.Store,
		found{repo: member, item: *result.Item, used: result.Used}, p)
	return true
}

// debianMemberBytes reads only an authorized, validated member representation.
// Remote metadata uses source.Policy so the selected cache variant retains
// upstream freshness and redirect constraints.
func (s artifactoryService) debianMemberBytes(
	ctx context.Context, st store.Store, member repo.Config, path string,
	p auth.Principal, acc access,
) ([]byte, func(context.Context, string) error, error) {
	if !acc.file(member, path, types.ActionRead) ||
		generic.CheckDownload(member, path, false) != nil {
		return nil, nil, source.ErrDenied
	}
	if member.Class == repo.Remote {
		if s.cfg.RemoteSources == nil {
			return nil, nil, source.ErrUnavailable
		}
		q := source.Request{
			Repo: member, Subject: p.Subject(), Access: acc.d,
			Project: member.ProjectKey,
			Resource: source.Resource{
				Path: path, EscapedPath: escapePath(path), Method: http.MethodGet,
			},
		}
		q, err := debianSourceRequest(q)
		if err != nil {
			return nil, nil, err
		}
		result, err := s.cfg.RemoteSources.Read(ctx, q)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, source.ErrDenied) {
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
			return body, result.CheckRead, nil
		}
		if result.Item == nil {
			return nil, nil, source.ErrInvalidResult
		}
		body, err := s.readDebianBlob(ctx, result.Item.Actual.SHA256)
		return body, result.CheckRead, err
	}
	it, err := st.Items().Get(ctx, member.Key, false, path)
	if err != nil {
		return nil, nil, fmt.Errorf("debian member item: %w", err)
	}
	if it.Folder {
		return nil, nil, store.ErrNotFound
	}
	body, err := s.readDebianBlob(ctx, it.Actual.SHA256)
	return body, nil, err
}
