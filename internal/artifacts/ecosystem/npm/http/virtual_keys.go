package npmhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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

type virtualKeysPart struct {
	member *http.Request
	rows   []json.RawMessage
	read   source.Result
}

// A virtual is not itself a permission target. Resolve exact-file Read from
// its real members without reading a keypair or contacting an upstream.
func (h handler) virtualKeysRead(w http.ResponseWriter, r *http.Request) bool {
	if h.k.Can(r, keysDocumentPath, api.ActionRead) {
		return true
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), pkgroute.RequestFrom(r).Repo, virtual.Options{Order: virtual.Stored})
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("npm keys permission members: %w", err))
		return false
	}
	for _, member := range members {
		mr, err := h.k.MemberRequest(r, member.Repo)
		if err == nil && h.k.Can(mr, keysDocumentPath, api.ActionRead) {
			return true
		}
	}
	return h.k.Require(w, r, keysDocumentPath, api.ActionRead)
}

func (h handler) virtualKeys(w http.ResponseWriter, r *http.Request) {
	if !h.virtualKeysRead(w, r) {
		return
	}
	members, err := h.k.MergeMembers(r, keysDocumentPath)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	parts := []virtualKeysPart{}
	for _, mr := range members {
		if !h.k.Can(mr, keysDocumentPath, api.ActionRead) {
			continue
		}
		part, e := h.virtualKeysPart(mr)
		if e != nil {
			if keysMemberMiss(e) {
				continue
			}
			h.k.Error(w, r, e)
			return
		}
		parts = append(parts, part)
	}
	// Re-read membership/patterns before disclosure. Remote delivery fences also
	// retain current credentials, policy, source identity and transport revision.
	current, err := h.k.DB().Repos().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if current.Class != repo.Virtual || current.PackageType != repo.NPM {
		h.k.Error(w, r, notFound(r))
		return
	}
	fresh := h.k.Bind(r, current)
	if !h.virtualKeysRead(w, fresh) {
		return
	}
	members, err = h.k.MergeMembers(fresh, keysDocumentPath)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	live := map[string]*http.Request{}
	for _, mr := range members {
		live[pkgroute.RequestFrom(mr).Repo.Key] = mr
	}
	retained := []virtual.Part[[]json.RawMessage]{}
	used := []source.Result{}
	for _, part := range parts {
		before := pkgroute.RequestFrom(part.member).Repo
		mr := live[before.Key]
		if mr == nil || pkgroute.RequestFrom(mr).Repo.Class != before.Class || !h.k.Can(mr, keysDocumentPath, api.ActionRead) {
			continue
		}
		if before.Class == repo.Remote {
			if part.read.Revalidate != nil {
				if err := part.read.Revalidate(r.Context()); err != nil {
					if keysMemberMiss(err) {
						continue
					}
					h.k.Error(w, r, pkgroute.SourceError(err, false))
					return
				}
			}
		} else {
			// Local configuration is live; a key rotation while another member reads
			// must not retain the old configured public-key list.
			part, err = h.virtualKeysPart(mr)
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
		}
		retained = append(retained, virtual.Part[[]json.RawMessage]{Member: before, Doc: part.rows})
		if before.Class == repo.Remote {
			used = append(used, part.read)
		}
	}
	keys, err := (npmfmt.KeysMerger{}).Merge(r.Context(), retained)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(keys) == 0 {
		h.k.Error(w, r, notFound(r))
		return
	}
	writeJSON(w, http.StatusOK, jsonType, struct {
		Keys []json.RawMessage `json:"keys"`
	}{keys})
	if r.Method == http.MethodGet {
		for _, result := range used {
			if result.Used != nil {
				_ = result.Used(r.Context())
			}
		}
	}
}

func (h handler) virtualKeysPart(mr *http.Request) (virtualKeysPart, error) {
	part := virtualKeysPart{member: mr}
	c := pkgroute.RequestFrom(mr).Repo
	if c.Class != repo.Remote {
		keys, err := npmfmt.Keys(mr.Context(), h.keyPairs, c)
		if err != nil {
			return part, fmt.Errorf("npm virtual member keys: %w", err)
		}
		for _, key := range keys {
			part.rows = append(part.rows, encode(key))
		}
		return part, nil
	}
	result, err := h.k.Read(mr, remoteKeysResource())
	if err != nil {
		return part, fmt.Errorf("npm virtual remote keys: %w", err)
	}
	doc, err := readRemoteDocument(mr.Context(), result)
	result.Stream = nil
	if err != nil {
		return part, err
	}
	if json.Unmarshal(doc["keys"], &part.rows) != nil || part.rows == nil {
		return part, remoteProtocolError()
	}
	part.read = result
	return part, nil
}

// Keys differ from package discovery: an unsigned upstream 404 contributes no
// keys, but a genuine authentication, rate-limit, outage or transport failure
// must retain its source answer. npm suppresses 400 too; do not rewrite it.
func keysMemberMiss(err error) bool {
	if err == nil || errors.Is(err, kernel.ErrStore) || errors.Is(err, format.ErrStorageFault) {
		return false
	}
	var upstream *source.HTTPError
	if errors.As(err, &upstream) {
		return upstream.Status == http.StatusNotFound
	}
	var answer *apierror.Error
	if errors.As(err, &answer) {
		return answer.Status == http.StatusNotFound || !answer.NoChallenge && (answer.Status == http.StatusUnauthorized || answer.Status == http.StatusForbidden)
	}
	return errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrForbidden) || errors.Is(err, kernel.ErrUnauthenticated) || errors.Is(err, source.ErrDenied) ||
		errors.Is(err, source.ErrAssumedOffline) ||
		pkgroute.IsRefusal(err)
}
