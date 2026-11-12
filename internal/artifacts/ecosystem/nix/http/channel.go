package nixhttp

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// A channel follows the NixOS Channel Registry's shape, which nix-channel
// --update expects: channels.nixos.org/<name> answers 302 to the newest
// release, releases.nixos.org/<flavor>/[<series>/]<snapshot>, which
// answers 200, and the client then downloads <final URL>/nixexprs.tar.zst,
// .tar.xz and .tar.bz2 in turn (NixOS/nix src/nix/nix-channel/
// nix-channel.cc). The JFrog documentation gives the client URL,
// channels/<name>, and the upload URL, channels/<name>/<flavor>/<snapshot>;
// that the one redirects to the other, and the page and tarball paths below
// the second, are inferred (#216).

// channels answers GET and HEAD under channels/: the channel itself, a
// snapshot's folder and its tarball. Any other path there is not found,
// as it was before channels were served.
func (h handler) channels(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 2 && nixfmt.ValidChannel(parts[1]):
		h.channel(w, r, parts[1])
		return
	case len(parts) == 4 || len(parts) == 5 && parts[4] == nixfmt.ChannelFile:
		if s, ok := nixfmt.ParseSnapshotDir(strings.Join(parts[:4], "/")); ok {
			if len(parts) == 4 {
				h.snapshotPage(w, r, s)
			} else {
				h.snapshotFile(w, r, s)
			}
			return
		}
	}
	h.fail(w, r, kernel.ErrNotFound)
}

// channel redirects to the newest snapshot of channel name that the caller
// may read. Read permission comes first (DV-057): a caller who may read
// nothing in the channel gets 403, or 401 when anonymous, before the
// blackout or the channel's existence is disclosed. The answer is computed,
// so only the blackout refuses it, at the request's own path (decision
// 0032, R2a and R9); patterns rejecting a snapshot's tarball refuse that
// download, and the channel still names it, as a listing keeps a release
// whose file they reject (R3).
func (h handler) channel(w http.ResponseWriter, r *http.Request, name string) {
	folder := "channels/" + name
	if !h.k.RequireFolder(w, r, folder, accessapi.ActionRead) {
		return
	}
	if err := h.k.CheckBlackedOut(r, folder); err != nil {
		h.fail(w, r, err)
		return
	}
	s, err := h.newest(r, name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A relative reference keeps the scheme, host and any proxy's prefix
	// the client used, which nix-channel then appends nixexprs.tar.xz to.
	target := s.Flavor + "/" + s.ID
	if p, _ := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest); !p.Folder {
		target = name + "/" + target
	}
	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.WriteHeader(http.StatusFound)
}

// newest returns the snapshot channel name names in r's repository: in a
// hosted repository, the most recently updated snapshot whose tarball the
// caller may read; in a remote one, the snapshot the channel registry
// names now; in a virtual one, the first member's in order that has one.
func (h handler) newest(r *http.Request, name string) (nixfmt.Snapshot, error) {
	c := pkgroute.RequestFrom(r).Repo
	switch c.Class {
	case repo.Virtual:
		return h.virtualNewest(r, name)
	case repo.Remote:
		s, _, err := h.remoteChannel(r, name)
		return s, err
	}
	var (
		best  model.Item
		found nixfmt.Snapshot
		ok    bool
	)
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		items, next, err := h.k.DB().Items().
			Descendants(r.Context(), c.Key, false, "channels/"+name, page)
		if err != nil {
			return nixfmt.Snapshot{}, fmt.Errorf("nix: list channel: %w", err)
		}
		for _, it := range items {
			s, valid := nixfmt.ParseSnapshotPath(it.FullPath())
			if it.Folder || !valid || s.Channel != name ||
				!h.k.Can(r, it.FullPath(), accessapi.ActionRead) {
				continue
			}
			// The upload last written leads, so publishing a snapshot again
			// rolls the channel back to it [I]: snapshot identifiers are
			// free-form, and the registry's channels move to what was
			// released last, not to the highest name.
			if !ok || it.Updated.After(best.Updated) ||
				it.Updated.Equal(best.Updated) && it.FullPath() > best.FullPath() {
				best, found, ok = it, s, true
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if !ok {
		return nixfmt.Snapshot{}, kernel.ErrNotFound
	}
	return found, nil
}

// virtualNewest asks each member in order that the caller may read the
// channel in, passing over a blacked-out member, which 7.x ignores when it
// resolves (R5), and one without the channel. An upstream fault fails
// closed, as it does for the binary cache.
func (h handler) virtualNewest(r *http.Request, name string) (nixfmt.Snapshot, error) {
	folder := "channels/" + name
	var found nixfmt.Snapshot
	err := h.members(r, func(mr *http.Request) (bool, error) {
		if !h.k.CanFolder(mr, folder, accessapi.ActionRead) ||
			h.k.CheckBlackedOut(mr, folder) != nil {
			return false, nil
		}
		s, err := h.newest(mr, name)
		if err != nil {
			return false, err
		}
		found = s
		return true, nil
	})
	return found, err
}

// members calls try with a member request for each member of r's virtual
// repository in order, until try reports that it answered. A member that
// no longer exists is skipped, and so is one for which try fails as
// missing reports. kernel.ErrNotFound means no member answered.
func (h handler) members(r *http.Request, try func(*http.Request) (bool, error)) error {
	for _, key := range pkgroute.RequestFrom(r).Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("nix: member: %w", err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return fmt.Errorf("nix: member: %w", err)
		}
		done, err := try(mr)
		if missing(err) {
			continue
		}
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return kernel.ErrNotFound
}

// snapshotPage answers a snapshot's folder with a page that links its
// tarball, as releases.nixos.org answers a release: nix-channel stores
// whatever this URL returns and then asks for nixexprs.tar.xz below it.
// The page discloses the tarball, so Read is checked there; the page is
// computed, so only the blackout refuses it, at the request's path.
func (h handler) snapshotPage(w http.ResponseWriter, r *http.Request, s nixfmt.Snapshot) {
	if !h.k.Require(w, r, s.Path(), accessapi.ActionRead) {
		return
	}
	if err := h.k.CheckBlackedOut(r, s.Dir()); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.holds(r, s); err != nil {
		h.fail(w, r, err)
		return
	}
	id := html.EscapeString(s.ID)
	write(w, r, []byte("<!DOCTYPE html>\n<html><head><title>"+id+"</title></head><body>"+
		"<a href=\""+id+"/"+nixfmt.ChannelFile+"\">"+nixfmt.ChannelFile+"</a></body></html>\n"),
		"text/html; charset=utf-8")
}

// holds reports, as nil, that r's repository has snapshot s: a hosted
// repository stores its tarball, a remote one's channel names it now, and
// a virtual one has a member that holds it whose tarball the caller may
// read.
func (h handler) holds(r *http.Request, s nixfmt.Snapshot) error {
	c := pkgroute.RequestFrom(r).Repo
	switch c.Class {
	case repo.Virtual:
		return h.members(r, func(mr *http.Request) (bool, error) {
			if !h.k.Can(mr, s.Path(), accessapi.ActionRead) ||
				h.k.CheckBlackedOut(mr, s.Dir()) != nil {
				return false, nil
			}
			return true, h.holds(mr, s)
		})
	case repo.Remote:
		current, _, err := h.remoteChannel(r, s.Channel)
		if err != nil {
			return err
		}
		if current != s {
			return kernel.ErrNotFound
		}
		return nil
	}
	it, err := h.k.DB().Items().Get(r.Context(), c.Key, false, s.Path())
	if err != nil {
		return fmt.Errorf("nix: snapshot: %w", err)
	}
	if it.Folder {
		return kernel.ErrNotFound
	}
	return nil
}

// snapshotFile serves a snapshot's tarball. A hosted repository serves it
// through ServeItem, so Read comes first (DV-057), then the download
// policy at the tarball's path, then whether it exists, as the item routes
// answer.
func (h handler) snapshotFile(w http.ResponseWriter, r *http.Request, s nixfmt.Snapshot) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		result, err := h.remoteSnapshot(r, s)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.k.ServeResult(w, r, result)
	case repo.Virtual:
		h.virtualSnapshot(w, r, s)
	default:
		h.k.ServeItem(w, r, s.Path())
	}
}

// virtualSnapshot serves the tarball from the first member in order that
// has it. The virtual repository's own patterns refuse it first, with
// their own 404; a member that refuses it, or lacks it, is passed over
// (R5).
func (h handler) virtualSnapshot(w http.ResponseWriter, r *http.Request, s nixfmt.Snapshot) {
	path := s.Path()
	if !h.k.Require(w, r, path, accessapi.ActionRead) {
		return
	}
	if err := h.k.CheckDownload(r, path); err != nil {
		h.fail(w, r, err)
		return
	}
	err := h.members(r, func(mr *http.Request) (bool, error) {
		if !h.k.Can(mr, path, accessapi.ActionRead) {
			return false, nil
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteSnapshot(mr, s)
			if err != nil {
				return false, err
			}
			h.k.ServeResult(w, mr, result)
			return true, nil
		}
		f, err := h.k.DownloadItem(mr, path)
		if err != nil {
			return false, fmt.Errorf("nix: member snapshot: %w", err)
		}
		if f.Item.Folder {
			return false, nil
		}
		h.k.ServeFound(w, mr, f)
		return true, nil
	})
	if err != nil {
		h.fail(w, r, err)
	}
}
