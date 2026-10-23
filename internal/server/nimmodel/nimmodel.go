// Package nimmodel serves the documented NGC metadata and model-resource URLs
// through a remote NIM repository. Container layers use the separate Docker
// remote and its own credentials and permissions.
package nimmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const maxMetadata = 16 << 20

// Protocol admits only remote repositories. The NIM runtime authenticates to
// this route independently of its Docker registry login.
func Protocol(svc *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{}
	if svc != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: svc}
	}
	return pkgroute.Protocol{
		Name: "nimmodel", Types: []repo.PackageType{repo.NIMModel},
		Surface: apierror.Artifactory, Sources: sources,
		Challenge: func(repo.Config) bool { return true },
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct{ kit *pkgroute.Kit }

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	resource, err := parseResource(pkgroute.RequestFrom(r).Rest, r.URL.RawQuery)
	if err != nil {
		h.kit.Error(w, r, err)
		return
	}
	resource.Method = http.MethodGet
	result, err := h.kit.Read(r, resource)
	if err != nil {
		h.kit.Error(w, r, err)
		return
	}
	h.kit.ServeResult(w, r, result)
}

func badRequest() error { return apierror.New(http.StatusBadRequest, "invalid NIM resource") }

// NGC documents both a tag-level metadata API and model file URLs. Their
// request identities are normalized into distinct cache paths so a change in
// tag, repository or file cannot reuse another representation.
func parseResource(raw, query string) (source.Resource, error) {
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded != part || !safeSegment(part) {
			return source.Resource{}, badRequest()
		}
	}
	if len(parts) < 3 || parts[0] != "v2" {
		return source.Resource{}, badRequest()
	}
	switch parts[1] {
	case "nim":
		return nimResource(raw, parts, query)
	case "repos":
		return repositoryResource(raw, parts, query)
	case "models":
		return modelResource(raw, parts, query)
	case "org":
		if len(parts) > 3 &&
			(parts[3] == "models" || parts[3] == "team" && len(parts) > 5 && parts[5] == "models") {
			return modelResource(raw, parts, query)
		}
		return repositoryResource(raw, parts, query)
	default:
		return source.Resource{}, noRoute()
	}
}

func noRoute() error {
	return apierror.New(http.StatusNotFound, "Could not find resource")
}

func nimResource(raw string, parts []string, query string) (source.Resource, error) {
	switch {
	case len(parts) == 3 && parts[2] == "metadata":
		values, err := url.ParseQuery(query)
		if err != nil || len(values) != 2 || len(values["resourceId"]) != 1 ||
			len(values["tag"]) != 1 || !safeID(values.Get("resourceId")) ||
			!safeSegment(values.Get("tag")) {
			return source.Resource{}, badRequest()
		}
		return metadataResource(values.Get("resourceId"), values.Get("tag")), nil
	case (len(parts) == 5 || len(parts) == 6) && parts[len(parts)-1] == "metadata":
		if query != "" {
			return source.Resource{}, badRequest()
		}
		id := "nim/" + strings.Join(parts[2:len(parts)-2], "/")
		tag := parts[len(parts)-2]
		r := metadataResource(id, tag)
		r.UpstreamPath = raw
		return r, nil
	default:
		return source.Resource{}, noRoute()
	}
}

func repositoryResource(raw string, parts []string, query string) (source.Resource, error) {
	if query != "" {
		return source.Resource{}, badRequest()
	}
	var id string
	switch {
	case parts[1] == "repos" && (len(parts) == 4 || len(parts) == 5):
		id = strings.Join(parts[2:], "/")
	case parts[1] == "org" && (len(parts) == 5 || len(parts) == 7) &&
		parts[len(parts)-2] == "repos" &&
		(len(parts) == 5 || parts[3] == "team"):
		id = parts[2] + "/" + parts[len(parts)-1]
		if len(parts) == 7 {
			id = parts[2] + "/" + parts[4] + "/" + parts[6]
		}
	default:
		return source.Resource{}, noRoute()
	}
	return source.Resource{
		Kind: source.Metadata, Path: "catalog/" + id + "/repo.json", UpstreamPath: raw,
		PermissionFolder: true, PermissionPath: "models/" + id,
		Variant: "nim-repository-v1", ExpectedContentType: "application/json",
		ValidateMetadata: validateJSON,
	}, nil
}

func modelResource(raw string, parts []string, query string) (source.Resource, error) {
	if query != "" {
		return source.Resource{}, badRequest()
	}
	r := source.Resource{UpstreamPath: raw, Kind: source.Artifact}
	switch {
	case parts[1] == "models" && len(parts) >= 7 &&
		(parts[4] == "versions" || len(parts) > 5 && parts[5] == "versions"):
		versionAt := 4
		if parts[5] == "versions" {
			versionAt = 5
		}
		id := strings.Join(parts[2:versionAt], "/")
		return modelObject(r, parts, id, versionAt)
	case parts[1] == "org" && len(parts) >= 8 && (parts[3] == "models" || len(parts) > 5 && parts[3] == "team" && parts[5] == "models"):
		modelAt := 3
		id := parts[2]
		if parts[3] == "team" {
			modelAt = 5
			id += "/" + parts[4]
		}
		if len(parts) < modelAt+5 || parts[modelAt+2] != "versions" {
			return source.Resource{}, badRequest()
		}
		id += "/" + parts[modelAt+1]
		return modelObject(r, parts, id, modelAt+2)
	default:
		return source.Resource{}, noRoute()
	}
}

func modelObject(
	r source.Resource,
	parts []string,
	id string,
	versionAt int,
) (source.Resource, error) {
	if len(parts) == versionAt+3 && parts[versionAt+2] == "zip" {
		r.Path = "models/" + id + "/" + parts[versionAt+1] + "/model.zip"
		return r, nil
	}
	if len(parts) < versionAt+4 || parts[versionAt+2] != "files" {
		return source.Resource{}, badRequest()
	}
	r.Path = "models/" + id + "/" + parts[versionAt+1] + "/" + strings.Join(
		parts[versionAt+3:],
		"/",
	)
	return r, nil
}

func safeID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !safeSegment(part) {
			return false
		}
	}
	return true
}

func metadataResource(id, tag string) source.Resource {
	q := url.Values{"resourceId": {id}, "tag": {tag}}
	return source.Resource{
		Kind: source.Metadata, Path: "models/" + id + "/" + tag + "/.jfrog_nim_model_info.json",
		UpstreamPath: "v2/nim/metadata", Query: q.Encode(),
		PermissionFolder: true, PermissionPath: "models/" + id + "/" + tag,
		Variant: "nim-tag-metadata-v1", ExpectedContentType: "application/json",
		ValidateMetadata: validateTagMetadata,
	}
}

func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func validateJSON(
	ctx context.Context,
	body io.ReadSeeker,
	size int64,
	_ source.MetadataResponse,
) error {
	if ctx.Err() != nil || size <= 0 || size > maxMetadata {
		return source.ErrInvalidResult
	}
	data, err := io.ReadAll(io.LimitReader(body, maxMetadata+1))
	if err != nil || int64(len(data)) != size {
		return source.ErrInvalidResult
	}
	var object map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&object); err != nil || object == nil {
		return source.ErrInvalidResult
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return source.ErrInvalidResult
	}
	return nil
}

func validateTagMetadata(
	ctx context.Context,
	body io.ReadSeeker,
	size int64,
	m source.MetadataResponse,
) error {
	if err := validateJSON(ctx, body, size, m); err != nil {
		return err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return source.ErrInvalidResult
	}
	var object struct {
		Profiles []struct {
			ID      string          `json:"profile_id"`
			Devices json.RawMessage `json:"tested_gpu_devices"`
		} `json:"profiles"`
	}
	if err := json.NewDecoder(body).Decode(&object); err != nil {
		return source.ErrInvalidResult
	}
	for _, profile := range object.Profiles {
		if profile.ID == "" || len(profile.Devices) == 0 || profile.Devices[0] != '[' {
			return source.ErrInvalidResult
		}
	}
	return nil
}
