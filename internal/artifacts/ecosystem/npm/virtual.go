package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// VirtualVersion keeps one member's complete manifest, including upstream fields
// whose JSON type differs from local metadata, such as boolean deprecation.
type VirtualVersion struct {
	Manifest         json.RawMessage `json:"manifest"`
	Published        time.Time       `json:"published"`
	Member           string          `json:"member"`
	HasInstallScript bool            `json:"has_install_script"`
	// ExternalTarball is a validated request-local synthetic download path.
	ExternalTarball string `json:"external_tarball,omitempty"`
}

// VirtualPackage is a base-URL/representation-independent merged model. JSON is
// immutable here; rendering decodes a fresh copy before rewriting tarball URLs.
type VirtualPackage struct {
	Name         string                     `json:"name"`
	Fields       map[string]json.RawMessage `json:"fields"`
	Versions     map[string]VirtualVersion  `json:"versions"`
	Tags         map[string]string          `json:"tags"`
	Times        map[string]json.RawMessage `json:"times"`
	Contributors []string                   `json:"contributors"`
}

// Merger applies npm's package-level priority and first-member version ownership.
// Read and source policy have already filtered each member's input.
type Merger struct{ Settings Settings }

// Merge selects contributing versions before deriving tags, so package priority
// also constrains latest selection and keeps complete first-member manifests.
func (m Merger) Merge(ctx context.Context, parts []virtual.Part[VirtualPackage]) (VirtualPackage, error) {
	priority := slices.ContainsFunc(parts, func(p virtual.Part[VirtualPackage]) bool {
		return p.Member.PriorityResolution.Bool() && len(p.Doc.Versions) > 0
	})
	out := VirtualPackage{Versions: map[string]VirtualVersion{}, Tags: map[string]string{}, Times: map[string]json.RawMessage{}}
	owners := map[string]bool{}
	var candidates []Candidate
	tagOwners, timeOwners := map[string]string{}, map[string]string{}
	for _, p := range parts {
		if err := ctx.Err(); err != nil {
			return VirtualPackage{}, fmt.Errorf("npm virtual merge: %w", err)
		}
		if len(p.Doc.Versions) == 0 || priority && !p.Member.PriorityResolution.Bool() {
			continue
		}
		if out.Name == "" {
			out.Name = p.Doc.Name
			out.Fields = map[string]json.RawMessage{}
			for key, raw := range p.Doc.Fields {
				if key != "versions" && key != "dist-tags" && key != "time" {
					out.Fields[key] = bytes.Clone(raw)
				}
			}
			out.Times["unpublished"] = bytes.Clone(p.Doc.Times["unpublished"])
			if len(out.Times["unpublished"]) == 0 {
				out.Times["unpublished"] = json.RawMessage("null")
			}
			owners[p.Member.Key] = true
		}
		for _, number := range slices.Sorted(maps.Keys(p.Doc.Versions)) {
			v := p.Doc.Versions[number]
			if _, present := out.Versions[number]; present {
				continue
			}
			v.Manifest, v.Member = bytes.Clone(v.Manifest), p.Member.Key
			out.Versions[number] = v
			candidates = append(candidates, Candidate{Version: number, Published: v.Published})
			if raw := p.Doc.Times[number]; len(raw) != 0 {
				out.Times[number] = bytes.Clone(raw)
			}
			owners[p.Member.Key] = true
		}
		for tag, number := range p.Doc.Tags {
			if tag == TagLatest {
				continue
			}
			if _, present := p.Doc.Versions[number]; !present {
				continue
			}
			previous, present := out.Tags[tag]
			if !present || m.Settings.MergeDistTagsByVersion && model.CompareSemver(number, previous) > 0 {
				out.Tags[tag], tagOwners[tag] = number, p.Member.Key
			}
		}
		mergeVirtualTimes(&out, p.Doc, p.Member.Key, timeOwners)
	}
	if len(candidates) != 0 {
		out.Tags[TagLatest] = m.Settings.Latest(candidates)
	}
	for _, owner := range tagOwners {
		owners[owner] = true
	}
	for _, owner := range timeOwners {
		owners[owner] = true
	}
	for _, p := range parts {
		if owners[p.Member.Key] && !slices.Contains(out.Contributors, p.Member.Key) {
			out.Contributors = append(out.Contributors, p.Member.Key)
		}
	}
	return out, nil
}

func virtualTime(raw json.RawMessage) (time.Time, bool) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	return at, err == nil
}

func mergeVirtualTimes(out *VirtualPackage, doc VirtualPackage, member string, timeOwners map[string]string) {
	for _, key := range []string{"created", "modified"} {
		at, ok := virtualTime(doc.Times[key])
		if !ok {
			continue
		}
		previous, present := virtualTime(out.Times[key])
		if !present || key == "created" && at.Before(previous) || key == "modified" && at.After(previous) {
			out.Times[key], timeOwners[key] = bytes.Clone(doc.Times[key]), member
		}
	}
}
