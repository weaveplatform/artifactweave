package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
)

func (c *RegistryContent) pages(
	ctx context.Context,
	client remote.RegistryClient,
	image, operation, reference string,
	res *http.Response,
) (*http.Response, error) {
	if res.Header.Get("Link") == "" {
		return res, nil
	}
	var tags []string
	var descriptors []Descriptor
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
			return nil, remote.ErrRequest
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
				return nil, remote.ErrRequest
			}
			tags = append(tags, listing.Tags...)
		} else {
			manifest, parseErr := ParseManifest(res.Header.Get("Content-Type"), body, "")
			if parseErr != nil || !manifest.IsIndex() {
				return nil, remote.ErrRequest
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
			return nil, remote.ErrRequest
		}
		seen[query] = true
		res, err = client.Do(
			ctx,
			image,
			remote.Request{
				Method:   http.MethodGet,
				Path:     operation + "/" + reference,
				Query:    query,
				Metadata: true,
				Header:   http.Header{"Accept": {MediaTypeOCIIndex}},
			},
		)
		if err != nil {
			//nolint:wrapcheck // preserves upstream error identity
			return nil, err
		}
		if res.StatusCode != 200 {
			_ = res.Body.Close()
			return nil, remote.ResponseError(res, policyNow(c.Policy))
		}
	}
	var data []byte
	if operation == "tags" {
		data, _ = json.Marshal(map[string]any{"name": image, "tags": tags})
	} else {
		data, _ = json.Marshal(
			map[string]any{
				"schemaVersion": 2,
				"mediaType":     MediaTypeOCIIndex,
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

// policyNow is the shared policy's clock as (*remote.Policy).now reads it,
// which this package cannot call: the injected Now first, else the wall
// clock, in UTC. It must stay in step with that method, or a Retry-After
// read here would be judged against another clock than the policy's own.
func policyNow(p *remote.Policy) time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
