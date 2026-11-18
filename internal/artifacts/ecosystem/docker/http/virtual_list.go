package dockerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type (
	listingMember struct {
		c      repo.Config
		chain  []repo.Config
		cached bool
	}
	listingDocument struct {
		list     *sortedTags
		result   source.Result
		err      error
		disabled bool
	}
	listingWalk struct {
		g       *Registry
		r       *http.Request
		q       Request
		members []listingMember
		docs    map[string]*listingDocument
		locals  map[string]*layout.TagPages
		known   bool
	}
)

func (g *Registry) listingMembers(r *http.Request, c repo.Config) ([]listingMember, error) {
	if c.Class != repo.Virtual {
		return []listingMember{{c: c}}, nil
	}
	chains := map[string][]repo.Config{}
	err := virtual.Walk(r.Context(), g.store.Repos(), c, func(chain []repo.Config, m repo.Config) error {
		if m.Class != repo.Virtual {
			if _, seen := chains[m.Key]; !seen {
				chains[m.Key] = slices.Clone(chain)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("registry listing routes: %w", err)
	}
	members, err := virtual.Members(r.Context(), g.store.Repos(), c, virtual.Options{Order: virtual.Package, NoUpstream: remote.NoOriginUpstream(r.Context(), c)})
	if err != nil {
		return nil, fmt.Errorf("registry listing members: %w", err)
	}
	out := make([]listingMember, 0, len(members))
	for _, m := range members {
		if m.Repo.PackageType == c.PackageType {
			out = append(out, listingMember{c: m.Repo, chain: chains[m.Repo.Key], cached: m.Repo.Class == repo.Remote && !m.Upstream})
		}
	}
	return out, nil
}

func (m listingMember) accepts(name string, folder bool) bool {
	for _, c := range append(slices.Clip(m.chain), m.c) {
		if generic.CheckBlackedOut(c, name) != nil || !repo.PatternsOf(c).Accepts(name, folder) {
			return false
		}
	}
	return true
}

// A source document is read once per member in this request, even when readPage
// reopens a long page. Only its bounded decoded immutable document is shared;
// every request retains its own source authority fence and member visibility.
func (s *listingWalk) document(ctx context.Context, m listingMember) *listingDocument {
	if d, ok := s.docs[m.c.Key]; ok {
		return d
	}
	d := &listingDocument{}
	s.docs[m.c.Key] = d
	if s.g.sources == nil {
		d.err = kernel.ErrNotFound
		return d
	}
	q := s.q
	q.Repo = m.c
	result, err := s.g.readRegistry(s.r.WithContext(ctx), q, m.cached)
	if err != nil {
		d.err = err
		return d
	}
	d.result = result
	stream, err := s.g.remoteStream(s.r.WithContext(ctx), result)
	if err != nil {
		d.err = err
		return d
	}
	defer stream.Body.Close()
	if s.q.Kind == KindCatalog {
		d.list, err = s.g.remoteCatalogList(stream)
	} else {
		var ok bool
		d.list, ok = s.g.remoteTagList(stream)
		if !ok {
			err = source.ErrInvalidResult
		}
	}
	if err == nil && result.Revalidate != nil {
		err = result.Revalidate(ctx)
	}
	d.err = err
	if err == nil && s.q.Kind == KindTags {
		s.known = true
	}
	return d
}

func (g *Registry) remoteCatalogList(stream *source.Stream) (*sortedTags, error) {
	key := "catalog:" + stream.Checksums.SHA256
	if stream.Checksums.SHA256 != "" {
		if list, ok := g.tagLists.get(key); ok {
			return list, nil
		}
	}
	var doc struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(io.LimitReader(stream.Body, maxRemoteTags)).Decode(&doc); err != nil {
		return nil, source.ErrInvalidResult
	}
	slices.SortFunc(doc.Repositories, layout.Compare)
	list := packTags(slices.Compact(doc.Repositories))
	if stream.Checksums.SHA256 != "" {
		g.tagLists.put(key, list)
	}
	return list, nil
}

// namedStream attaches request-specific policy to a sorted underlying stream.
// Errors remain errors: source authority/storage faults cannot become a short
// successful listing once delivery has begun.
type namedStream struct {
	next func(context.Context) (string, bool, error)
}

func (s namedStream) Next(ctx context.Context) (string, bool, error) { return s.next(ctx) }

func (s *listingWalk) open(ctx context.Context, prefix, last string, batch int) names {
	streams := make([]layout.NameStream, 0, len(s.members))
	for _, m := range s.members {
		if !s.eligible(ctx, m) {
			continue
		}
		if s.q.Kind == KindTags && !m.accepts(s.q.Name, true) {
			continue
		}
		if m.c.Class == repo.Remote {
			streams = append(streams, s.remoteNames(m, prefix, last))
			continue
		}
		if s.q.Kind == KindTags {
			src := layout.TagsAfter(s.g.store.Items().DockerPages(), m.c.Key, s.q.Name, last, batch)
			s.locals[m.c.Key] = src
			streams = append(streams, namedStream{next: func(ctx context.Context) (string, bool, error) {
				for {
					tag, ok, err := src.Next(ctx)
					if err != nil {
						return "", false, fmt.Errorf("registry member tags: %w", err)
					}
					if !ok {
						return "", false, nil
					}
					s.known = true
					path := s.q.Name + "/" + tag + "/" + layout.ManifestFile
					d, _ := accessapi.DeciderFrom(ctx)
					if !m.accepts(path, false) ||
						d != nil && !d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: m.c.Key, Class: string(m.c.Class)}, path, accessapi.ActionRead) {
						continue
					}
					return tag, true, nil
				}
			}})
		} else {
			streams = append(
				streams,
				layout.ImagesAfter(s.g.store.Items().DockerPages(), m.c.Key, prefix, last, func(name string) bool { return s.catalogAccepts(m, name) }, batch),
			)
			if prefix != "" {
				src := streams[len(streams)-1]
				streams[len(streams)-1] = namedStream{next: func(ctx context.Context) (string, bool, error) {
					name, ok, err := src.Next(ctx)
					if err != nil {
						return "", false, fmt.Errorf("registry catalog stream: %w", err)
					}
					return prefix + name, ok, nil
				}}
			}
		}
	}
	return layout.MergeNames(streams...)
}

func (s *listingWalk) eligible(ctx context.Context, m listingMember) bool {
	if !s.g.readsAnything(s.r.WithContext(ctx), m.c) {
		return false
	}
	if s.q.Kind != KindTags {
		return true
	}
	d, _ := accessapi.DeciderFrom(ctx)
	return d == nil || d.AllowsFolder(accessapi.ResourceArtifact, accessapi.Repo{Key: m.c.Key, Class: string(m.c.Class)}, s.q.Name, accessapi.ActionRead)
}

func (s *listingWalk) found(ctx context.Context) (bool, error) {
	if s.known {
		return true, nil
	}
	// n=0 and an exhausted cursor need only one member to prove image existence.
	for _, m := range s.members {
		if !s.eligible(ctx, m) || !m.accepts(s.q.Name, true) {
			continue
		}
		if m.c.Class == repo.Remote {
			d := s.document(ctx, m)
			if d.err == nil && !d.disabled {
				return true, nil
			}
			if d.err != nil && !listingPassedOver(d.err) {
				return false, d.err
			}
			continue
		}
		src, ok := s.locals[m.c.Key]
		if !ok {
			src = layout.TagsAfter(s.g.store.Items().DockerPages(), m.c.Key, s.q.Name, "", 1)
			s.locals[m.c.Key] = src
		}
		found, err := src.Found(ctx)
		if err != nil {
			return false, fmt.Errorf("registry virtual image existence: %w", err)
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func (s *listingWalk) fence(ctx context.Context) (bool, error) {
	changed := false
	for _, d := range s.docs {
		if d.disabled || d.err != nil || d.result.Revalidate == nil {
			continue
		}
		if err := d.result.Revalidate(ctx); err != nil {
			if s.q.Repo.Class == repo.Virtual && listingPassedOver(err) {
				d.disabled = true
				changed = true
				continue
			}
			return false, fmt.Errorf("registry listing source fence: %w", err)
		}
	}
	if changed {
		s.known = false
	}
	return changed, nil
}

func (s *listingWalk) used(ctx context.Context) {
	for _, d := range s.docs {
		if !d.disabled && d.err == nil && d.result.Used != nil {
			_ = d.result.Used(ctx)
		}
	}
}

func (g *Registry) virtualTags(w http.ResponseWriter, r *http.Request, q Request) {
	if err := g.requireVirtual(r, q); err != nil {
		g.writeErr(w, r, err)
		return
	}
	if imageBlackedOut(w, q) {
		return
	}
	members, err := g.listingMembers(r, q.Repo)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	state := &listingWalk{g: g, r: r, q: q, members: members, docs: map[string]*listingDocument{}, locals: map[string]*layout.TagPages{}}
	open := func(ctx context.Context, last string, batch int) names { return state.open(ctx, "", last, batch) }
	for {
		page, err := readPage(r, open)
		if err != nil {
			g.writeErr(w, r, err)
			return
		}
		found, err := state.found(r.Context())
		if err != nil {
			g.writeErr(w, r, err)
			return
		}
		changed, err := state.fence(r.Context())
		if err != nil {
			g.writeErr(w, r, err)
			return
		}
		if changed {
			continue
		}
		if !found {
			Write(w, nameUnknown(q))
			return
		}
		name, _ := json.Marshal(q.addressed())
		g.writePage(w, r, `{"name":`+string(name)+`,"tags":`, page)
		state.used(r.Context())
		return
	}
}

type sortedNames interface {
	len() int
	at(int) string
}

// Seek into the raw immutable project interval rather than copying or walking
// it on every page. Both projection bounds and the client cursor cost log N.
func listingWindow(list sortedNames, project, prefix, last string) (int, int) {
	start, end := 0, list.len()
	projectPrefix := ""
	if project != "" {
		projectPrefix = project + "/"
		start = sort.Search(end, func(i int) bool { return layout.Compare(list.at(i), projectPrefix) >= 0 })
		end = sort.Search(end, func(i int) bool { return layout.Compare(list.at(i), projectPrefix+"~") >= 0 })
	}
	start += sort.Search(end-start, func(i int) bool {
		return layout.Compare(prefix+strings.TrimPrefix(list.at(start+i), projectPrefix), last) > 0
	})
	return start, end
}

func (s *listingWalk) catalogAccepts(m listingMember, name string) bool {
	if s.q.Repo.Class != repo.Virtual && m.c.Class != repo.Remote {
		return ValidName(name) && repo.PatternsOf(m.c).Accepts(name, true)
	}
	return ValidName(name) && m.accepts(name, true)
}

func (g *Registry) listingError(w http.ResponseWriter, r *http.Request, q Request, err error) {
	if q.Repo.Class == repo.Remote {
		g.remoteError(w, r, q, err)
	} else {
		g.writeErr(w, r, err)
	}
}

func listingPassedOver(err error) bool {
	return !errors.Is(err, format.ErrStorageFault) && (virtualPassedOver(err) || retrievalFailed(err))
}

func virtualListing(kind Kind, method string) bool {
	return method == http.MethodGet && (kind == KindTags || kind == KindCatalog)
}

func (g *Registry) unsupportedVirtualOperation(q Request, method string) bool {
	if q.Repo.Class != repo.Virtual || virtualListing(q.Kind, method) || g.uploads != nil && q.Kind == KindReferrers && method == http.MethodGet {
		return false
	}
	return g.uploads == nil || !virtualPull(q.Kind, method) && !virtualWrite(q.Kind, method) && (method != http.MethodDelete || q.Kind != KindManifest && q.Kind != KindBlob)
}

func (s *listingWalk) remoteNames(m listingMember, prefix, last string) names {
	var list *sortedTags
	var d *listingDocument
	i, end := 0, 0
	initialized := false
	return namedStream{next: func(ctx context.Context) (string, bool, error) {
		if !initialized {
			initialized = true
			d = s.document(ctx, m)
			if d.disabled {
				return "", false, nil
			}
			if d.err != nil {
				if s.q.Repo.Class == repo.Virtual && listingPassedOver(d.err) {
					return "", false, nil
				}
				return "", false, d.err
			}
			list = d.list
			project := ""
			if s.q.Kind == KindCatalog {
				project = m.c.DockerProjectID
			}
			i, end = listingWindow(list, project, prefix, last)

		}
		if d.disabled {
			return "", false, nil
		}
		for i < end {
			name := list.at(i)
			i++
			if s.q.Kind == KindCatalog && m.c.DockerProjectID != "" {
				name = strings.TrimPrefix(name, m.c.DockerProjectID+"/")
			}
			if s.q.Kind == KindCatalog {
				if !s.catalogAccepts(m, name) {
					continue
				}
			} else {
				path := s.q.Name + "/" + name + "/" + layout.ManifestFile
				if !m.accepts(path, false) {
					continue
				}
				if d.result.CheckRead != nil {
					if err := d.result.CheckRead(ctx, path); err != nil {
						if listingPassedOver(err) {
							continue
						}
						return "", false, fmt.Errorf("registry tag authority: %w", err)
					}
				}
			}
			return prefix + name, true, nil
		}
		return "", false, nil
	}}
}
