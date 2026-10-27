package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Settings controls how the protocol serves the stored index. Nil retains
// Artifactory's default: chart URLs relative to the repository URL.
type Settings struct{ RelativeURLs *bool }

// IsRelative reports whether chart URLs remain relative.
func (s Settings) IsRelative() bool { return s.RelativeURLs == nil || *s.RelativeURLs }

// Index is Helm's repository index. Stored chart URLs are relative; only
// the protocol's absolute mode rewrites them for the requesting proxy.
//
//nolint:tagliatelle // Helm's wire names
type Index struct {
	APIVersion string                    `json:"apiVersion" yaml:"apiVersion"`
	Entries    map[string][]ChartVersion `json:"entries"    yaml:"entries"`
	Generated  string                    `json:"generated"  yaml:"generated"`
}

// ChartVersion adds the archive's location and digest to Chart.yaml.
type ChartVersion struct {
	Chart   `         yaml:",inline"`
	Created string   `yaml:"created" json:"created"`
	Digest  string   `yaml:"digest"  json:"digest"`
	URLs    []string `yaml:"urls"    json:"urls"`
}

// EncodeIndex writes Helm's fields alphabetically, omitting absent chart
// fields as Helm's JSON model does. Versions remain quoted even when they
// resemble numbers, booleans or scientific notation (RTFACT-20334).
func EncodeIndex(index Index) ([]byte, error) {
	raw, err := json.Marshal(index)
	if err != nil {
		return nil, fmt.Errorf("helm: encode index: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("helm: index fields: %w", err)
	}
	var node yaml.Node
	if err := node.Encode(fields); err != nil {
		return nil, fmt.Errorf("helm: index yaml: %w", err)
	}
	quoteVersions(&node)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, fmt.Errorf("helm: write index: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("helm: close index: %w", err)
	}
	return out.Bytes(), nil
}

func quoteVersions(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if (key.Value == "version" || key.Value == "appVersion") &&
				value.Kind == yaml.ScalarNode {
				value.Style = yaml.DoubleQuotedStyle
			}
		}
	}
	for _, child := range n.Content {
		quoteVersions(child)
	}
}

func chartURL(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	// A colon in the first segment must not turn a relative chart path
	// into a URI scheme when Helm resolves it against the repository URL.
	parts[0] = strings.ReplaceAll(parts[0], ":", "%3A")
	return strings.Join(parts, "/")
}

func indexStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Work derives one repository index from cached package metadata, without
// opening chart archives. A removed repository no longer owes this work.
func (Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("helm: repository %s: %w", work.Key, err)
	case !Serves(c.PackageType) || !derives(c, false):
		return nil
	}
	out := Index{
		APIVersion: "v1",
		Entries:    map[string][]ChartVersion{},
		Generated:  indexStamp(env.Now()),
	}
	page := store.Page{Limit: store.MaxLimit}
	for {
		packages, next, err := env.Store.Packages().List(ctx, c.Key, page)
		if err != nil {
			return fmt.Errorf("helm: list packages: %w", err)
		}
		for _, p := range packages {
			versions, err := env.Store.Packages().Versions(ctx, p.ID)
			if err != nil {
				return fmt.Errorf("helm: versions of %s: %w", p.Name, err)
			}
			for _, v := range versions {
				m, err := DecodeMetadata(v.Metadata)
				if err != nil {
					return err
				}
				out.Entries[p.Name] = append(out.Entries[p.Name], ChartVersion{
					Chart:   m.Chart,
					Created: indexStamp(m.Created),
					Digest:  m.Digest,
					URLs:    []string{chartURL(m.Path)},
				})
			}
			slices.SortStableFunc(
				out.Entries[p.Name],
				func(a, b ChartVersion) int { return types.CompareSemver(b.Version, a.Version) },
			)
		}
		if next == "" {
			break
		}
		page.After = next
	}
	data, err := EncodeIndex(out)
	if err != nil {
		return err
	}
	if _, err := env.Derive(
		ctx,
		format.Derived{Repo: c, Path: IndexPath, Body: bytes.NewReader(data), By: "_system_"},
	); err != nil {
		return fmt.Errorf("helm: derive index: %w", err)
	}
	return nil
}

// Resolve seeds work for a cold index before the item service calls
// EnsureFresh. A parked item is deliberately left parked: reading must
// surface its failure, not silently reset its retry budget.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	if !derives(c, d.Cached) || d.Path != IndexPath {
		return d.Path, nil
	}
	_, err := env.Store.Items().Get(ctx, c.Key, false, IndexPath)
	switch {
	case err == nil:
		return d.Path, nil
	case !errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("helm: read index: %w", err)
	}
	_, err = env.Store.WorkQueue().Get(ctx, KindIndex, c.Key)
	switch {
	case err == nil:
		return d.Path, nil
	case !errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("helm: index work: %w", err)
	}
	if err := queueIndex(ctx, env, c, false); err != nil {
		return "", err
	}
	return d.Path, nil
}

// Classify keeps the root index out of replication: each repository
// derives its own metadata from the charts it receives.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if path == IndexPath {
		return format.ClassDerived
	}
	return format.ClassUnique
}
