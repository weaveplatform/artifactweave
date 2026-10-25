package bower

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	bowerfmt "github.com/weaveplatform/artifactweave/internal/format/bower"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxRefsBytes = 1 << 20

var (
	refRE    = regexp.MustCompile(`^[a-f0-9]{44} refs/tags/v?([^\s]+)$`)
	anyRefRE = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{44}) refs/(?:heads|tags)/[^\s]+$`)
)

type remoteRef struct{ Version, Tag string }

func remoteInvalid() error { return source.ErrInvalidResult }

func parseRefs(body []byte) ([]remoteRef, error) {
	if len(body) == 0 || len(body) > maxRefsBytes {
		return nil, remoteInvalid()
	}
	var versions []remoteRef
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "^{}") && anyRefRE.MatchString(line) {
			continue
		}
		match := refRE.FindStringSubmatch(line)
		if match == nil {
			if !anyRefRE.MatchString(line) {
				return nil, remoteInvalid()
			}
			continue
		}
		version := match[1]
		if !bowerfmt.ValidVersion(version) || seen[version] {
			return nil, remoteInvalid()
		}
		seen[version] = true
		versions = append(
			versions,
			remoteRef{Version: version, Tag: strings.SplitN(line, "refs/tags/", 2)[1]},
		)
		if len(versions) > 4096 {
			return nil, remoteInvalid()
		}
	}
	if scanner.Err() != nil {
		return nil, remoteInvalid()
	}
	return versions, nil
}

func validateRefs(
	_ context.Context,
	body io.ReadSeeker,
	size int64,
	_ source.MetadataResponse,
) error {
	if size <= 0 || size > maxRefsBytes {
		return remoteInvalid()
	}
	data, err := io.ReadAll(io.LimitReader(body, maxRefsBytes+1))
	if err != nil || int64(len(data)) != size {
		return remoteInvalid()
	}
	_, err = parseRefs(data)
	return err
}

func (h handler) remoteRefs(r *http.Request, name string) ([]remoteRef, source.Result, error) {
	if gitRemote(r) {
		return h.gitRefs(r, name)
	}
	owner := pkgroute.RequestFrom(r).Repo.Key
	resource := source.Resource{
		Kind: source.Metadata, Path: "catalog/" + name + "/refs",
		UpstreamPath: "refs/" + owner + "/" + name,
		Method:       http.MethodGet, PermissionFolder: true, PermissionPath: name,
		Variant: "bower-refs-v1", ValidateMetadata: validateRefs,
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, source.Result{}, fmt.Errorf("bower remote refs: %w", err)
	}
	if result.Stream == nil {
		return nil, source.Result{}, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxRefsBytes+1))
	if err != nil {
		return nil, source.Result{}, remoteInvalid()
	}
	vs, err := parseRefs(data)
	if err != nil {
		return nil, source.Result{}, err
	}
	return vs, result, nil
}

func (h handler) remoteVersions(r *http.Request, name string) ([]string, error) {
	vs, result, err := h.remoteRefs(r, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		manifest := bowerfmt.ManifestPath(name, v.Version)
		if !h.k.Can(r, manifest, types.ActionRead) {
			continue
		}
		if result.CheckRead != nil {
			if err := result.CheckRead(r.Context(), manifest); err != nil {
				if errors.Is(err, source.ErrDenied) {
					continue
				}
				return nil, fmt.Errorf("bower remote refs authority: %w", err)
			}
		}
		out = append(out, v.Version)
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return nil, fmt.Errorf("bower remote refs revalidate: %w", err)
		}
	}
	return out, nil
}

func (h handler) remotePackageNames(r *http.Request, query string) ([]string, error) {
	if gitRemote(r) {
		return h.gitPackageNames(r, query)
	}
	resource := source.Resource{
		Kind: source.Metadata, Path: "catalog/packages.json", UpstreamPath: "packages",
		Method: http.MethodGet, PermissionFolder: true, PermissionPath: "",
		Variant: "bower-packages-v1", ValidateMetadata: validatePackages,
	}
	result, err := h.k.Read(r, resource)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bower remote packages: %w", err)
	}
	if result.Stream == nil {
		return nil, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxRefsBytes+1))
	if err != nil {
		return nil, remoteInvalid()
	}
	return parsePackages(data)
}

func validatePackages(
	_ context.Context,
	body io.ReadSeeker,
	size int64,
	_ source.MetadataResponse,
) error {
	if size < 0 || size > maxRefsBytes {
		return remoteInvalid()
	}
	data, err := io.ReadAll(io.LimitReader(body, maxRefsBytes+1))
	if err != nil || int64(len(data)) != size {
		return remoteInvalid()
	}
	_, err = parsePackages(data)
	return err
}

func parsePackages(data []byte) ([]string, error) {
	var entries []packageEntry
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) > 4096 {
		return nil, remoteInvalid()
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		if !bowerfmt.ValidName(e.Name) || !strings.HasPrefix(e.URL, "art://") || seen[e.Name] {
			return nil, remoteInvalid()
		}
		seen[e.Name] = true
		out = append(out, e.Name)
	}
	slices.Sort(out)
	return out, nil
}

//nolint:gocyclo // Header, path and manifest checks are independent archive admission gates.
func validateArchive(
	name, version string,
	optionalVersion ...bool,
) func(context.Context, io.ReadSeeker, int64) error {
	gitArchive := len(optionalVersion) > 0 && optionalVersion[0]
	return func(_ context.Context, body io.ReadSeeker, size int64) error {
		if size <= 0 || size > maxArchiveBytes {
			return remoteInvalid()
		}
		gz, err := gzip.NewReader(io.LimitReader(body, maxArchiveBytes+1))
		if err != nil {
			return remoteInvalid()
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		var root string
		manifest := false
		seen := map[string]bool{}
		var total int64
		globalHeaders := 0
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil || h.Size < 0 {
				return remoteInvalid()
			}
			if h.Typeflag == tar.TypeXGlobalHeader {
				// GitHub tarballs carry a global comment. Reject PAX keys that
				// could change extraction paths or links in a different tar reader.
				globalHeaders++
				total += h.Size
				if globalHeaders > 8 || h.Size > 4096 || total > maxArchiveBytes {
					return remoteInvalid()
				}
				pax, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
				if err != nil || int64(len(pax)) != h.Size || !safePAXGlobal(pax) {
					return remoteInvalid()
				}
				continue
			}
			p := strings.TrimSuffix(h.Name, "/")
			if strings.HasPrefix(p, "/") || strings.ContainsRune(p, '\\') ||
				strings.ContainsRune(p, 0) {
				return remoteInvalid()
			}
			parts := strings.Split(p, "/")
			for _, part := range parts {
				if part == "" || part == "." || part == ".." {
					return remoteInvalid()
				}
			}
			if root == "" {
				root = parts[0]
			}
			if parts[0] != root {
				return remoteInvalid()
			}
			if len(parts) == 1 && h.Typeflag == tar.TypeDir {
				continue
			}
			if len(parts) < 2 {
				return remoteInvalid()
			}
			rel := strings.Join(parts[1:], "/")
			if _, _, _, ok := bowerfmt.ParsePath(name + "/" + version + "/" + rel); !ok {
				return remoteInvalid()
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != 0 || seen[rel] {
				return remoteInvalid()
			}
			seen[rel] = true
			total += h.Size
			if len(seen) > maxArchiveFiles || total > maxArchiveBytes {
				return remoteInvalid()
			}
			if rel == "bower.json" {
				if h.Size > bowerfmt.MaxManifestBytes {
					return remoteInvalid()
				}
				data, err := io.ReadAll(io.LimitReader(tr, bowerfmt.MaxManifestBytes+1))
				if err != nil || int64(len(data)) != h.Size {
					return remoteInvalid()
				}
				m, err := bowerfmt.DecodeManifest(data)
				if gitArchive {
					m, err = bowerfmt.DecodeGitManifest(data)
				}
				if err != nil || m.Name != name ||
					(m.Version != version && (!gitArchive || m.Version != "")) {
					return remoteInvalid()
				}
				manifest = true
			} else {
				if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
					return remoteInvalid()
				}
			}
		}
		if !manifest {
			return remoteInvalid()
		}
		// tar stops at its zero blocks; draining gzip also verifies its CRC and
		// catches a truncated or corrupt compressed trailer.
		remaining := maxArchiveBytes - total
		n, err := io.Copy(io.Discard, io.LimitReader(gz, remaining+1))
		if err != nil || n > remaining {
			return remoteInvalid()
		}
		return nil
	}
}

func safePAXGlobal(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	for len(data) > 0 {
		space := bytes.IndexByte(data, ' ')
		if space <= 0 || space > 5 {
			return false
		}
		n, err := strconv.Atoi(string(data[:space]))
		if err != nil || n <= space+2 || n > len(data) {
			return false
		}
		record := data[space+1 : n]
		key, value, ok := bytes.Cut(bytes.TrimSuffix(record, []byte{'\n'}), []byte{'='})
		if !ok || !bytes.HasSuffix(record, []byte{'\n'}) || string(key) != "comment" ||
			len(value) > 1024 {
			return false
		}
		data = data[n:]
	}
	return true
}

func (h handler) remoteBinary(w http.ResponseWriter, r *http.Request, name, version, tag string) {
	if gitRemote(r) {
		h.gitBinary(w, r, name, version)
		return
	}
	owner := pkgroute.RequestFrom(r).Repo.Key
	refs, _, err := h.remoteRefs(r, name)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	found := false
	for _, ref := range refs {
		if ref.Version == version {
			tag, found = ref.Tag, true
			break
		}
	}
	if !found {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower version not found"))
		return
	}
	resource := source.Resource{
		Kind:         source.Artifact,
		Path:         name + "/" + version + "/bower-archive.tar.gz",
		UpstreamPath: "binaries/" + owner + "/" + name + ".git/" + tag,
		Method:       r.Method,
		Variant:      "bower-archive-v1",
		Validate:     validateArchive(name, version),
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}
