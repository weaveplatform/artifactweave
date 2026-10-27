package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/vcs"
)

// vcsNested handles the path forms JFrog documents for GitLab subgroup
// owners. The generated strict router owns the ordinary one-segment forms.
// A separate wildcard is necessary because chi's {userOrg} captures only one
// segment, while a GitLab group/subgroup is represented by several segments.
//
//nolint:gocyclo // This bounded route grammar validates each documented subgroup form in one place.
func (s artifactoryService) vcsNested(w http.ResponseWriter, r *http.Request) {
	const prefix = "/artifactory/api/vcs/"
	rawPath := strings.ToLower(r.URL.RawPath)
	if !strings.HasPrefix(r.URL.Path, prefix) || len(r.URL.Path) > 2048 ||
		strings.Contains(rawPath, "%2f") || strings.Contains(rawPath, "%5c") ||
		strings.Contains(rawPath, "%2e") || strings.Contains(rawPath, "%25") {
		apierror.Write(
			w,
			apierror.ProfileArtifactory,
			apierror.New(http.StatusBadRequest, "invalid VCS path"),
		)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(parts) < 5 || len(parts) > 16 {
		http.NotFound(w, r)
		return
	}
	op, key := parts[0], parts[1]
	if key == "" || key == "." || key == ".." || len(key) > 128 {
		apierror.Write(
			w,
			apierror.ProfileArtifactory,
			apierror.New(http.StatusBadRequest, "invalid VCS repository key"),
		)
		return
	}
	var owner, name, kind, ref, ext string
	var list bool
	var file bool
	switch op {
	case "tags", "branches", "refs":
		owner, name = strings.Join(parts[2:len(parts)-1], "/"), parts[len(parts)-1]
		list = true
		if strings.Count(owner, "/") < 1 {
			http.NotFound(w, r)
			return
		}
	case "downloadTag", "downloadBranch", "downloadTagFile", "downloadBranchFile":
		kind = "tags"
		if strings.Contains(op, "Branch") {
			kind = "heads"
		}
		file = strings.HasSuffix(op, "File")
		marker := -1
		for i := 4; i+1 < len(parts); i++ {
			if parts[i] == "archive" && parts[i+1] == "refs" {
				marker = i
				break
			}
		}
		switch {
		case marker >= 4 && marker+2 < len(parts):
			owner, name = strings.Join(parts[2:marker-1], "/"), parts[marker-1]
			ref = strings.Join(parts[marker+2:], "/")
		case file:
			// A normal file selector can contain a path after TAG! or BRANCH!;
			// the generated single-segment route cannot capture that suffix.
			selector := -1
			for i := 4; i < len(parts); i++ {
				if strings.Contains(parts[i], "!") {
					selector = i
					break
				}
			}
			if selector < 4 {
				http.NotFound(w, r)
				return
			}
			owner, name = strings.Join(parts[2:selector-1], "/"), parts[selector-1]
			ref = strings.Join(parts[selector:], "/")
		default:
			http.NotFound(w, r)
			return
		}
		if file {
			var filename string
			var ok bool
			ref, filename, ok = strings.Cut(ref, "!")
			if !ok || !vcs.ValidFilePath(filename) {
				apierror.Write(
					w,
					apierror.ProfileArtifactory,
					apierror.New(http.StatusBadRequest, "invalid VCS file path"),
				)
				return
			}
			ref += "!" + filename
		}
		ext = r.URL.Query().Get("ext")
		if ext == "" {
			ext = "tar.gz"
		}
	default:
		http.NotFound(w, r)
		return
	}
	if !vcs.ValidProject(owner, name) {
		apierror.Write(
			w,
			apierror.ProfileArtifactory,
			apierror.New(http.StatusBadRequest, "invalid VCS repository path"),
		)
		return
	}
	if list {
		a, err := s.vcsRepository(r.Context(), key, owner, name)
		if err != nil {
			apierror.Write(w, apierror.ProfileArtifactory, err)
			return
		}
		if a.repo.VCSGitProvider != "GITLAB" {
			http.NotFound(w, r)
			return
		}
		refs, _, err := s.vcsRefs(r.Context(), key, owner, name)
		if err != nil {
			apierror.Write(w, apierror.ProfileArtifactory, err)
			return
		}
		if op == "refs" {
			var b strings.Builder
			for _, entry := range refs {
				refKind := "tags"
				if entry.Branch {
					refKind = "heads"
				}
				b.WriteString(entry.CommitID + " refs/" + refKind + "/" + entry.Name + "\n")
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(b.String())
			return
		}
		out := make(vcsListResponse, 0, len(refs))
		for _, entry := range refs {
			if entry.Branch == (op == "branches") {
				out = append(out, vcsListRef{entry.Name, entry.CommitID, entry.Branch})
			}
		}
		_ = out.write(w)
		return
	}
	a, err := s.vcsRepository(r.Context(), key, owner, name)
	if err != nil {
		apierror.Write(w, apierror.ProfileArtifactory, err)
		return
	}
	if a.repo.VCSGitProvider != "GITLAB" && strings.Count(owner, "/") > 0 {
		http.NotFound(w, r)
		return
	}
	if file {
		body, size, err := s.vcsFile(r.Context(), key, owner, name, kind, ref)
		if err != nil {
			apierror.Write(w, apierror.ProfileArtifactory, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if _, err := io.Copy(w, body); err != nil {
			noteCause(r.Context(), err)
		}
		return
	}
	if !vcs.ValidRef(ref) || !vcs.ValidExtension(ext) {
		apierror.Write(
			w,
			apierror.ProfileArtifactory,
			apierror.New(http.StatusBadRequest, "invalid VCS archive request"),
		)
		return
	}
	body, size, err := s.vcsArchive(
		r.Context(),
		key,
		owner,
		name,
		kind,
		ref,
		ext,
		r.URL.Query().Get("properties"),
	)
	if err != nil {
		apierror.Write(w, apierror.ProfileArtifactory, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if _, err := io.Copy(w, body); err != nil {
		noteCause(r.Context(), err)
	}
}
