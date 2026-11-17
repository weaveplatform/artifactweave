package format

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// VirtualSelector is a format whose virtual downloads compare member results.
// A nil selection keeps the engine's ordinary first-hit lookup.
type VirtualSelector interface {
	SelectVirtual(context.Context, Env, repo.Config, string) (*VirtualSelection, error)
}

// VirtualCandidate describes an authorized member result without its content.
// The HTTP walk owns streams and closes every candidate it does not return.
type VirtualCandidate struct {
	Repo     repo.Config
	Path     string
	Modified time.Time
	Exact    bool
}

// VirtualChoice is the format's decision and the explanation a trace records.
type VirtualChoice struct {
	Take, Stop bool
	Steps      []string
}

// VirtualSelection keeps format policy out of the member walk. Skip returns
// trace steps when a member is excluded. Choose compares an authorized candidate
// with the current winner (nil initially). DirectRemotes bypasses standalone
// cache steps, letting each remote choose its own copy through its source policy.
type VirtualSelection struct {
	DirectRemotes bool
	Skip          func(repo.Config) []string
	Choose        func(context.Context, VirtualCandidate, *VirtualCandidate) (VirtualChoice, error)
	Missing       string
	Returning     func(VirtualCandidate) string
}
