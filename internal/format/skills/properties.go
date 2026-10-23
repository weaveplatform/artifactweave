package skills

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var propertyKeys = []string{
	"skill.name", "skill.version", "skill.description", "skill.author",
	"skill.tags", "skill.displayName", "skill.summary", "skill.changelog",
	"skill.fingerprint",
}

func setProperties(
	ctx context.Context,
	st store.Store,
	it types.Item,
	meta Metadata,
) (Metadata, error) {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return meta, fmt.Errorf("skills: properties of %s: %w", it.FullPath(), err)
	}
	first := func(key string) string {
		if len(current[key]) == 0 {
			return ""
		}
		return current[key][0]
	}
	if meta.Name == "" {
		meta.Name = first("skill.name")
	}
	if meta.Description == "" {
		meta.Description = first("skill.description")
	}
	if meta.Author == "" {
		meta.Author = first("skill.author")
	}
	if len(meta.Tags) == 0 {
		meta.Tags = slices.Clone(current["skill.tags"])
	}
	if meta.DisplayName == "" {
		meta.DisplayName = first("skill.displayName")
	}
	if meta.Summary == "" {
		meta.Summary = first("skill.summary")
	}
	if meta.Changelog == "" {
		meta.Changelog = first("skill.changelog")
	}
	if meta.Name == "" {
		meta.Name = meta.Slug
	}
	wanted := types.Properties{
		"skill.name": {meta.Name}, "skill.version": {meta.Version},
		"skill.fingerprint": {meta.Fingerprint},
	}
	one := func(key, value string) {
		if value != "" {
			wanted[key] = []string{types.TruncatePropertyValue(value, types.PropertyValueMax)}
		}
	}
	one("skill.description", meta.Description)
	one("skill.author", meta.Author)
	one("skill.displayName", meta.DisplayName)
	one("skill.summary", meta.Summary)
	one("skill.changelog", meta.Changelog)
	if len(meta.Tags) > 0 {
		wanted["skill.tags"] = make([]string, 0, len(meta.Tags))
		for _, tag := range meta.Tags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				wanted["skill.tags"] = append(wanted["skill.tags"],
					types.TruncatePropertyValue(tag, types.PropertyValueMax))
			}
		}
		slices.Sort(wanted["skill.tags"])
		wanted["skill.tags"] = slices.Compact(wanted["skill.tags"])
	}
	set := types.Properties{}
	var remove []string
	for _, key := range propertyKeys {
		want, yes := wanted[key]
		have, had := current[key]
		switch {
		case yes && !slices.Equal(have, want):
			set[key] = want
		case !yes && had:
			remove = append(remove, key)
		}
	}
	if len(set) > 0 || len(remove) > 0 {
		if err := st.Items().PatchProperties(ctx, it.ID, set, remove, it.Updated); err != nil {
			return meta, fmt.Errorf("skills: properties of %s: %w", it.FullPath(), err)
		}
	}
	return meta, nil
}
