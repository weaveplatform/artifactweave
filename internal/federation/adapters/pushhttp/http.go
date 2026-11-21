package pushhttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artifactory "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

const metadataLimit = 4 << 20

var errUnavailable = errors.New("replication target temporarily unavailable")

type connection struct {
	endpoint replication.PushEndpoint
	client   *outbound.Client
	inTx     func(context.Context) bool
	revision string
	username string
	password string
}

var _ port.PushConnection = (*connection)(nil)

func (c *connection) Revision() string { return c.revision }

func (c *connection) request(ctx context.Context, method string, target *url.URL, body io.Reader, length int64, headers http.Header) (*http.Response, error) {
	if ctx == nil || c.inTx(ctx) {
		return nil, fmt.Errorf("replication network call inside metadata transaction: %w", kernel.ErrInvalid)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, port.PushTargetInvalid
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if length >= 0 {
		req.ContentLength = length
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange replication target: %w", err)
	}
	return response, nil
}

func statusError(status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return port.PushTargetUnauthorized
	case status == http.StatusForbidden:
		return port.PushTargetForbidden
	case status == http.StatusNotFound:
		return kernel.ErrNotFound
	case status == http.StatusTooManyRequests || status >= 500:
		return errUnavailable
	default:
		return port.PushTargetInvalid
	}
}

// decodeDocument bounds metadata, rejects trailing/truncated JSON, and never
// incorporates an untrusted response body or decoder error in retained errors.
func decodeDocument(body io.Reader, value any) error {
	limited := &io.LimitedReader{R: body, N: metadataLimit + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(value); err != nil {
		return documentError(err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return documentError(err)
	}
	if limited.N <= 0 {
		return port.PushTargetInvalid
	}
	return nil
}

func documentError(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, outbound.ErrConnection, outbound.ErrSecurity, outbound.ErrClosed} {
		if errors.Is(err, known) {
			return known
		}
	}
	return port.PushTargetInvalid
}

func (c *connection) info(ctx context.Context, path string) (port.PushTargetItem, error) {
	target, err := c.endpoint.StorageRootURL(path)
	if err != nil {
		return port.PushTargetItem{}, fmt.Errorf("address replication metadata: %w", err)
	}
	response, err := c.request(ctx, http.MethodGet, target, nil, 0, nil)
	if err != nil {
		return port.PushTargetItem{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return port.PushTargetItem{}, statusError(response.StatusCode)
	}
	var document map[string]json.RawMessage
	if err := decodeDocument(response.Body, &document); err != nil {
		return port.PushTargetItem{}, fmt.Errorf("decode target item metadata: %w", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return port.PushTargetItem{}, fmt.Errorf("encode target metadata document: %w", port.PushTargetInvalid)
	}
	var info artifactory.FileInfo
	if json.Unmarshal(encoded, &info) != nil || info.Repo == nil || *info.Repo != c.endpoint.Repository() ||
		info.Path == nil || *info.Path != "/"+path || info.LastModified == nil || info.LastUpdated == nil {
		return port.PushTargetItem{}, fmt.Errorf("validate target item namespace and timestamps: %w", port.PushTargetInvalid)
	}
	snapshot := model.PushTargetSnapshot{Modified: *info.LastModified, Updated: *info.LastUpdated}
	if children, folder := document["children"]; folder {
		var entries []artifactory.FolderChild
		if json.Unmarshal(children, &entries) != nil || entries == nil || info.Size != nil || info.Checksums != nil {
			return port.PushTargetItem{}, fmt.Errorf("validate target folder metadata: %w", port.PushTargetInvalid)
		}
		snapshot.Folder = true
	} else {
		if info.Size == nil || info.Checksums == nil || info.Checksums.Sha256 == nil {
			return port.PushTargetItem{}, fmt.Errorf("target omitted computed file size or checksum: %w", port.PushTargetInvalid)
		}
		snapshot.Size, err = strconv.ParseInt(*info.Size, 10, 64)
		if err != nil || snapshot.Size < 0 || !digest(*info.Checksums.Sha256, 32) {
			return port.PushTargetItem{}, port.PushTargetInvalid
		}
		snapshot.Actual.SHA256 = strings.ToLower(*info.Checksums.Sha256)
		for _, checksum := range []struct {
			from *string
			to   *string
			size int
		}{{info.Checksums.Sha1, &snapshot.Actual.SHA1, 20}, {info.Checksums.Md5, &snapshot.Actual.MD5, 16}} {
			if checksum.from != nil {
				if !digest(*checksum.from, checksum.size) {
					return port.PushTargetItem{}, port.PushTargetInvalid
				}
				*checksum.to = strings.ToLower(*checksum.from)
			}
		}
	}
	return port.PushTargetItem{Path: path, Snapshot: snapshot}, nil
}

func digest(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size
}

func (c *connection) Inspect(ctx context.Context, path string) (port.PushTargetItem, error) {
	if _, err := c.endpoint.ItemURL(path); err != nil {
		return port.PushTargetItem{}, fmt.Errorf("inspect replication path: %w", err)
	}
	item, err := c.info(ctx, path)
	if err != nil {
		return port.PushTargetItem{}, err
	}
	item.Properties, err = c.properties(ctx, path)
	if err != nil {
		return port.PushTargetItem{}, err
	}
	current, err := c.info(ctx, path)
	if err != nil {
		return port.PushTargetItem{}, err
	}
	if !sameSnapshot(item.Snapshot, current.Snapshot) {
		return port.PushTargetItem{}, fmt.Errorf("replication target changed during metadata read: %w", kernel.ErrConflict)
	}
	return item, nil
}

func (c *connection) properties(ctx context.Context, path string) (map[string][]string, error) {
	target, err := c.endpoint.StorageURL(path)
	if err != nil {
		return nil, fmt.Errorf("address replication properties: %w", err)
	}
	target.RawQuery = "properties"
	response, err := c.request(ctx, http.MethodGet, target, nil, 0, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		var refusal struct {
			Errors []struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := decodeDocument(response.Body, &refusal); err != nil {
			return nil, err
		}
		if len(refusal.Errors) == 1 &&
			refusal.Errors[0].Status == http.StatusNotFound && refusal.Errors[0].Message == "No properties could be found." {
			// Inspect performs a second info read even for this special 404:
			// missing metadata alone cannot establish a still-existing item.
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("read replication properties: %w", kernel.ErrNotFound)
	}
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response.StatusCode)
	}
	var document artifactory.ItemProperties
	if err := decodeDocument(response.Body, &document); err != nil {
		return nil, err
	}
	if document.Properties == nil {
		return nil, port.PushTargetInvalid
	}
	return *document.Properties, nil
}

func (c *connection) Put(ctx context.Context, item artifactsapi.TransferItem, body io.ReadSeeker) error {
	target, err := c.endpoint.ItemURL(item.Path)
	if err != nil {
		return fmt.Errorf("address replication upload: %w", err)
	}
	length := item.Size
	headers := make(http.Header)
	var reader io.Reader
	if item.Folder {
		if body != nil {
			return port.PushTargetInvalid
		}
		target.Path += "/"
		target.RawPath += "/"
		length = 0
	} else {
		if body == nil || length < 0 || !digest(item.Actual.SHA256, 32) {
			return port.PushTargetInvalid
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind replication content: %w", outbound.SafeError(err))
		}
		// net/http closes request bodies. Exposing only Reader keeps the
		// source's ReadSeekCloser owned by the durable driver after failure.
		reader = struct{ io.Reader }{body}
		headers.Set("Content-Type", "application/octet-stream")
		headers.Set("X-Checksum-Sha256", item.Actual.SHA256)
		if item.Actual.SHA1 != "" {
			headers.Set("X-Checksum-Sha1", item.Actual.SHA1)
		}
		if item.Actual.MD5 != "" {
			headers.Set("X-Checksum-Md5", item.Actual.MD5)
		}
	}
	response, err := c.request(ctx, http.MethodPut, target, reader, length, headers)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return statusError(response.StatusCode)
	}
	return nil
}

func (c *connection) Properties(ctx context.Context, path string, desired map[string][]string) error {
	observed, err := c.Inspect(ctx, path)
	if err != nil {
		return err
	}
	props := make(map[string]any, len(observed.Properties)+len(desired))
	for key := range observed.Properties {
		if _, keep := desired[key]; !keep {
			props[key] = nil
		}
	}
	for key, values := range desired {
		if len(values) == 0 {
			return port.PushTargetMetadata
		}
		props[key] = values
	}
	if len(props) == 0 {
		return nil
	}
	body, err := json.Marshal(struct {
		Props map[string]any `json:"props"`
	}{props})
	if err != nil {
		return port.PushTargetMetadata
	}
	target, err := c.endpoint.MetadataURL(path)
	if err != nil {
		return fmt.Errorf("address replication property update: %w", err)
	}
	target.RawQuery = "recursiveProperties=0"
	response, err := c.request(ctx, http.MethodPatch, target, bytes.NewReader(body), int64(len(body)), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return statusError(response.StatusCode)
	}
	return nil
}

func sameSnapshot(a, b model.PushTargetSnapshot) bool {
	return a.Folder == b.Folder && a.Size == b.Size && a.Actual == b.Actual && a.Modified.Equal(b.Modified) && a.Updated.Equal(b.Updated)
}

func (c *connection) Delete(ctx context.Context, path string, expected model.PushTargetSnapshot) error {
	observed, err := c.Inspect(ctx, path)
	if err != nil {
		return err
	}
	if expected.Folder || observed.Snapshot.Folder || !sameSnapshot(expected, observed.Snapshot) {
		return fmt.Errorf("replication deletion snapshot changed: %w", kernel.ErrConflict)
	}
	target, err := c.endpoint.ItemURL(path)
	if err != nil {
		return fmt.Errorf("address replication deletion: %w", err)
	}
	// The native API has no documented conditional checksum delete. This
	// fresh check narrows the race; it cannot atomically exclude remote writers.
	response, err := c.request(ctx, http.MethodDelete, target, nil, 0, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return statusError(response.StatusCode)
	}
	return nil
}
