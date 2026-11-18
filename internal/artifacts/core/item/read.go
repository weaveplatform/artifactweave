package item

import "github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"

type readLayout struct {
	layout *layout.Layout
	err    error
}

// ReadSession returns a request-local service whose layout snapshot is shared
// by translation, virtual selection and member resolution. The copy owns only
// this map; service dependencies remain shared. Writes and transfers keep their
// ordinary operation lifetime by continuing to use the original service.
// A session is for one sequential request, not concurrent reuse.
func (s *Service) ReadSession() *Service {
	copy := *s
	copy.readLayouts = map[string]readLayout{}
	return &copy
}
