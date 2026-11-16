package itemkit

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// indexEntry is a child a folder's HTML index lists.
type indexEntry struct {
	name   string
	folder bool
}

// writeIndex answers a folder GET with the plain HTML index of entries,
// titled "Index of <title>", and a HEAD with its headers alone.
func writeIndex(w http.ResponseWriter, r *http.Request, title string, entries []indexEntry) {
	title = html.EscapeString(title)
	var b strings.Builder
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html><head><title>Index of %s</title></head><body>\n", title)
	fmt.Fprintf(&b, "<h1>Index of %s</h1>\n<pre>", title)
	for _, e := range entries {
		href, text := url.PathEscape(e.name), e.name
		if strings.Contains(href, ":") {
			// A browser reads "sha256:<hex>/", a Docker digest folder, as a
			// URL with the scheme sha256 (RFC 3986, 4.2); the dot segment
			// keeps it relative.
			href = "./" + href
		}
		if e.folder {
			href, text = href+"/", text+"/"
		}
		fmt.Fprintf(&b, "<a href=\"%s\">%s</a>\n", html.EscapeString(href), html.EscapeString(text))
	}
	b.WriteString("</pre></body></html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, b.String())
	}
}

// listVirtualFolder answers a folder GET of folder ("" for the root) in the
// virtual repository v with the HTML index of every member's children, as
// 7.161.26's RepositoryBrowsingServiceImpl.getVirtualRepoBrowsableChildren
// Data merges them. found is whether the lookup found the folder in a
// member. It returns false, having written nothing, when no member holds
// the folder, which 7.x answers 404 (isItemNotFoundOnVirtualRepo), so the
// caller answers its own miss.
//
// Each virtual repository the walk reaches that accepts the folder
// (getSearchableRepos) lists its own members: a local or federated one's
// children, a remote one's cache children and, when it lists remote folder
// items, its upstream's (getRemoteRepoBrowsableChildrenData). A member that
// is blacked out or whose patterns reject the folder lists nothing
// (getLocalRepoBrowsableChildrenData). A child shows when the caller may
// read it in that member and the member's patterns accept it, then when
// both the virtual repository that holds the member and v accept it
// (virtualRepoAccepts). A name several members hold shows once, sorted by
// name as a member's own index is.
func (s *Kit) listVirtualFolder(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	v repo.Config,
	folder string,
	acc Access,
	found bool,
) bool {
	ctx := r.Context()
	p, _ := httpkit.Principal(ctx)
	l := &virtualListing{
		s: s, st: st, acc: acc, p: p, top: v, folder: folder,
		members: map[string]*memberChildren{}, budget: &remote.ListingBudget{},
	}
	if err := virtual.Walk(ctx, st.Repos(), v, l.visit(ctx)); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "list folder", err))
		return true
	}
	entries, held := l.merge(ctx)
	if !held && !found {
		return false
	}
	writeIndex(w, r, path.Join(v.Key, folder), entries)
	return true
}

// listUnfoundVirtualFolder answers a folder GET of the virtual repository
// c that the lookup found in no member, since a remote member's upstream
// may still list it, and reports whether it did.
func (s *Kit) listUnfoundVirtualFolder(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	c repo.Config,
	req ItemRequest,
	acc Access,
) bool {
	return c.Class == repo.Virtual && req.IsFolder &&
		s.listVirtualFolder(w, r, st, c, req.Path, acc, false)
}

// virtualListing is one folder index of a virtual repository being read:
// the routes to the members that list it, and each member's children,
// read once however many routes reach it.
type virtualListing struct {
	s       *Kit
	st      Store
	acc     Access
	p       kernel.Principal
	top     repo.Config
	folder  string
	routes  []listingRoute
	members map[string]*memberChildren
	// budget bounds the upstream listings of the whole index, as
	// remote.ListingBudget asks of one browsing operation.
	budget *remote.ListingBudget
}

// listingRoute is a member and the virtual repository that holds it.
type listingRoute struct{ parent, member repo.Config }

// memberChildren is what a member holds at the folder: its stored children
// (a remote member's cache's), its upstream listing, and whether it holds
// the folder at all.
type memberChildren struct {
	stored []model.Item
	live   *remote.FolderListing
	held   bool
}

// visit is virtual.Walk's visit: it keeps each route to a member that lists
// the folder and reads the member the first time a route reaches it.
func (l *virtualListing) visit(ctx context.Context) func([]repo.Config, repo.Config) error {
	return func(chain []repo.Config, m repo.Config) error {
		parent := chain[len(chain)-1]
		if m.Class == repo.Virtual ||
			!repo.PatternsOf(parent).AcceptsRequest(l.folder, true) ||
			generic.CheckDownload(m, l.folder, true) != nil {
			return nil
		}
		if _, ok := l.members[m.Key]; !ok {
			mc, err := l.read(ctx, m)
			if err != nil {
				return err
			}
			l.members[m.Key] = mc
		}
		l.routes = append(l.routes, listingRoute{parent: parent, member: m})
		return nil
	}
}

// read reads what the member m holds at the folder. Only a member whose
// folder the caller may read counts as holding it, so a folder the lookup
// found nowhere the caller may read keeps the lookup's own answer.
func (l *virtualListing) read(ctx context.Context, m repo.Config) (*memberChildren, error) {
	mc := &memberChildren{}
	if !l.acc.Folder(m, l.folder, accessapi.ActionRead) {
		return mc, nil
	}
	cached := m.Class == repo.Remote
	if !cached || m.StoreArtifactsLocally == nil || *m.StoreArtifactsLocally {
		var err error
		if mc.stored, mc.held, err = l.storedChildren(ctx, m.Key, cached); err != nil {
			return nil, err
		}
	}
	if cached && m.ListRemoteFolderItems.Bool() && l.s.RemoteSources != nil {
		listing, err := remote.ListFolder(ctx, l.s.RemoteSources, source.Request{
			Repo: m, Subject: accessapi.SubjectOf(l.p), Access: l.acc.Decider, Project: m.ProjectKey,
		}, l.folder, l.budget)
		// An upstream that fails to list leaves only this member's live
		// children out [I]: 7.x's RemoteRepo.listRemoteResources answers a
		// failure with no items (RTFACT-6372), and the rest of the index
		// still lists.
		if err == nil {
			mc.live = &listing
		}
	}
	return mc, nil
}

// storedChildren reads the children of the folder in the tree key and
// cached name, and whether the tree holds the folder; a file there is no
// folder to list (VirtualRepo.getChildrenNamesDeeply passes it over).
func (l *virtualListing) storedChildren(
	ctx context.Context,
	key string,
	cached bool,
) ([]model.Item, bool, error) {
	if l.folder != "" {
		it, err := l.st.Items().Get(ctx, key, cached, l.folder)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && !it.Folder {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("folder in %s: %w", key, err)
		}
	}
	var children []model.Item
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		batch, next, err := l.st.Items().Children(ctx, key, cached, l.folder, page)
		if err != nil {
			return nil, false, fmt.Errorf("children in %s: %w", key, err)
		}
		children = append(children, batch...)
		if next == "" {
			return children, true, nil
		}
		page.After = next
	}
}

// merge is the index's entries and whether any member holds the folder.
// Each upstream listing is revalidated just before it is shown, as
// remote.FolderListing asks; one that fails is left out as a listing
// failure is.
func (l *virtualListing) merge(ctx context.Context) ([]indexEntry, bool) {
	held := false
	for _, mc := range l.members {
		if mc.live != nil && mc.live.Revalidate(ctx) != nil {
			mc.live = nil
		}
		held = held || mc.held || mc.live != nil && len(mc.live.Entries) > 0
	}
	// A name several members hold is a folder when any holds a folder there
	// [I]: 7.x keeps the first member's kind (RTFACT-25900 reports a file
	// hiding a folder that way), which would hide the folder's children.
	kinds := map[string]bool{}
	add := func(rt listingRoute, p string, folder bool) {
		if !browsable(rt.parent, p, folder) || !browsable(l.top, p, folder) {
			return
		}
		name := path.Base(p)
		kinds[name] = kinds[name] || folder
	}
	for _, rt := range l.routes {
		mc := l.members[rt.member.Key]
		patterns := repo.PatternsOf(rt.member)
		for _, c := range mc.stored {
			p := c.FullPath()
			if l.acc.Item(rt.member, p, c.Folder, accessapi.ActionRead) &&
				patterns.AcceptsRequest(p, c.Folder) {
				add(rt, p, c.Folder)
			}
		}
		if mc.live != nil {
			// remote.ListFolder already kept only what the member's
			// patterns accept and the caller may read.
			for _, e := range mc.live.Entries {
				add(rt, e.Path, e.Folder)
			}
		}
	}
	entries := make([]indexEntry, 0, len(kinds))
	for name, folder := range kinds {
		entries = append(entries, indexEntry{name: name, folder: folder})
	}
	slices.SortFunc(entries, func(a, b indexEntry) int { return strings.Compare(a.name, b.name) })
	return entries, held
}

// browsable is RepositoryBrowsingServiceImpl.virtualRepoAccepts: whether
// the virtual repository v shows p, which its patterns must accept and,
// for a Maven group package type, which must be no Maven index path, one
// holding ".index" or a file named nexus-maven-repository-index*.
func browsable(v repo.Config, p string, folder bool) bool {
	if !repo.PatternsOf(v).AcceptsRequest(p, folder) {
		return false
	}
	if !repo.InMavenGroup(v.PackageType) {
		return true
	}
	if folder {
		p += "/"
	}
	name := p[strings.LastIndexByte(p, '/')+1:]
	return !strings.Contains(p, ".index") && !strings.HasPrefix(name, "nexus-maven-repository-index")
}
