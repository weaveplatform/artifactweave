package cocoapods

import (
	"encoding/json"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func gitSource(origin map[string]json.RawMessage) (source.CocoaPodsGitSource, error) {
	var git source.CocoaPodsGitSource
	for key := range origin {
		switch key {
		case "git", "tag", "branch", "commit", "submodules":
		default:
			return git, source.ErrInvalidResult
		}
	}
	data, err := json.Marshal(origin)
	if err != nil || json.Unmarshal(data, &git) != nil || git.Validate() != nil {
		return git, source.ErrInvalidResult
	}
	return git, nil
}

func archiveExtension(fields map[string]json.RawMessage, target string) string {
	var origin map[string]json.RawMessage
	if json.Unmarshal(fields["source"], &origin) == nil {
		if _, ok := origin["git"]; ok {
			return ".tar.gz"
		}
	}
	return sourceExtension(target)
}
