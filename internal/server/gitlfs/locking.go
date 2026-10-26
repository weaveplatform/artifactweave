package gitlfs

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxLockRequest = 16 << 10

type lockRef struct {
	Name string `json:"name"`
}

type lockWire struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	LockedAt string `json:"locked_at"`
	Owner    struct {
		Name string `json:"name"`
	} `json:"owner"`
}

func lockDocument(v types.GitLFSLock) lockWire {
	d := lockWire{ID: v.ID, Path: v.Path, LockedAt: v.LockedAt.UTC().Format(time.RFC3339)}
	d.Owner.Name = v.Owner
	return d
}

func lockResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func lockError(w http.ResponseWriter, status int, message string) {
	lockResponse(w, status, struct {
		Message string `json:"message"`
	}{message})
}

func lockHeaders(r *http.Request) bool {
	if !strings.Contains(r.Header.Get("Accept"), mediaType) {
		return false
	}
	// The pinned git-lfs client omits Content-Type on GET /locks, despite the
	// locking specification asking for it on every request. GET has no body.
	return r.Method == http.MethodGet && r.Header.Get("Content-Type") == "" ||
		validMediaType(r.Header.Get("Content-Type"))
}

func lockPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, err := auth.RequireAuthenticated(r.Context())
	if err != nil {
		lockError(w, http.StatusUnauthorized, "authentication required")
		return auth.Principal{}, false
	}
	return p, true
}

func validLockPath(path string) bool {
	if path == "" || len(path) > 4096 || path != strings.Trim(path, "/") ||
		strings.ContainsAny(path, "\r\n\x00") {
		return false
	}
	_, _, _, err := types.SplitItemPath(path)
	return err == nil
}

func validLockRef(ref *lockRef) bool {
	if ref == nil {
		return true
	}
	s := ref.Name
	return len(s) <= 512 && strings.HasPrefix(s, "refs/") &&
		!strings.ContainsAny(s, "\\\x00\r\n ~^:?*[]") &&
		!strings.Contains(s, "..") && !strings.Contains(s, "//") &&
		!strings.Contains(s, "@{") && !strings.HasSuffix(s, "/") &&
		!strings.HasSuffix(s, ".lock")
}

func decodeLockBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxLockRequest)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock request")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock request")
		return false
	}
	return true
}

func (h handler) lockTarget(r *http.Request, path string) (*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		return nil, errors.New("remote Git LFS repository is read-only")
	}
	// Virtual repositories cannot be permission targets. Their deployment
	// member supplies write authority; read members supply list authority.
	if c.Class != repo.Virtual && !h.k.Can(r, path, types.ActionWrite) {
		return nil, auth.ErrForbidden
	}
	if c.Class != repo.Virtual {
		return r, nil
	}
	if c.DefaultDeploymentRepo == "" || !slices.Contains(c.Repositories, c.DefaultDeploymentRepo) {
		return nil, store.ErrNotFound
	}
	members, err := h.members(r)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		mc := pkgroute.RequestFrom(member).Repo
		if mc.Key == c.DefaultDeploymentRepo &&
			(mc.Class == repo.Local || mc.Class == repo.Federated) {
			if !h.k.Can(member, path, types.ActionWrite) {
				return nil, auth.ErrForbidden
			}
			return member, nil
		}
	}
	return nil, store.ErrNotFound
}

func lockTargetStatus(err error) int {
	if errors.Is(err, auth.ErrForbidden) {
		return http.StatusForbidden
	}
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusMethodNotAllowed
	}
	return http.StatusMethodNotAllowed
}

func (h handler) createLock(w http.ResponseWriter, r *http.Request) {
	if !lockHeaders(r) || r.URL.RawQuery != "" {
		lockError(w, http.StatusUnsupportedMediaType, "invalid Git LFS lock request headers")
		return
	}
	p, ok := lockPrincipal(w, r)
	if !ok {
		return
	}
	var request struct {
		Path string   `json:"path"`
		Ref  *lockRef `json:"ref"`
	}
	if !decodeLockBody(w, r, &request) {
		return
	}
	if !validLockPath(request.Path) || !validLockRef(request.Ref) {
		lockError(w, http.StatusUnprocessableEntity, "invalid Git LFS lock path or ref")
		return
	}
	target, err := h.lockTarget(r, request.Path)
	if err != nil {
		lockError(w, lockTargetStatus(err), "Git LFS lock create denied")
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		members, err := h.members(r)
		if err != nil {
			lockReadError(w, err)
			return
		}
		for _, member := range members {
			if pkgroute.RequestFrom(member).Repo.Key == pkgroute.RequestFrom(target).Repo.Key ||
				!h.k.Can(member, request.Path, types.ActionRead) {
				continue
			}
			rows, _, readErr := h.readLockPage(member, lockFilter{Path: request.Path}, 1, "", false)
			if readErr != nil {
				lockReadError(w, readErr)
				return
			}
			if len(rows) != 0 {
				if h.k.Can(member, request.Path, types.ActionRead) {
					lockResponse(w, http.StatusConflict, map[string]any{
						"message": "Git LFS path is already locked", "lock": lockDocument(rows[0]),
					})
				} else {
					lockError(w, http.StatusConflict, "Git LFS path is already locked")
				}
				return
			}
		}
	}
	v := types.GitLFSLock{
		ID: rand.Text(), RepoKey: pkgroute.RequestFrom(target).Repo.Key,
		Path: request.Path, Owner: p.Name, LockedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err = h.k.DB().GitLFSLocks().Create(r.Context(), v); err != nil {
		if errors.Is(err, store.ErrConflict) {
			if h.k.Can(target, v.Path, types.ActionRead) {
				rows, _, listErr := h.k.DB().GitLFSLocks().List(r.Context(), store.GitLFSLockFilter{
					RepoKey: v.RepoKey, Path: v.Path,
				}, store.Page{Limit: 1})
				if listErr == nil && len(rows) == 1 {
					lockResponse(w, http.StatusConflict, map[string]any{
						"message": "Git LFS path is already locked", "lock": lockDocument(rows[0]),
					})
					return
				}
			}
			lockError(w, http.StatusConflict, "Git LFS path is already locked")
			return
		}
		lockError(w, http.StatusInternalServerError, "Git LFS lock could not be created")
		return
	}
	lockResponse(w, http.StatusCreated, struct {
		Lock lockWire `json:"lock"`
	}{lockDocument(v)})
}

type lockFilter struct {
	Path string
	ID   string
}

type lockCursor struct {
	Repo   string `json:"repo"`
	Mode   string `json:"mode"`
	Path   string `json:"path"`
	ID     string `json:"id"`
	Member int    `json:"member"`
	After  string `json:"after"`
}

func parseLockCursor(raw, repoKey, mode string, filter lockFilter, maxMember int) (lockCursor, error) {
	c := lockCursor{Repo: repoKey, Mode: mode, Path: filter.Path, ID: filter.ID}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 4096 {
		return c, store.ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil ||
		c.Repo != repoKey || c.Mode != mode || c.Path != filter.Path || c.ID != filter.ID ||
		c.Member < 0 || c.Member >= maxMember {
		return c, store.ErrInvalidCursor
	}
	if len(c.After) > 2048 {
		return c, store.ErrInvalidCursor
	}
	return c, nil
}

func encodeLockCursor(c lockCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

func lockLimit(raw string) (int, error) {
	if raw == "" {
		return store.DefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.MaxLimit {
		return 0, store.ErrInvalid
	}
	return n, nil
}

func lockQuery(raw url.Values) (lockFilter, int, string, error) {
	for key, values := range raw {
		if !slices.Contains([]string{"path", "id", "cursor", "limit", "refspec"}, key) || len(values) != 1 {
			return lockFilter{}, 0, "", store.ErrInvalid
		}
	}
	f := lockFilter{Path: raw.Get("path"), ID: raw.Get("id")}
	if f.Path != "" && !validLockPath(f.Path) || len(f.ID) > 256 {
		return f, 0, "", store.ErrInvalid
	}
	if ref := raw.Get("refspec"); ref != "" && !validLockRef(&lockRef{Name: ref}) {
		return f, 0, "", store.ErrInvalid
	}
	limit, err := lockLimit(raw.Get("limit"))
	return f, limit, raw.Get("cursor"), err
}

func (h handler) listLocks(w http.ResponseWriter, r *http.Request) {
	if !lockHeaders(r) {
		lockError(w, http.StatusUnsupportedMediaType, "invalid Git LFS lock request headers")
		return
	}
	if _, ok := lockPrincipal(w, r); !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock query")
		return
	}
	f, limit, cursor, err := lockQuery(query)
	if err != nil {
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock query")
		return
	}
	locks, next, err := h.collectLocks(r, "list", f, limit, cursor)
	if err != nil {
		lockReadError(w, err)
		return
	}
	lockResponse(w, http.StatusOK, struct {
		Locks      []lockWire `json:"locks"`
		NextCursor string     `json:"next_cursor,omitempty"`
	}{locks, next})
}

func (h handler) verifyLocks(w http.ResponseWriter, r *http.Request) {
	if !lockHeaders(r) || r.URL.RawQuery != "" {
		lockError(w, http.StatusUnsupportedMediaType, "invalid Git LFS lock request headers")
		return
	}
	p, ok := lockPrincipal(w, r)
	if !ok {
		return
	}
	var request struct {
		Cursor string   `json:"cursor"`
		Limit  int      `json:"limit"`
		Ref    *lockRef `json:"ref"`
	}
	if !decodeLockBody(w, r, &request) {
		return
	}
	if !validLockRef(request.Ref) || request.Limit < 0 || request.Limit > store.MaxLimit {
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock verification")
		return
	}
	if request.Limit == 0 {
		request.Limit = store.DefaultLimit
	}
	locks, next, err := h.collectLocks(r, "verify", lockFilter{}, request.Limit, request.Cursor)
	if err != nil {
		lockReadError(w, err)
		return
	}
	reply := struct {
		Ours       []lockWire `json:"ours"`
		Theirs     []lockWire `json:"theirs"`
		NextCursor string     `json:"next_cursor,omitempty"`
	}{Ours: []lockWire{}, Theirs: []lockWire{}, NextCursor: next}
	for _, lock := range locks {
		if lock.Owner.Name == p.Name {
			reply.Ours = append(reply.Ours, lock)
		} else {
			reply.Theirs = append(reply.Theirs, lock)
		}
	}
	lockResponse(w, http.StatusOK, reply)
}

func (h handler) unlockLock(w http.ResponseWriter, r *http.Request) {
	if !lockHeaders(r) || r.URL.RawQuery != "" {
		lockError(w, http.StatusUnsupportedMediaType, "invalid Git LFS lock request headers")
		return
	}
	p, ok := lockPrincipal(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" || len(id) > 256 || strings.ContainsAny(id, "/\\\x00\r\n") {
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock ID")
		return
	}
	var request struct {
		Force bool     `json:"force"`
		Ref   *lockRef `json:"ref"`
	}
	if !decodeLockBody(w, r, &request) {
		return
	}
	if !validLockRef(request.Ref) {
		lockError(w, http.StatusBadRequest, "invalid Git LFS unlock request")
		return
	}
	target, err := h.lockTargetForID(r)
	if err != nil {
		lockError(w, lockTargetStatus(err), "Git LFS unlock denied")
		return
	}
	repoKey := pkgroute.RequestFrom(target).Repo.Key
	v, err := h.k.DB().GitLFSLocks().Get(r.Context(), repoKey, id)
	if err != nil {
		lockError(w, http.StatusNotFound, "Git LFS lock not found")
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class != repo.Virtual &&
		!h.k.Can(r, v.Path, types.ActionWrite) ||
		!h.k.Can(target, v.Path, types.ActionWrite) {
		lockError(w, http.StatusForbidden, "Git LFS unlock denied")
		return
	}
	if v.Owner != p.Name {
		if !request.Force || pkgroute.RequestFrom(r).Repo.Class != repo.Virtual &&
			!h.k.Can(r, v.Path, types.ActionDelete) ||
			!h.k.Can(target, v.Path, types.ActionDelete) {
			lockError(w, http.StatusForbidden, "Git LFS unlock denied")
			return
		}
	}
	v, err = h.k.DB().GitLFSLocks().Delete(r.Context(), repoKey, id)
	if err != nil {
		lockError(w, http.StatusNotFound, "Git LFS lock not found")
		return
	}
	lockResponse(w, http.StatusOK, struct {
		Lock lockWire `json:"lock"`
	}{lockDocument(v)})
}

func (h handler) lockTargetForID(r *http.Request) (*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		return nil, store.ErrNotFound
	}
	if c.Class != repo.Virtual {
		return r, nil
	}
	if c.DefaultDeploymentRepo == "" {
		return nil, store.ErrNotFound
	}
	members, err := h.members(r)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		mc := pkgroute.RequestFrom(member).Repo
		if mc.Key == c.DefaultDeploymentRepo &&
			(mc.Class == repo.Local || mc.Class == repo.Federated) {
			return member, nil
		}
	}
	return nil, store.ErrNotFound
}

func lockReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		lockError(w, http.StatusForbidden, "Git LFS locks denied")
	case errors.Is(err, store.ErrInvalidCursor), errors.Is(err, store.ErrInvalid):
		lockError(w, http.StatusBadRequest, "invalid Git LFS lock cursor")
	case errors.Is(err, store.ErrNotFound):
		lockError(w, http.StatusBadGateway, "Git LFS lock source unavailable")
	default:
		lockError(w, http.StatusBadGateway, "Git LFS lock source unavailable")
	}
}

func (h handler) lockMembers(r *http.Request, mode string) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		if mode == "verify" {
			return nil, auth.ErrForbidden
		}
		return []*http.Request{r}, nil
	}
	if c.Class != repo.Virtual {
		return []*http.Request{r}, nil
	}
	if mode == "verify" {
		target, err := h.lockTargetForID(r)
		if err != nil {
			return nil, err
		}
		if !h.k.CanFolder(target, "", types.ActionWrite) {
			return nil, auth.ErrForbidden
		}
	}
	return h.members(r)
}

func (h handler) readLockPage(
	r *http.Request, f lockFilter, limit int, after string, strict bool,
) ([]types.GitLFSLock, string, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		return h.remoteLockPage(r, f, limit, after, strict)
	}
	return h.k.DB().GitLFSLocks().List(r.Context(), store.GitLFSLockFilter{
		RepoKey: c.Key, Path: f.Path, ID: f.ID,
	}, store.Page{Limit: limit, After: after})
}

// earlierLock suppresses a later virtual member's lock at a path already
// supplied by a preceding member. It also checks upstream members afresh, so
// a source failure cannot be mistaken for an unlocked path.
func (h handler) earlierLock(
	members []*http.Request, before int, path string,
) (bool, error) {
	for _, member := range members[:before] {
		if !h.k.Can(member, path, types.ActionRead) {
			continue
		}
		rows, _, err := h.readLockPage(member, lockFilter{Path: path}, 1, "", false)
		if err != nil {
			return false, err
		}
		if len(rows) == 1 && h.k.Can(member, path, types.ActionRead) {
			return true, nil
		}
	}
	return false, nil
}

func (h handler) collectLocks(
	r *http.Request, mode string, f lockFilter, limit int, rawCursor string,
) ([]lockWire, string, error) {
	want := types.ActionRead
	if mode == "verify" {
		want = types.ActionWrite
	}
	virtual := pkgroute.RequestFrom(r).Repo.Class == repo.Virtual
	if f.Path != "" {
		if !virtual && !h.k.Can(r, f.Path, want) {
			return nil, "", auth.ErrForbidden
		}
	} else if !virtual && !h.k.CanFolder(r, "", want) {
		return nil, "", auth.ErrForbidden
	}
	members, err := h.lockMembers(r, mode)
	if err != nil {
		return nil, "", err
	}
	if len(members) == 0 {
		return []lockWire{}, "", nil
	}
	virtualVerify := mode == "verify" && virtual
	var target *http.Request
	if virtualVerify {
		target, err = h.lockTargetForID(r)
		if err != nil {
			return nil, "", err
		}
	}
	c, err := parseLockCursor(rawCursor, pkgroute.RequestFrom(r).Repo.Key,
		mode, f, len(members))
	if err != nil {
		return nil, "", err
	}
	out := make([]lockWire, 0, limit)
	// A page can contain hidden paths. Bound source/database page work per
	// request, retaining a continuation cursor even for an empty visible page.
	for rounds := 0; rounds < 16 && c.Member < len(members); rounds++ {
		member := members[c.Member]
		memberWant := want
		if virtualVerify {
			memberWant = types.ActionRead
		}
		if virtualVerify && !h.k.CanFolder(member, "", memberWant) {
			return nil, "", auth.ErrForbidden
		}
		if f.Path != "" && !h.k.Can(member, f.Path, memberWant) {
			c.Member++
			c.After = ""
			continue
		}
		rows, next, err := h.readLockPage(member, f, limit-len(out), c.After, virtualVerify)
		if err != nil {
			return nil, "", err
		}
		for _, row := range rows {
			if !h.k.Can(member, row.Path, memberWant) {
				if virtualVerify {
					return nil, "", auth.ErrForbidden
				}
				continue
			}
			if !virtual && !h.k.Can(r, row.Path, want) ||
				virtualVerify && !h.k.Can(target, row.Path, types.ActionWrite) {
				continue
			}
			if mode == "list" && c.Member > 0 {
				duplicate, err := h.earlierLock(members, c.Member, row.Path)
				if err != nil {
					return nil, "", err
				}
				if duplicate {
					continue
				}
			}
			out = append(out, lockDocument(row))
		}
		if next != "" {
			c.After = next
		} else {
			c.Member++
			c.After = ""
		}
		if len(out) == limit {
			break
		}
	}
	if c.Member >= len(members) {
		return out, "", nil
	}
	return out, encodeLockCursor(c), nil
}
