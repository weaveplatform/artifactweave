package api

// API is the artifacts module's published surface for other modules: one
// implementation of each contract, as the module binds them.
type API struct {
	Repositories Repositories
	Layouts      Layouts
}
