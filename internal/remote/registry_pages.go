package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
)

func (c *RegistryContent) pages(
	ctx context.Context,
	client *Registry,
	image, operation, reference string,
	res *http.Response,
) (*http.Response, error) {
	if res.Header.Get("Link") == "" {
		return res, nil
	}
	var tags []string
	var descriptors []layout.Descriptor
	seen := map[string]bool{}
	var total int
	first := res
	for page := 0; ; page++ {
		body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
		_ = res.Body.Close()
		total += len(body)
		if err != nil {
			return nil, fmt.Errorf("read registry listing: %w", err)
		}
		if total > 16<<20 || page >= 128 {
			return nil, ErrRequest
		}
		// The assembled listing must not persist a page that upstream marked
		// private to a request. Its validators no longer describe combined bytes.
		if strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-store") ||
			res.Header.Get("Vary") != "" {
			first.Header.Set("Cache-Control", "no-store")
		}
		if operation == "tags" {
			var listing struct {
				Tags []string `json:"tags"`
			}
			if err = json.Unmarshal(body, &listing); err != nil {
				return nil, ErrRequest
			}
			tags = append(tags, listing.Tags...)
		} else {
			manifest, parseErr := layout.ParseManifest(res.Header.Get("Content-Type"), body, "")
			if parseErr != nil || !manifest.Index() {
				return nil, ErrRequest
			}
			descriptors = append(descriptors, manifest.Manifests...)
		}
		query, err := registryNext(client, image, operation, reference, res.Header.Get("Link"))
		if err != nil {
			return nil, err
		}
		if query == "" {
			break
		}
		if seen[query] {
			return nil, ErrRequest
		}
		seen[query] = true
		res, err = client.Do(
			ctx,
			image,
			Request{
				Method:   http.MethodGet,
				Path:     operation + "/" + reference,
				Query:    query,
				Metadata: true,
				Header:   http.Header{"Accept": {layout.MediaTypeOCIIndex}},
			},
		)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != 200 {
			_ = res.Body.Close()
			return nil, responseError(res, c.Policy.now())
		}
	}
	var data []byte
	if operation == "tags" {
		data, _ = json.Marshal(map[string]any{"name": image, "tags": tags})
	} else {
		data, _ = json.Marshal(
			map[string]any{
				"schemaVersion": 2,
				"mediaType":     layout.MediaTypeOCIIndex,
				"manifests":     descriptors,
			},
		)
	}
	first.Body = io.NopCloser(bytes.NewReader(data))
	first.ContentLength = int64(len(data))
	first.Header.Set("Content-Length", strconv.Itoa(len(data)))
	for _, key := range []string{"Docker-Content-Digest", "X-Checksum-Sha256", "ETag", "Last-Modified", "Link"} {
		first.Header.Del(key)
	}
	return first, nil
}
