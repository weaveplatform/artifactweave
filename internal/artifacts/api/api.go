package api

// API is the artifacts module's published surface for other modules: one
// implementation of each contract, as the module binds them.
type API struct {
	Repositories   Repositories
	Lifetimes      RepositoryLifetimes
	Layouts        Layouts
	PackageIndex   PackageIndex
	TransferSource TransferSource
	PullSource     PullSource
	ItemRemoval    ItemRemoval
	// StoragePolicies is the garbage collection schedule and trash can
	// policy, which the configuration descriptor applies and exports.
	StoragePolicies StoragePolicies
}
