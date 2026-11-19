package inmem

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (p projectStore) RoleProjects(ctx context.Context, q port.ProjectRoleQuery) ([]model.Project, bool, error) {
	if q.Limit < 1 {
		return nil, false, fmt.Errorf("project page limit must be positive: %w", kernel.ErrInvalid)
	}
	compare := func(a, b model.Project) int {
		c := strings.Compare(a.DisplayName, b.DisplayName)
		if c == 0 {
			c = strings.Compare(a.Key, b.Key)
		}
		if q.Descending {
			return -c
		}
		return c
	}
	limit := q.Limit
	if limit < math.MaxInt {
		limit++
	}
	var out []model.Project
	err := p.s.Do(ctx, func() error {
		for _, project := range p.projects().Rows {
			if q.AfterKey != "" && compare(project, model.Project{DisplayName: q.AfterName, Key: q.AfterKey}) <= 0 {
				continue
			}
			used := false
			for key, member := range p.members().Rows {
				if key.project == project.Key && (key.typ == model.PrincipalUser || key.typ == model.PrincipalGroup) && slices.Contains(member.Roles, q.RoleName) {
					used = true
					break
				}
			}
			if used {
				i, _ := slices.BinarySearchFunc(out, project, compare)
				out = slices.Insert(out, i, project)
				if len(out) > limit {
					out = out[:limit]
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return kernel.Clone(out), more, nil
}
