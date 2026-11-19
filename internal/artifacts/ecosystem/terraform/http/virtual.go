package terraformhttp

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type virtualModuleVersion struct {
	member   *http.Request
	item     *model.Item
	location moduleSource
	reads    []source.Result
	path     string
}

func moduleMember(c repo.Config) bool {
	return c.Class == repo.Remote || (c.Class == repo.Local || c.Class == repo.Federated) && c.TerraformType == repo.TerraformModule
}

func modulePassedOver(err error) bool {
	if errors.Is(err, kernel.ErrStore) || errors.Is(err, format.ErrStorageFault) {
		return false
	}
	if pkgroute.PassedOver(err) {
		return true
	}
	var upstream *source.HTTPError
	if errors.As(err, &upstream) && (upstream.Status == 401 || upstream.Status == 403) {
		return true
	}
	var answer *apierror.Error
	return errors.As(err, &answer) && answer.NoChallenge && (answer.Status == 401 || answer.Status == 403)
}

func (h handler) virtualModule(w http.ResponseWriter, r *http.Request) {
	q := pkgroute.RequestFrom(r)
	// Enabling module reads must not admit deferred provider/mirror operations.
	if !strings.HasPrefix(q.Rest, "v1/modules/") {
		h.k.Error(w, r, apierror.New(501, fmt.Sprintf("'%s' is a virtual repository, which artifactweave does not serve through the terraform API yet", q.Repo.Key)))
		return
	}
	if r.URL.Query().Has("sig") {
		h.virtualSignedModule(w, r)
		return
	}
	op, ok := parse(q.Rest)
	if !ok {
		h.k.Error(w, r, missing())
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.k.Error(w, r, apierror.New(405, "method not allowed"))
		return
	}
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.virtualModuleError(w, r, err)
		return
	}
	parts := []virtual.Part[[]tf.ModuleVersion[virtualModuleVersion]]{}
	for _, mr := range members {
		if !moduleMember(pkgroute.RequestFrom(mr).Repo) {
			continue
		}
		vs, e := h.modulePart(r, mr, op)
		if e != nil {
			if modulePassedOver(e) {
				continue
			}
			h.k.Error(w, r, e)
			return
		}
		if !op.isList {
			for _, v := range vs {
				public, err := h.virtualModuleURL(r, op.module, v.Value)
				if err != nil {
					if modulePassedOver(err) {
						continue
					}
					h.k.Error(w, r, err)
					return
				}
				w.Header().Set("X-Terraform-Get", public)
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			continue
		}
		parts = append(parts, virtual.Part[[]tf.ModuleVersion[virtualModuleVersion]]{Member: pkgroute.RequestFrom(mr).Repo, Doc: vs})
	}
	if op.isList {
		h.virtualModuleVersions(w, r, parts)
		return
	}
	h.k.Error(w, r, missing())
}

func (h handler) virtualModuleVersions(w http.ResponseWriter, r *http.Request, parts []virtual.Part[[]tf.ModuleVersion[virtualModuleVersion]]) {
	// A retained contribution can disappear at its final authority fence. Remove
	// only failed versions and merge again, so a later eligible owner still wins.
	for {
		versions, err := (tf.ModuleMerger[virtualModuleVersion]{}).Merge(r.Context(), parts)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		out := make([]map[string]string, 0, len(versions))
		retry := false
		for _, v := range versions {
			if err := h.virtualVersionFence(r, v.Value); err != nil {
				if !modulePassedOver(err) {
					h.k.Error(w, r, err)
					return
				}
				owner := pkgroute.RequestFrom(v.Value.member).Repo.Key
				for i := range parts {
					if parts[i].Member.Key == owner {
						parts[i].Doc = slices.DeleteFunc(parts[i].Doc, func(candidate tf.ModuleVersion[virtualModuleVersion]) bool { return candidate.Version == v.Version })
					}
				}
				retry = true
				continue
			}
			out = append(out, map[string]string{"version": v.Version})
		}
		if retry {
			continue
		}
		if len(out) == 0 {
			h.k.Error(w, r, missing())
			return
		}
		writeJSON(w, r, map[string]any{"modules": []any{map[string]any{"versions": out}}})
		return
	}
}

func (h handler) virtualModuleError(w http.ResponseWriter, r *http.Request, err error) {
	if modulePassedOver(err) {
		err = missing()
	}
	h.k.Error(w, r, err)
}

// Membership is checked at each archive path, so nested virtual patterns do
// not prune a whole module before its visible versions have been examined.
func (h handler) virtualAllowed(r, mr *http.Request, path string) (bool, error) {
	if !h.k.Can(mr, path, accessapi.ActionRead) {
		return false, nil
	}
	if err := generic.CheckDownload(pkgroute.RequestFrom(mr).Repo, path, false); err != nil {
		return false, nil
	}
	members, err := h.k.DownloadMembers(r, path)
	if err != nil {
		if modulePassedOver(err) {
			return false, nil
		}
		return false, fmt.Errorf("terraform virtual archive members: %w", err)
	}
	for _, member := range members {
		if pkgroute.RequestFrom(member).Repo.Key == pkgroute.RequestFrom(mr).Repo.Key {
			return true, nil
		}
	}
	return false, nil
}

func (h handler) modulePart(r, mr *http.Request, op operation) ([]tf.ModuleVersion[virtualModuleVersion], error) {
	c := pkgroute.RequestFrom(mr).Repo
	out := []tf.ModuleVersion[virtualModuleVersion]{}
	if c.Class != repo.Remote {
		vs, err := h.versions(mr, op.module.Package())
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if !op.isList && v.version != op.module.Version {
				continue
			}
			allowed, err := h.virtualAllowed(r, mr, v.item.FullPath())
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			it := v.item
			out = append(out, tf.ModuleVersion[virtualModuleVersion]{Version: v.version, Value: virtualModuleVersion{member: mr, item: &it, path: it.FullPath()}})
		}
		return out, nil
	}
	if !h.k.CanFolder(mr, op.module.Package(), accessapi.ActionRead) {
		return nil, kernel.ErrNotFound
	}
	document := ".terraform/modules/" + op.module.Package() + "/versions"
	if !op.isList {
		document = ".terraform/modules/" + op.module.Package() + "/" + op.module.Version + "/download"
	}
	// The real remote applies its metadata policy; outer virtuals filter the
	// disclosed archive paths rather than this hidden metadata implementation path.
	if err := generic.CheckDownload(c, document, false); err != nil {
		return nil, fmt.Errorf("terraform member metadata policy: %w", err)
	}
	versions, result, err := h.upstreamModuleVersions(mr, op.module)
	if err != nil {
		return nil, err
	}
	if op.isList && len(versions) > moduleDiscoveryLimit {
		return nil, remoteModuleError()
	}
	for _, v := range versions {
		if !op.isList && v != op.module.Version {
			continue
		}
		m := op.module
		m.Version = v
		location, metadata, e := h.upstreamModuleLocation(mr, m)
		if e != nil {
			if modulePassedOver(e) {
				continue
			}
			return nil, e
		}
		if !h.k.Can(mr, location.listingPath(m), accessapi.ActionRead) {
			continue
		}
		allowed, e := h.virtualAllowed(r, mr, location.Path)
		if e != nil {
			return nil, e
		}
		if !allowed {
			continue
		}
		out = append(
			out,
			tf.ModuleVersion[virtualModuleVersion]{
				Version: v,
				Value:   virtualModuleVersion{member: mr, path: location.Path, location: location, reads: []source.Result{result, metadata}},
			},
		)
	}
	return out, nil
}

func (h handler) virtualVersionFence(r *http.Request, v virtualModuleVersion) error {
	allowed, err := h.virtualAllowed(r, v.member, v.path)
	if err != nil {
		return err
	}
	if !allowed {
		return kernel.ErrNotFound
	}
	if err := combineModuleFences(v.reads, nil, nil)(r.Context()); err != nil {
		return err
	}
	p, _ := kernel.FromContext(r.Context())
	err = h.virtualClaimFence(r, claims{Subject: p.Name, Repo: pkgroute.RequestFrom(r).Repo.Key, Member: pkgroute.RequestFrom(v.member).Repo.Key, Path: v.path})
	if errors.Is(err, forbidden) {
		return kernel.ErrNotFound
	}
	return err
}

func (h handler) virtualModuleURL(r *http.Request, m tf.Module, v virtualModuleVersion) (string, error) {
	if err := h.virtualVersionFence(r, v); err != nil {
		return "", err
	}
	p, _ := kernel.FromContext(r.Context())
	now := h.o.Now()
	c := claims{
		Subject: p.Name,
		Repo:    pkgroute.RequestFrom(r).Repo.Key,
		Member:  pkgroute.RequestFrom(v.member).Repo.Key,
		Path:    v.path,
		Issued:  now.Unix(),
		Expires: now.Add(90 * time.Second).Unix(),
	}
	if v.item != nil {
		found, err := h.k.ResolveDownloadItem(v.member, v.path)
		if err != nil {
			return "", fmt.Errorf("terraform hosted archive: %w", err)
		}
		if found.Item.Folder {
			return "", kernel.ErrNotFound
		}
		c.SHA256 = found.Item.Actual.SHA256
		if err := h.virtualClaimFence(r, c); err != nil {
			return "", err
		}
		return h.signDownloadURL(r, c)
	}
	resource := moduleArtifactResource(v.member, v.location)
	completeModuleResource(&resource)
	artifact, err := h.k.Read(v.member, resource)
	if err != nil {
		return "", fmt.Errorf("terraform member archive: %w", err)
	}
	defer closeModuleResult(artifact)
	c.SHA256 = moduleDigest(artifact)
	if c.SHA256 == "" {
		return "", remoteModuleError()
	}
	reference, err := p.DownloadReference()
	if err != nil {
		return "", fmt.Errorf("terraform download identity: %w", err)
	}
	c.Credential = &reference
	c.Source = &artifact.Identity
	c.Module = m.Package() + "/" + m.Version
	c.ModuleSource = h.moduleSourceDigest(v.location)
	selected, err := h.moduleDirectory(v.member, artifact, v.location)
	if err != nil {
		return "", err
	}
	c.Rewrite = moduleRewriteTarget(r)
	representation, err := h.rewriteModule(v.member, artifact, v.location, c.Rewrite)
	if err != nil {
		return "", err
	}
	defer closeModuleResult(representation.result)
	c.DeliveredSHA256 = representation.digest
	c.Archive = representation.archive
	if err := combineModuleFences(append(v.reads, artifact), readCheck, within(artifact, v.path))(r.Context()); err != nil {
		return "", err
	}
	if err := h.virtualVersionFence(r, v); err != nil {
		return "", err
	}
	if err := h.virtualClaimFence(r, c); err != nil {
		if errors.Is(err, forbidden) {
			return "", kernel.ErrNotFound
		}
		return "", err
	}
	return h.moduleDownloadURL(r, c, selected, representation.archive)
}

// Signed virtual URLs restore the signer before binding a member request. The
// current effective walk, including nested virtual patterns, is authoritative.
func (h handler) virtualSignedModule(w http.ResponseWriter, r *http.Request) {
	c, valid := h.verify(r.URL.Query().Get("sig"))
	q := pkgroute.RequestFrom(r)
	path, ok := strings.CutPrefix(q.Rest, "v1/modules/")
	if !valid || c.Repo != q.Repo.Key || c.Member == "" || !ok || path != c.Path || c.Provider != nil {
		h.k.Error(w, r, forbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.k.Error(w, r, apierror.New(405, "method not allowed"))
		return
	}
	p, d, err := h.principal(r, c.Subject)
	if c.Credential != nil {
		if h.o.Authenticator == nil {
			h.k.Error(w, r, forbidden)
			return
		}
		p, err = h.o.Authenticator.RestoreDownload(r.Context(), c.Subject, *c.Credential)
		if err == nil {
			p, d, err = h.o.Authorizer.Authorize(r.Context(), p, p.Anonymous)
		}
	}
	if err != nil {
		h.k.Error(w, r, signedAuthorityError(err))
		return
	}
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	members, err := h.k.DownloadMembers(r, c.Path)
	if err != nil {
		h.virtualSignedError(w, r, err)
		return
	}
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Key != c.Member || !moduleMember(pkgroute.RequestFrom(mr).Repo) {
			continue
		}
		if !h.k.Can(mr, c.Path, accessapi.ActionRead) {
			h.k.Error(w, r, forbidden)
			return
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			h.remoteSignedModule(w, mr, c)
			return
		}
		// A remote capability remains bound to that source incarnation even if
		// legacy state replaces the member with hosted identical bytes.
		if c.Source != nil || c.Credential != nil || c.Module != "" || c.ModuleSource != "" {
			h.k.Error(w, r, forbidden)
			return
		}
		found, e := h.k.ResolveDownloadItem(mr, c.Path)
		if e != nil {
			h.virtualSignedError(w, r, e)
			return
		}
		if found.Item.Folder || found.Item.Actual.SHA256 != c.SHA256 {
			h.k.Error(w, r, forbidden)
			return
		}
		if err := h.virtualClaimFence(r, c); err != nil {
			h.k.Error(w, r, err)
			return
		}
		h.k.ServeFound(w, mr, found)
		return
	}
	h.k.Error(w, r, forbidden)
}

// Re-read configurations and principal at the last delivery boundary. In
// particular a signed URL cannot retain a removed nested membership edge.
func (h handler) virtualClaimFence(r *http.Request, c claims) error {
	v, err := h.k.DB().Repos().Get(r.Context(), c.Repo)
	if errors.Is(err, kernel.ErrNotFound) {
		return forbidden
	}
	if err != nil {
		return fmt.Errorf("terraform current virtual: %w", err)
	}
	if v.Class != repo.Virtual || generic.CheckDownload(v, c.Path, false) != nil {
		return forbidden
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), v, virtual.Options{Order: virtual.Package, Path: c.Path})
	if err != nil {
		return fmt.Errorf("terraform current members: %w", err)
	}
	_, d, err := h.principal(r, c.Subject)
	if err != nil {
		return signedAuthorityError(err)
	}
	for _, m := range members {
		if m.Repo.Key != c.Member || (c.Provider == nil && !moduleMember(m.Repo)) || (c.Provider != nil && !providerMember(m.Repo)) {
			continue
		}
		if generic.CheckDownload(m.Repo, c.Path, false) != nil {
			return forbidden
		}
		if !d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: m.Repo.Key, Class: string(m.Repo.Class)}, c.Path, accessapi.ActionRead) {
			return forbidden
		}
		return nil
	}
	return forbidden
}

// A missing or refused capability is invalid, but a failed storage read is
// still a server fault, just as on a directly addressed signed module URL.
func (h handler) virtualSignedError(w http.ResponseWriter, r *http.Request, err error) {
	if signedDeliveryRefusal(err) {
		err = forbidden
	}
	h.k.Error(w, r, err)
}
