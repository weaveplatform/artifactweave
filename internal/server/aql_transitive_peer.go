package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type peerAQLRow struct {
	Repo         string    `json:"repo"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Size         int64     `json:"size"`
	Created      time.Time `json:"created"`
	CreatedBy    string    `json:"created_by"`
	Modified     time.Time `json:"modified"`
	ModifiedBy   string    `json:"modified_by"`
	Updated      time.Time `json:"updated"`
	ActualSHA1   string    `json:"actual_sha1"`
	ActualMD5    string    `json:"actual_md5"`
	SHA256       string    `json:"sha256"`
	OriginalSHA1 string    `json:"original_sha1"`
	OriginalMD5  string    `json:"original_md5"`
	Properties   []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"properties"`
}

func (s artifactoryService) transitivePeer(
	ctx context.Context,
	p auth.Principal,
	c repo.Config,
	q *aql.Query,
	limit int,
) ([]aql.Hit, source.Result, error) {
	var zero source.Result
	if s.cfg.RemoteSources == nil {
		return nil, zero, source.ErrUnavailable
	}
	peer, err := remote.ParseSmartPeer(c)
	if err != nil {
		return nil, zero, fmt.Errorf("transitive peer: %w", err)
	}
	text, err := q.PeerQuery(peer.Repository, limit)
	if err != nil {
		return nil, zero, fmt.Errorf("transitive peer: %w", err)
	}
	var hits []aql.Hit
	resource := source.Resource{
		Kind:                source.Metadata,
		Path:                ".artifactweave-aql-query",
		PermissionFolder:    true,
		Method:              http.MethodGet,
		RequireSmart:        true,
		SmartAQL:            text,
		ExternalURL:         peer.BaseURL + "/api/search/aql",
		Variant:             "smart-aql-v1",
		ExpectedContentType: "application/json",
		ValidateMetadata: func(_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			var e error
			hits, e = decodePeerAQL(r, size, peer.Repository, c.Key, limit)
			return e
		},
	}
	result, err := s.cfg.RemoteSources.Read(
		ctx,
		source.Request{
			Repo:     c,
			Subject:  p.Subject(),
			Access:   p.Access(),
			Project:  c.ProjectKey,
			Resource: resource,
		},
	)
	if err != nil {
		return nil, zero, fmt.Errorf("transitive peer: %w", err)
	}
	if result.Stream == nil || result.Revalidate == nil || result.CheckRead == nil {
		if result.Stream != nil {
			_ = result.Stream.Body.Close()
		}
		return nil, zero, source.ErrInvalidResult
	}
	_ = result.Stream.Body.Close()
	result.Stream = nil
	return hits, result, nil
}

func decodePeerAQL(r io.Reader, size int64, peer, key string, limit int) ([]aql.Hit, error) {
	if size < 0 || size > 8<<20 {
		return nil, source.ErrInvalidResult
	}
	var doc struct {
		Results []peerAQLRow      `json:"results"`
		Errors  []json.RawMessage `json:"errors"`
		Range   *struct {
			Start        int    `json:"start_pos"`
			End          int    `json:"end_pos"`
			Total        int    `json:"total"`
			Notification string `json:"notification"`
		} `json:"range"`
	}
	decoder := json.NewDecoder(io.LimitReader(r, 8<<20+1))
	if err := decoder.Decode(&doc); err != nil {
		return nil, source.ErrInvalidResult
	}
	var trailing any
	if err := decoder.Decode(
		&trailing,
	); err != io.EOF || doc.Results == nil || len(doc.Results) > limit || doc.Range == nil || doc.Range.Start != 0 || doc.Range.End != len(doc.Results) || doc.Range.Total != len(doc.Results) ||
		doc.Range.Notification != "" || len(doc.Errors) > 0 {
		return nil, source.ErrInvalidResult
	}
	hits := make([]aql.Hit, 0, len(doc.Results))
	seen := make(map[string]aql.Hit)
	for _, row := range doc.Results {
		hit, err := row.hit(peer, key)
		if err != nil {
			return nil, err
		}
		if previous, ok := seen[hit.Item.FullPath()]; ok {
			if !reflect.DeepEqual(previous, hit) {
				return nil, source.ErrInvalidResult
			}
		}
		seen[hit.Item.FullPath()] = hit
		hits = append(hits, hit)
	}
	return hits, nil
}

func (r peerAQLRow) hit(peer, key string) (aql.Hit, error) {
	if r.Repo != peer || r.Size < 0 || r.Type != "file" && r.Type != "folder" ||
		strings.ContainsAny(r.Path+r.Name, "\r\n%?#;") ||
		len(r.Path)+len(r.Name) > 4096 ||
		strings.ContainsAny(r.Name, "/\\") {
		return aql.Hit{}, source.ErrInvalidResult
	}
	it := types.Item{
		RepoKey:    key,
		Path:       r.Path,
		Name:       r.Name,
		Folder:     r.Type == "folder",
		Size:       r.Size,
		Created:    r.Created,
		CreatedBy:  r.CreatedBy,
		Modified:   r.Modified,
		ModifiedBy: r.ModifiedBy,
		Updated:    r.Updated,
		Actual:     types.Checksums{SHA1: r.ActualSHA1, MD5: r.ActualMD5, SHA256: r.SHA256},
		Original:   types.Checksums{SHA1: r.OriginalSHA1, MD5: r.OriginalMD5},
	}
	if !it.IsRoot() {
		parent, name, depth, err := types.SplitItemPath(it.FullPath())
		if err != nil || parent != it.Path || name != it.Name {
			return aql.Hit{}, source.ErrInvalidResult
		}
		it.Depth = depth
	} else if !it.Folder {
		return aql.Hit{}, source.ErrInvalidResult
	}
	for _, digest := range []struct {
		value string
		size  int
	}{{r.ActualSHA1, 20}, {r.ActualMD5, 16}, {r.SHA256, 32}, {r.OriginalSHA1, 20}, {r.OriginalMD5, 16}} {
		if digest.value != "" {
			b, err := hex.DecodeString(digest.value)
			if err != nil || len(b) != digest.size {
				return aql.Hit{}, source.ErrInvalidResult
			}
		}
	}
	if len(r.Properties) > 4096 {
		return aql.Hit{}, source.ErrInvalidResult
	}
	properties := make(types.Properties)
	for _, property := range r.Properties {
		if types.ValidatePropertyKey(property.Key) != nil || len(property.Value) > 16384 {
			return aql.Hit{}, source.ErrInvalidResult
		}
		properties.Set(property.Key, append(properties[property.Key], property.Value)...)
	}
	if len(properties) > 1024 {
		return aql.Hit{}, source.ErrInvalidResult
	}
	return aql.Hit{Item: it, Properties: properties}, nil
}
