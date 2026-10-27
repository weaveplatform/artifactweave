package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/permission"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// The permission APIs (issue #82): the v1 and v2 permission targets under
// /artifactory and the Access v2 permissions under /access are three views
// of one stored permission (decision 0006). Each converts its wire form with
// pkg/types and calls the permission service, which validates and stores.

const (
	permV1Path     = "/artifactory/api/security/permissions/"
	permV2Path     = "/artifactory/api/v2/security/permissions/"
	permAccessPath = "/access/api/v2/permissions/"
	// permPageDefault is the Access listing's page size when the client
	// sends no limit; JFrog documents a default without its value.
	permPageDefault = 100
	permPageMax     = 99999
)

// permCaller is an authenticated caller of a permission API: the service
// over the store and the caller's decider, which answers "admin" and "holds
// MANAGE on this permission" with group admin rights and token scopes
// applied.
type permCaller struct {
	svc permission.Service
	dec *authz.Decider
	// canManageResources: the caller holds the Manage Resources role, which manages
	// every permission as an administrator does and reaches nothing else
	// (decision 0006).
	canManageResources bool
}

func permissionCaller(ctx context.Context, cfg Config) (permCaller, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return permCaller{}, fmt.Errorf("permissions: %w", err)
	}
	st, err := artifactoryService{cfg: cfg}.content()
	if err != nil {
		return permCaller{}, err
	}
	// The middleware has already evaluated the request's permissions under
	// its token scope; accessFor only evaluates them for a principal it did
	// not see.
	a, err := accessFor(ctx, st, p)
	if err != nil {
		return permCaller{}, artifactoryService{
			cfg: cfg,
		}.internalError(
			ctx,
			"permission caller",
			err,
		)
	}
	resources, err := a.d.ManagesResources(ctx, st.Users(), st.Groups())
	if err != nil {
		return permCaller{}, artifactoryService{
			cfg: cfg,
		}.internalError(
			ctx,
			"permission caller",
			err,
		)
	}
	return permCaller{
		svc:                permission.Service{Store: st},
		dec:                a.d,
		canManageResources: resources,
	}, nil
}

// managesAll reports whether the caller manages every permission: an
// administrator, or a holder of the Manage Resources role, who may "create,
// edit, and delete permissions on any resource type" and sees every
// permission target (docs.jfrog.com permissions page; JA-16022 in 7.146.7;
// decision 0006).
func (c permCaller) managesAll() bool { return c.dec.IsAdmin() || c.canManageResources }

// admin refuses a caller that does not manage every permission. The
// permission APIs' writes and their admin-only reads ask it; every other
// admin-only route still asks for an administrator.
func (c permCaller) admin() error {
	if !c.managesAll() {
		return auth.ErrForbidden
	}
	return nil
}

// reader may list every permission and read any one whole: a caller that
// manages every permission, or a token carrying the system:permissions:r
// scope. Reading one section is stricter, for those managers alone
// (GetPermissionResource).
func (c permCaller) reader() bool {
	return c.managesAll() || c.dec.System("permissions", types.ScopeRead)
}

// manages reports whether the caller may manage p through the v2 API: a
// caller that manages every permission, or a holder of MANAGE in any of
// its sections.
func (c permCaller) manages(p types.Permission) bool {
	if c.managesAll() {
		return true
	}
	for _, r := range p.Resources {
		if c.dec.Holds(r.Actions, types.ActionManage) {
			return true
		}
	}
	return false
}

// lookup returns the named permission for a caller allowed by may. A
// permission the caller may not see answers 403 whether or not it exists,
// so a caller that does not see every permission cannot probe for names;
// one that does gets 404.
func (c permCaller) lookup(
	ctx context.Context,
	name string,
	may func(types.Permission) bool,
) (types.Permission, error) {
	p, err := c.svc.Get(ctx, name)
	switch {
	case errors.Is(err, permission.ErrNotFound) && !c.reader():
		return types.Permission{}, auth.ErrForbidden
	case err != nil:
		return types.Permission{}, fmt.Errorf("permissions: %w", err)
	case !may(p):
		return types.Permission{}, auth.ErrForbidden
	}
	return p, nil
}

// permissionError maps the service's errors: 400 for a document to fix,
// 404 and 409 for names, and 500 for anything else. auth and apierror
// errors pass through.
func (s artifactoryService) permissionError(ctx context.Context, op string, err error) error {
	var ae *apierror.Error
	switch {
	case errors.As(err, &ae):
		return err
	case errors.Is(err, permission.ErrInvalid):
		return badRequestf("%s", after(err, permission.ErrInvalid))
	case errors.Is(err, types.ErrLossyProjection):
		return badRequestf("%s; read it through /access/api/v2/permissions", err)
	case errors.Is(err, permission.ErrNotFound):
		return apierror.New(http.StatusNotFound, after(err, permission.ErrNotFound))
	case errors.Is(err, permission.ErrConflict):
		return apierror.New(http.StatusConflict, after(err, permission.ErrConflict))
	case errors.Is(err, permission.ErrForbidden):
		return apierror.New(http.StatusForbidden, after(err, permission.ErrForbidden))
	default:
		return s.internalError(ctx, op, err)
	}
}

// after is the message following sentinel, whatever wraps it.
func after(err, sentinel error) string {
	msg := err.Error()
	if _, rest, ok := strings.Cut(msg, sentinel.Error()+": "); ok {
		return rest
	}
	return msg
}

// invalidDoc marks a document the converters refused (an unknown action
// name) as the caller's to fix.
func invalidDoc(err error) error {
	return fmt.Errorf("%w: %w", permission.ErrInvalid, err)
}

// bodyName returns the name a write applies to: the path parameter, which
// wins over a body name that agrees; a disagreeing body name is a 400.
func bodyName(path string, body *string) (string, error) {
	if body != nil && *body != "" && *body != path {
		return "", badRequestf(
			"the name in the body (%s) does not match the path (%s)",
			*body,
			path,
		)
	}
	return path, nil
}

// --- v1 permission targets ------------------------------------------------

// GetPermissionTargets lists every permission with its v1 detail URL.
func (s artifactoryService) GetPermissionTargets(
	ctx context.Context,
	_ artgen.GetPermissionTargetsRequestObject,
) (artgen.GetPermissionTargetsResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	ps, err := c.svc.List(ctx)
	if err != nil {
		return nil, s.permissionError(ctx, "list permissions", err)
	}
	return artgen.GetPermissionTargets200JSONResponse(listItems(ctx, permV1Path, ps)), nil
}

func listItems(
	ctx context.Context,
	path string,
	ps []types.Permission,
) []artgen.PermissionTargetListItem {
	base := baseurl.From(ctx) + path
	out := make([]artgen.PermissionTargetListItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, artgen.PermissionTargetListItem{
			Name: strPtr(p.Name), Uri: strPtr(base + nameSegment(p.Name)),
		})
	}
	return out
}

// GetPermissionTargetDetails returns the v1 view: the repository section
// with action letters.
func (s artifactoryService) GetPermissionTargetDetails(
	ctx context.Context,
	req artgen.GetPermissionTargetDetailsRequestObject,
) (artgen.GetPermissionTargetDetailsResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	p, err := c.svc.Get(ctx, req.PermissionTargetName)
	if err != nil {
		return nil, s.permissionError(ctx, "get permission", err)
	}
	v1, err := types.PermissionToV1(p)
	if err != nil {
		return nil, s.permissionError(ctx, "render permission", err)
	}
	doc, err := convert[artgen.GetPermissionTargetDetails200JSONResponse](v1)
	if err != nil {
		return nil, s.internalError(ctx, "render permission", err)
	}
	return doc, nil
}

// CreateOrReplacePermissionTarget creates the permission or replaces its
// repository section; build and release bundle sections set through v2 or
// Access stay.
func (s artifactoryService) CreateOrReplacePermissionTarget(
	ctx context.Context,
	req artgen.CreateOrReplacePermissionTargetRequestObject,
) (artgen.CreateOrReplacePermissionTargetResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	body := req.JSONBody
	if body == nil {
		body = (*artgen.PermissionTarget)(
			req.ApplicationVndOrgJfrogArtifactorySecurityPermissionTargetPlusJSONBody,
		)
	}
	if body == nil {
		return nil, badRequestf("a permission target body is required")
	}
	name, err := bodyName(req.PermissionTargetName, body.Name)
	if err != nil {
		return nil, err
	}
	w, err := convert[wire.PermissionTargetV1](body)
	if err != nil {
		return nil, s.internalError(ctx, "read permission", err)
	}
	w.Name = name
	p, err := types.PermissionFromV1(w)
	if err != nil {
		return nil, s.permissionError(ctx, "read permission", invalidDoc(err))
	}
	created, err := c.svc.PutResource(ctx, name, types.ResourceArtifact,
		p.Resources[types.ResourceArtifact], true)
	if err != nil {
		return nil, s.permissionError(ctx, "store permission", err)
	}
	verb := "updated"
	if created {
		verb = "created"
	}
	return artgen.CreateOrReplacePermissionTarget200JSONResponse{
		Info: strPtr(fmt.Sprintf("Successfully %s permission target '%s'", verb, name)),
	}, nil
}

// DeletePermissionTarget removes the whole permission.
func (s artifactoryService) DeletePermissionTarget(
	ctx context.Context,
	req artgen.DeletePermissionTargetRequestObject,
) (artgen.DeletePermissionTargetResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	if err := c.svc.Delete(ctx, req.PermissionTargetName); err != nil {
		return nil, s.permissionError(ctx, "delete permission", err)
	}
	return artgen.DeletePermissionTarget200TextResponse(fmt.Sprintf(
		"Permission Target '%s' has been removed successfully.", req.PermissionTargetName)), nil
}

// --- v2 permission targets ------------------------------------------------

// GetPermissionTargetsV2 lists what the caller may manage: every permission
// for an administrator or a Manage Resources holder, otherwise those it
// holds MANAGE on.
func (s artifactoryService) GetPermissionTargetsV2(
	ctx context.Context,
	_ artgen.GetPermissionTargetsV2RequestObject,
) (artgen.GetPermissionTargetsV2ResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	ps, err := c.svc.List(ctx)
	if err != nil {
		return nil, s.permissionError(ctx, "list permissions", err)
	}
	ps = slices.DeleteFunc(ps, func(p types.Permission) bool { return !c.manages(p) })
	return artgen.GetPermissionTargetsV2200JSONResponse(listItems(ctx, permV2Path, ps)), nil
}

// v2Views renders the permissions the v2 form can express. One whose
// targets carry different patterns has no v2 view and is left out of a
// listing; reading it by name explains why.
func v2Views(ps []types.Permission) ([]artgen.PermissionTargetV2, error) {
	out := make([]artgen.PermissionTargetV2, 0, len(ps))
	for _, p := range ps {
		w, err := types.PermissionToV2(p)
		if errors.Is(err, types.ErrLossyProjection) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("permissions: %s: %w", p.Name, err)
		}
		doc, err := convert[artgen.PermissionTargetV2](w)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

// granting lists, for the by-user and by-group views, the permissions
// naming one principal directly; an unknown principal has none.
func (s artifactoryService) granting(
	ctx context.Context,
	users, groups []string,
) ([]artgen.PermissionTargetV2, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	ps, _, err := c.svc.Granting(ctx, users, groups, false)
	if errors.Is(err, permission.ErrNotFound) {
		return []artgen.PermissionTargetV2{}, nil
	}
	if err != nil {
		return nil, s.permissionError(ctx, "permissions by principal", err)
	}
	// getpermissiontargetsbyuserv2.json: an administrator, or a holder of
	// MANAGE, who sees the permissions it manages; a Manage Resources holder
	// sees them all.
	ps = slices.DeleteFunc(ps, func(p types.Permission) bool { return !c.manages(p) })
	views, err := v2Views(ps)
	if err != nil {
		return nil, s.internalError(ctx, "render permissions", err)
	}
	return views, nil
}

// GetPermissionTargetsByGroupV2 returns the permissions granting the group.
func (s artifactoryService) GetPermissionTargetsByGroupV2(
	ctx context.Context,
	req artgen.GetPermissionTargetsByGroupV2RequestObject,
) (artgen.GetPermissionTargetsByGroupV2ResponseObject, error) {
	views, err := s.granting(ctx, nil, []string{req.GroupName})
	if err != nil {
		return nil, err
	}
	return artgen.GetPermissionTargetsByGroupV2200JSONResponse(views), nil
}

// GetPermissionTargetsByUserV2 returns the permissions granting the user
// directly; grants through its groups are not expanded.
func (s artifactoryService) GetPermissionTargetsByUserV2(
	ctx context.Context,
	req artgen.GetPermissionTargetsByUserV2RequestObject,
) (artgen.GetPermissionTargetsByUserV2ResponseObject, error) {
	views, err := s.granting(ctx, []string{req.UserName}, nil)
	if err != nil {
		return nil, err
	}
	return artgen.GetPermissionTargetsByUserV2200JSONResponse(views), nil
}

// GetPermissionTargetDetailsV2 returns every v2 section.
func (s artifactoryService) GetPermissionTargetDetailsV2(
	ctx context.Context,
	req artgen.GetPermissionTargetDetailsV2RequestObject,
) (artgen.GetPermissionTargetDetailsV2ResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	p, err := c.lookup(ctx, req.PermissionTargetName, c.manages)
	if err != nil {
		return nil, s.permissionError(ctx, "get permission", err)
	}
	w, err := types.PermissionToV2(p)
	if err != nil {
		return nil, s.permissionError(ctx, "render permission", err)
	}
	doc, err := convert[artgen.GetPermissionTargetDetailsV2200JSONResponse](w)
	if err != nil {
		return nil, s.internalError(ctx, "render permission", err)
	}
	return doc, nil
}

// PermissionTargetExistenceCheckV2 answers 200 or 404 with no body.
func (s artifactoryService) PermissionTargetExistenceCheckV2(
	ctx context.Context,
	req artgen.PermissionTargetExistenceCheckV2RequestObject,
) (artgen.PermissionTargetExistenceCheckV2ResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	if _, err := c.lookup(ctx, req.PermissionTargetName, c.manages); err != nil {
		return nil, s.permissionError(ctx, "check permission", err)
	}
	return artgen.PermissionTargetExistenceCheckV2200Response{}, nil
}

// v2Body returns the v2 document a write carries, in either media type.
func v2Body(
	json *artgen.PermissionTargetV2,
	vendor *artgen.PermissionTargetV2,
) (*artgen.PermissionTargetV2, error) {
	if json == nil {
		json = vendor
	}
	if json == nil {
		return nil, badRequestf("a permission target body is required")
	}
	return json, nil
}

func (s artifactoryService) readV2(
	ctx context.Context,
	path string,
	body *artgen.PermissionTargetV2,
) (types.Permission, error) {
	name, err := bodyName(path, body.Name)
	if err != nil {
		return types.Permission{}, err
	}
	w, err := convert[wire.PermissionTargetV2](body)
	if err != nil {
		return types.Permission{}, s.internalError(ctx, "read permission", err)
	}
	w.Name = name
	p, err := types.PermissionFromV2(w)
	if err != nil {
		return types.Permission{}, s.permissionError(ctx, "read permission", invalidDoc(err))
	}
	return p, nil
}

// CreatePermissionTargetV2 creates the permission; 409 when the name is
// taken, which jfrog-client-go reads as "already exists".
func (s artifactoryService) CreatePermissionTargetV2(
	ctx context.Context,
	req artgen.CreatePermissionTargetV2RequestObject,
) (artgen.CreatePermissionTargetV2ResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	body, err := v2Body(
		req.JSONBody,
		(*artgen.PermissionTargetV2)(
			req.ApplicationVndOrgJfrogArtifactorySecurityPermissionTargetV2PlusJSONBody,
		),
	)
	if err != nil {
		return nil, err
	}
	p, err := s.readV2(ctx, req.PermissionTargetName, body)
	if err != nil {
		return nil, err
	}
	if err := c.svc.Create(ctx, p); err != nil {
		return nil, s.permissionError(ctx, "create permission", err)
	}
	return artgen.CreatePermissionTargetV2201Response{}, nil
}

// v2Sections are the sections the v2 form carries. A v2 replace drops those
// of them the body leaves out and keeps the others (destination,
// pipeline_source), which no v2 client can see or send.
var v2Sections = []types.ResourceType{
	types.ResourceArtifact, types.ResourceBuild, types.ResourceReleaseBundle,
}

// CreateOrReplacePermissionTargetV2 creates the permission or replaces its
// v2 sections whole. Administrators may create; holders of MANAGE may
// replace the actions of one that exists, granting or revoking only the
// actions they hold and never MANAGE, and leaving their own entries and
// their groups' as they are (createorreplacepermissiontargetv2.json,
// decision 0006).
func (s artifactoryService) CreateOrReplacePermissionTargetV2(
	ctx context.Context,
	req artgen.CreateOrReplacePermissionTargetV2RequestObject,
) (artgen.CreateOrReplacePermissionTargetV2ResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	body, err := v2Body(
		req.JSONBody,
		(*artgen.PermissionTargetV2)(
			req.ApplicationVndOrgJfrogArtifactorySecurityPermissionTargetV2PlusJSONBody,
		),
	)
	if err != nil {
		return nil, err
	}
	p, err := s.readV2(ctx, req.PermissionTargetName, body)
	if err != nil {
		return nil, err
	}
	_, err = c.svc.Write(
		ctx,
		p.Name,
		func(old types.Permission, exists bool) (types.Permission, error) {
			switch {
			case !exists:
				return p, c.admin()
			case !c.manages(old):
				return types.Permission{}, auth.ErrForbidden
			case !c.managesAll():
				if err := permission.CheckManagerEdit(old, p, v2Sections, c.dec); err != nil {
					return types.Permission{}, fmt.Errorf("permissions: %w", err)
				}
			}
			p.Description = old.Description
			for rt, r := range old.Resources {
				if !slices.Contains(v2Sections, rt) {
					p.Resources[rt] = r
				}
			}
			return p, nil
		},
	)
	if err != nil {
		return nil, s.permissionError(ctx, "store permission", err)
	}
	return artgen.CreateOrReplacePermissionTargetV2200Response{}, nil
}

// DeletePermissionTargetV2 removes the permission. Unlike a read or a
// replace, it needs an administrator (deletepermissiontargetv2.json) or a
// Manage Resources holder.
func (s artifactoryService) DeletePermissionTargetV2(
	ctx context.Context,
	req artgen.DeletePermissionTargetV2RequestObject,
) (artgen.DeletePermissionTargetV2ResponseObject, error) {
	c, err := permissionCaller(ctx, s.cfg)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	if err := c.svc.Delete(ctx, req.PermissionTargetName); err != nil {
		return nil, s.permissionError(ctx, "delete permission", err)
	}
	return artgen.DeletePermissionTargetV2200Response{}, nil
}

// --- Access v2 permissions ------------------------------------------------

// accessJSON writes a permission document as stored. The generated Access
// types only carry the artifact, build and release_bundle sections; the
// platform provider also sends destination and pipeline_source, which must
// read back as written.
type accessJSON struct {
	status int
	body   any
}

func (r accessJSON) write(w http.ResponseWriter) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(r.body); err != nil {
		return fmt.Errorf("permissions: encode: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	_, err := w.Write(buf.Bytes())
	return err //nolint:wrapcheck // the client went away
}

func (r accessJSON) VisitCreatePermissionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r accessJSON) VisitGetPermissionDetailsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r accessJSON) VisitGetPermissionsResponse(w http.ResponseWriter) error { return r.write(w) }

// accessCaller is permissionCaller for the Access service.
func (s accessService) caller(ctx context.Context) (permCaller, error) {
	return permissionCaller(ctx, s.cfg)
}

func (s accessService) errOf(ctx context.Context, op string, err error) error {
	return artifactoryService{cfg: s.cfg}.permissionError(ctx, op, err)
}

// GetPermissions pages through the permissions, optionally only those
// granting given users or groups, as name and uri pairs or, with expand,
// documents narrowed to those principals' grants. The cursor is the last
// name of the previous page.
func (s accessService) GetPermissions(
	ctx context.Context,
	req accessgen.GetPermissionsRequestObject,
) (accessgen.GetPermissionsResponseObject, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if !c.reader() {
		return nil, auth.ErrForbidden
	}
	q := req.Params
	limit := permPageDefault
	if q.Limit != nil {
		limit = *q.Limit
	}
	var users, groups []string
	if q.User != nil {
		users = *q.User
	}
	if q.Group != nil {
		groups = *q.Group
	}
	filtered := len(users)+len(groups) > 0
	expand := q.Expand != nil && *q.Expand
	switch {
	case limit < 1 || limit > permPageMax:
		return nil, badRequestf("limit must be between 1 and %d", permPageMax)
	case expand && !filtered:
		return nil, badRequestf("expand requires a user or group filter")
	}
	var (
		ps []types.Permission
		pr permission.Principals
	)
	if filtered {
		inherited := q.IncludeInheritedGroups == nil || *q.IncludeInheritedGroups
		// User names match without regard to case, group names exactly
		// (getpermissions).
		users, err = c.svc.FoldUsers(ctx, users)
		if err == nil {
			ps, pr, err = c.svc.Granting(ctx, users, groups, inherited)
		}
	} else {
		ps, err = c.svc.List(ctx)
	}
	if err != nil {
		return nil, s.errOf(ctx, "list permissions", err)
	}
	page, next := pageAfter(ps, q.Cursor, limit)
	if expand {
		docs := make([]wire.AccessPermission, 0, len(page))
		for _, p := range page {
			docs = append(docs, types.PermissionToAccess(pr.Narrow(p)))
		}
		return accessJSON{http.StatusOK, accessPage{Permissions: docs, Cursor: next}}, nil
	}
	base := baseurl.From(ctx) + permAccessPath
	items := make([]accessItem, 0, len(page))
	for _, p := range page {
		items = append(items, accessItem{Name: p.Name, URI: base + url.PathEscape(p.Name)})
	}
	return accessJSON{http.StatusOK, accessPage{Permissions: items, Cursor: next}}, nil
}

type accessItem struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

type accessPage struct {
	Permissions any    `json:"permissions"`
	Cursor      string `json:"cursor,omitempty"`
}

// pageAfter returns up to limit permissions after the cursor, and the next
// cursor when more remain. getpermissions.json documents the cursor as the
// name of the page's last permission, passed back as the next call's
// cursor, so the next page starts after that name rather than at it, which
// would repeat it.
func pageAfter(ps []types.Permission, cursor *string, limit int) ([]types.Permission, string) {
	start := 0
	if cursor != nil && *cursor != "" {
		start = len(ps)
		if i := slices.IndexFunc(
			ps,
			func(p types.Permission) bool { return p.Name > *cursor },
		); i >= 0 {
			start = i
		}
	}
	end := min(start+limit, len(ps))
	page := ps[start:end]
	next := ""
	if end < len(ps) && len(page) > 0 {
		next = page[len(page)-1].Name
	}
	return page, next
}

// readAccess decodes an Access permission document. The create body is read
// raw, because the generated type drops the destination and
// pipeline_source sections.
func readAccess(ctx context.Context, body any) (wire.AccessPermission, error) {
	var w wire.AccessPermission
	if raw := rawBody(ctx); raw != nil {
		if err := json.Unmarshal(raw, &w); err != nil {
			return w, badRequestf("the permission is not valid JSON: %v", err)
		}
		return w, nil
	}
	return convert[wire.AccessPermission](body)
}

// CreatePermission creates a permission and returns it as stored, 201.
func (s accessService) CreatePermission(
	ctx context.Context,
	req accessgen.CreatePermissionRequestObject,
) (accessgen.CreatePermissionResponseObject, error) {
	c, err := s.caller(ctx)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	w, err := readAccess(ctx, req.Body)
	if err != nil {
		return nil, err
	}
	p, err := types.PermissionFromAccess(w)
	if err != nil {
		return nil, s.errOf(ctx, "read permission", invalidDoc(err))
	}
	if err := c.svc.Create(ctx, p); err != nil {
		return nil, s.errOf(ctx, "create permission", err)
	}
	stored, err := c.svc.Get(ctx, p.Name)
	if err != nil {
		return nil, s.errOf(ctx, "read permission", err)
	}
	return accessJSON{http.StatusCreated, types.PermissionToAccess(stored)}, nil
}

// GetPermissionDetails returns one permission to an administrator, a
// Manage Resources holder or a system:permissions:r token
// (getpermissiondetails.json).
func (s accessService) GetPermissionDetails(
	ctx context.Context,
	req accessgen.GetPermissionDetailsRequestObject,
) (accessgen.GetPermissionDetailsResponseObject, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := permission.ValidName(req.PermissionName); err != nil {
		return nil, s.errOf(ctx, "get permission", err)
	}
	p, err := c.lookup(ctx, req.PermissionName, func(types.Permission) bool { return c.reader() })
	if err != nil {
		return nil, s.errOf(ctx, "get permission", err)
	}
	return accessJSON{http.StatusOK, types.PermissionToAccess(p)}, nil
}

// DeletePermission removes a permission, 204.
func (s accessService) DeletePermission(
	ctx context.Context,
	req accessgen.DeletePermissionRequestObject,
) (accessgen.DeletePermissionResponseObject, error) {
	c, err := s.caller(ctx)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	if err := permission.ValidName(req.PermissionName); err != nil {
		return nil, s.errOf(ctx, "delete permission", err)
	}
	if err := c.svc.Delete(ctx, req.PermissionName); err != nil {
		return nil, s.errOf(ctx, "delete permission", err)
	}
	return accessgen.DeletePermission204Response{}, nil
}

// resourceType checks a section path parameter.
func resourceType(name string, rt string) (types.ResourceType, error) {
	if err := permission.ValidName(name); err != nil {
		return "", fmt.Errorf("permissions: %w", err)
	}
	if !permission.ValidResourceType(types.ResourceType(rt)) {
		return "", fmt.Errorf("%w: Unknown resource type '%s'", permission.ErrInvalid, rt)
	}
	return types.ResourceType(rt), nil
}

// sectionFrom converts one Access section into the internal shape through
// the permission converter, so action names are checked the same way.
func sectionFrom(rt types.ResourceType, body any) (types.Resource, error) {
	r, err := convert[wire.AccessResource](body)
	if err != nil {
		return types.Resource{}, err
	}
	p, err := types.PermissionFromAccess(wire.AccessPermission{
		Resources: map[string]wire.AccessResource{string(rt): r},
	})
	if err != nil {
		return types.Resource{}, invalidDoc(err)
	}
	return p.Resources[rt], nil
}

// sectionTo renders one section in the Access shape.
func sectionTo(rt types.ResourceType, r types.Resource) (accessgen.PermissionResource, error) {
	w := types.PermissionToAccess(types.Permission{
		Resources: map[types.ResourceType]types.Resource{rt: r},
	})
	return convert[accessgen.PermissionResource](w.Resources[string(rt)])
}

// GetPermissionResource returns one section. It is admin only, as
// getpermissionresource documents, but for Manage Resources holders, who
// see every permission whole: a system:permissions:r token, which reads the
// whole permission, is refused here (#382).
func (s accessService) GetPermissionResource(
	ctx context.Context,
	req accessgen.GetPermissionResourceRequestObject,
) (accessgen.GetPermissionResourceResponseObject, error) {
	c, err := s.caller(ctx)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	rt, err := resourceType(req.PermissionName, string(req.ResourceType))
	if err != nil {
		return nil, s.errOf(ctx, "get permission section", err)
	}
	p, err := c.svc.Get(ctx, req.PermissionName)
	if err != nil {
		return nil, s.errOf(ctx, "get permission section", err)
	}
	r, ok := p.Resources[rt]
	if !ok {
		return nil, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Permission '%s' has no %s resource", p.Name, rt))
	}
	doc, err := sectionTo(rt, r)
	if err != nil {
		return nil, s.errOf(ctx, "render permission section", err)
	}
	return accessgen.GetPermissionResource200JSONResponse(doc), nil
}

// UpdatePermissionResource merges the body into an existing section.
func (s accessService) UpdatePermissionResource(
	ctx context.Context,
	req accessgen.UpdatePermissionResourceRequestObject,
) (accessgen.UpdatePermissionResourceResponseObject, error) {
	c, err := s.caller(ctx)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	rt, err := resourceType(req.PermissionName, string(req.ResourceType))
	if err != nil {
		return nil, s.errOf(ctx, "update permission section", err)
	}
	patch, err := sectionFrom(rt, req.Body)
	if err != nil {
		return nil, s.errOf(ctx, "read permission section", err)
	}
	merged, err := c.svc.MergeResource(ctx, req.PermissionName, rt, patch)
	if err != nil {
		return nil, s.errOf(ctx, "update permission section", err)
	}
	doc, err := sectionTo(rt, merged)
	if err != nil {
		return nil, s.errOf(ctx, "render permission section", err)
	}
	return accessgen.UpdatePermissionResource200JSONResponse(doc), nil
}

// ReplacePermissionResource replaces one section whole, creating it when
// the permission had none.
func (s accessService) ReplacePermissionResource(
	ctx context.Context,
	req accessgen.ReplacePermissionResourceRequestObject,
) (accessgen.ReplacePermissionResourceResponseObject, error) {
	c, err := s.caller(ctx)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	rt, err := resourceType(req.PermissionName, string(req.ResourceType))
	if err != nil {
		return nil, s.errOf(ctx, "replace permission section", err)
	}
	r, err := sectionFrom(rt, req.Body)
	if err != nil {
		return nil, s.errOf(ctx, "read permission section", err)
	}
	if _, err := c.svc.PutResource(ctx, req.PermissionName, rt, r, false); err != nil {
		return nil, s.errOf(ctx, "replace permission section", err)
	}
	doc, err := sectionTo(rt, r)
	if err != nil {
		return nil, s.errOf(ctx, "render permission section", err)
	}
	return accessgen.ReplacePermissionResource200JSONResponse(doc), nil
}

// DeletePermissionResource removes one section, 204.
func (s accessService) DeletePermissionResource(
	ctx context.Context,
	req accessgen.DeletePermissionResourceRequestObject,
) (accessgen.DeletePermissionResourceResponseObject, error) {
	c, err := s.caller(ctx)
	if err == nil {
		err = c.admin()
	}
	if err != nil {
		return nil, err
	}
	rt, err := resourceType(req.PermissionName, string(req.ResourceType))
	if err != nil {
		return nil, s.errOf(ctx, "delete permission section", err)
	}
	if err := c.svc.DeleteResource(ctx, req.PermissionName, rt); err != nil {
		return nil, s.errOf(ctx, "delete permission section", err)
	}
	return accessgen.DeletePermissionResource204Response{}, nil
}
