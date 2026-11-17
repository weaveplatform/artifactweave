package npmhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type virtualSource struct{}

func (virtualSource) Item(context.Context, pkgroute.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

// Preserve the old class refusal before deferred operations validate a body,
// path, query or credentials. Enabling reads must not enable local write/auth
// behavior on virtual repositories before NPM-02/03/04.
func (h handler) virtualRouteGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := pkgroute.RequestFrom(r)
		if q.Repo.Class != repo.Virtual {
			next.ServeHTTP(w, r)
			return
		}
		allowed := r.Method == http.MethodGet || r.Method == http.MethodHead
		p, err := pkgroute.ParsePath(q.Rest)
		if err != nil || len(p.Segments) == 0 {
			allowed = false
		}
		if allowed && p.Segments[0] == "-" {
			allowed = false
			if r.Method == http.MethodGet && len(p.Segments) >= 4 && p.Segments[1] == "package" {
				_, tag, tagErr := tagPath(r)
				allowed = tagErr == nil && tag == ""
			}
		}
		if !allowed {
			h.k.Error(
				w,
				r,
				apierror.New(http.StatusNotImplemented, fmt.Sprintf("'%s' is a virtual repository, which artifactweave does not serve through the npm API yet", q.Repo.Key)),
			)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h handler) virtualRead(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	name, rest, ok := splitName(p.Segments)
	switch {
	case !ok:
		h.k.Error(w, r, notFound(r))
	case slices.Contains(rest, "-"):
		h.virtualTarball(w, r, name, p.String())
	case len(rest) == 0:
		h.virtualPackage(w, r, name, "", false)
	case len(rest) == 1:
		h.virtualPackage(w, r, name, rest[0], false)
	default:
		h.k.Error(w, r, notFound(r))
	}
}

// A virtual is not a permission target. Its package-folder gate derives Read
// from real members before any document policy or existence lookup, matching
// the local loadDocument precondition without widening per-version visibility.
func (h handler) virtualFolder(w http.ResponseWriter, r *http.Request, name string) bool {
	if h.k.CanFolder(r, name, api.ActionRead) {
		return true
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), pkgroute.RequestFrom(r).Repo, virtual.Options{Order: virtual.Stored})
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("npm virtual permission members: %w", err))
		return false
	}
	for _, member := range members {
		mr, err := h.k.MemberRequest(r, member.Repo)
		if err != nil {
			continue
		}
		if h.k.CanFolder(mr, name, api.ActionRead) {
			return true
		}
	}
	return h.k.RequireFolder(w, r, name, api.ActionRead)
}

func (h handler) virtualPackage(w http.ResponseWriter, r *http.Request, name, spec string, tagsOnly bool) {
	if !h.virtualFolder(w, r, name) {
		return
	}
	model, err := h.virtualModel(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if tagsOnly {
		writeJSON(w, http.StatusOK, jsonType, model.Tags)
		return
	}
	if spec != "" {
		if tagged, ok := model.Tags[spec]; ok {
			spec = tagged
		}
		if _, ok := model.Versions[spec]; !ok {
			if clean, valid := npmfmt.CleanVersion(spec); valid {
				spec = clean
			}
		}
		v, ok := model.Versions[spec]
		if !ok {
			h.k.Error(w, r, apierror.New(http.StatusNotFound, fmt.Sprintf("version not found: %s@%s", name, spec)))
			return
		}
		doc, err := virtualVersion(name, spec, v, tarballBase(r), false)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, jsonType, doc)
		return
	}
	doc, err := renderVirtual(model, tarballBase(r), abbreviated(r))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	contentType := jsonType
	if abbreviated(r) {
		contentType = abbreviatedType
	}
	writeConditional(w, r, contentType, doc)
}

func (h handler) virtualModel(r *http.Request, name string) (npmfmt.VirtualPackage, error) {
	entry, err := h.k.Merged(r, documentPath(name), documentPath(name), func(r *http.Request) (virtual.Entry, error) {
		return h.mergeVirtual(r, name)
	})
	if err != nil {
		return npmfmt.VirtualPackage{}, fmt.Errorf("npm virtual model: %w", err)
	}
	var model npmfmt.VirtualPackage
	if err := json.Unmarshal(entry.Body, &model); err != nil {
		return model, fmt.Errorf("npm virtual cached model: %w", err)
	}
	return model, nil
}

func (h handler) mergeVirtual(r *http.Request, name string) (virtual.Entry, error) {
	members, err := h.k.MergeMembers(r, documentPath(name))
	if err != nil {
		return virtual.Entry{}, fmt.Errorf("npm virtual members: %w", err)
	}
	var parts []virtual.Part[npmfmt.VirtualPackage]
	priority := false
	for _, mr := range members {
		c := pkgroute.RequestFrom(mr).Repo
		if priority && !c.PriorityResolution.Bool() {
			continue
		}
		doc, err := h.virtualMember(mr, name)
		if err != nil {
			if virtualPassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			return virtual.Entry{}, err
		}
		parts = append(parts, virtual.Part[npmfmt.VirtualPackage]{Member: c, Doc: doc})
		priority = priority || c.PriorityResolution.Bool()
	}
	if len(parts) == 0 {
		return virtual.Entry{}, virtualNotFound(name)
	}
	merged, err := (npmfmt.Merger{Settings: h.format}).Merge(r.Context(), parts)
	if err != nil {
		return virtual.Entry{}, fmt.Errorf("npm virtual package merge: %w", err)
	}
	return virtual.Entry{Body: encode(merged)}, nil
}

func (h handler) virtualMember(r *http.Request, name string) (npmfmt.VirtualPackage, error) {
	if !h.k.CanFolder(r, name, api.ActionRead) {
		return npmfmt.VirtualPackage{}, kernel.ErrNotFound
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		doc, result, err := h.upstreamPackage(r, name, jsonType, "")
		if err != nil {
			return npmfmt.VirtualPackage{}, err
		}
		versions, err := h.rewriteVersions(r, name, doc)
		if err != nil {
			return npmfmt.VirtualPackage{}, err
		}
		paths := make([]string, 0, len(versions))
		for v := range versions {
			paths = append(paths, npmfmt.CanonicalPath(name, v))
		}
		fenceDisclosures(&result, paths)
		if result.Revalidate != nil {
			if err := result.Revalidate(r.Context()); err != nil {
				return npmfmt.VirtualPackage{}, fmt.Errorf("npm virtual source authority: %w", err)
			}
		}
		return virtualDocument(name, doc)
	}
	p, found, err := h.readable(r, name)
	if err != nil {
		return npmfmt.VirtualPackage{}, err
	}
	if !found {
		return npmfmt.VirtualPackage{}, kernel.ErrNotFound
	}
	full, err := p.full("")
	if err != nil {
		return npmfmt.VirtualPackage{}, err
	}
	var doc remoteDocument
	if err := json.Unmarshal(encode(full), &doc); err != nil {
		return npmfmt.VirtualPackage{}, fmt.Errorf("npm local virtual model: %w", err)
	}
	model, err := virtualDocument(name, doc)
	for _, v := range p.versions {
		version := model.Versions[v.number]
		version.Published = v.meta.Published
		version.HasInstallScript = v.meta.HasInstallScript
		model.Versions[v.number] = version
	}
	return model, err
}

func virtualDocument(name string, doc remoteDocument) (npmfmt.VirtualPackage, error) {
	out := npmfmt.VirtualPackage{Name: name, Fields: doc, Versions: map[string]npmfmt.VirtualVersion{}}
	var versions map[string]remoteDocument
	if json.Unmarshal(doc["versions"], &versions) != nil || versions == nil {
		return out, remoteProtocolError()
	}
	if json.Unmarshal(doc["dist-tags"], &out.Tags) != nil {
		return out, remoteProtocolError()
	}
	_ = json.Unmarshal(doc["time"], &out.Times)
	for number, fields := range versions {
		var dist remoteDocument
		if json.Unmarshal(fields["dist"], &dist) != nil || dist == nil {
			return out, remoteProtocolError()
		}
		dist["tarball"] = jsonString(npmfmt.CanonicalPath(name, number))
		fields["dist"] = encode(dist)
		var published time.Time
		var text string
		if json.Unmarshal(out.Times[number], &text) == nil {
			published, _ = time.Parse(time.RFC3339Nano, text)
		}
		out.Versions[number] = npmfmt.VirtualVersion{Manifest: encode(fields), Published: published}
	}
	if len(out.Versions) == 0 {
		return out, kernel.ErrNotFound
	}
	return out, nil
}

func virtualNotFound(name string) error {
	return apierror.New(http.StatusNotFound, "package not found: "+name)
}

// Raw JSON is decoded anew on every render: cached manifests and raw types stay
// unchanged when Accept, ?write=true or the request's base URL changes.
func virtualVersion(name, number string, v npmfmt.VirtualVersion, base string, short bool) (remoteDocument, error) {
	var full remoteDocument
	if err := json.Unmarshal(v.Manifest, &full); err != nil {
		return nil, fmt.Errorf("npm virtual version: %w", err)
	}
	fields := full
	if short {
		fields = remoteDocument{}
		for _, key := range append(slices.Clone(abbreviatedFields), "deprecated", "hasInstallScript") {
			if value, ok := full[key]; ok {
				fields[key] = value
			}
		}
	} else {
		fields["_id"] = jsonString(name + "@" + number)
	}
	if short && v.HasInstallScript {
		fields["hasInstallScript"] = json.RawMessage("true")
	}
	var dist remoteDocument
	if json.Unmarshal(full["dist"], &dist) != nil || dist == nil {
		return nil, remoteProtocolError()
	}
	dist["tarball"] = jsonString(base + "/" + escapePath(npmfmt.CanonicalPath(name, number)))
	fields["version"], fields["dist"] = jsonString(number), encode(dist)
	return fields, nil
}

func renderVirtual(model npmfmt.VirtualPackage, base string, short bool) (remoteDocument, error) {
	out := remoteDocument{}
	if !short {
		for key, value := range model.Fields {
			out[key] = value
		}
		out["_id"], out["_rev"] = jsonString(model.Name), jsonString(rev)
		out["time"] = encode(model.Times)
	}
	out["name"], out["dist-tags"] = jsonString(model.Name), encode(model.Tags)
	versions := map[string]remoteDocument{}
	for number, v := range model.Versions {
		fields, err := virtualVersion(model.Name, number, v, base, short)
		if err != nil {
			return nil, err
		}
		versions[number] = fields
	}
	out["versions"] = encode(versions)
	if short {
		out["modified"] = model.Times["modified"]
		if len(out["modified"]) == 0 {
			out["modified"] = jsonString("")
		}
	}
	return out, nil
}

// An upstream's credentials belong to its remote repository. Their refusal
// cannot challenge this caller or hide a readable later member. Storage faults
// retain their server-error identity even if they wrap an upstream response.
func virtualPassedOver(err error) bool {
	if errors.Is(err, format.ErrStorageFault) {
		return false
	}
	var answer *apierror.Error
	var upstream *source.HTTPError
	return pkgroute.PassedOver(err) ||
		errors.As(err, &upstream) && (upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden) ||
		errors.As(err, &answer) && answer.NoChallenge && (answer.Status == http.StatusUnauthorized || answer.Status == http.StatusForbidden)
}
