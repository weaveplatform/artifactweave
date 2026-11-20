package huggingface

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLead recognizes documented model/dataset metadata within an existing snapshot. A snapshot without it has no substitute lead.
func (Handler) CleanupLead(model.PackageVersion) (format.CleanupLeadSelector, error) {
	return func(file api.IndexFile) api.IndexLead {
		id, revision, name, ok := ParseSnapshotPath(file.Path)
		v := file.Version
		lead := api.IndexLead{Single: true, TieBreak: file.Path}
		if !ok || id.Kind+"/"+id.RepoID() != v.Package.Name || revision != v.Version {
			return lead
		}
		expected := ".jfrog_huggingface_model_info.json"
		if id.Kind == "datasets" {
			expected = ".jfrog_huggingface_dataset_info.json"
		}
		lead.Found = name == expected
		if lead.Found {
			lead.Rank = 1
		} else if id.Kind == "datasets" && name == ".jfrog_huggingface_dataset_info" {
			lead.Found = true
		}
		return lead
	}, nil
}
