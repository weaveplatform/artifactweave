package api

// LayoutDefinition is a repository layout as the configuration descriptor
// stores it (repoLayouts.<name>): its name, the artifact and descriptor path
// patterns, and the integration-revision expressions. It has the fields of
// the layout engine's Definition (artifacts/core/layout), in the same order
// and of the same types, so the engine converts it with a plain conversion,
// which stops compiling if either struct changes.
type LayoutDefinition struct {
	Name                             string
	ArtifactPathPattern              string
	DistinctiveDescriptorPathPattern bool
	DescriptorPathPattern            string
	FolderIntegrationRevisionRegExp  string
	FileIntegrationRevisionRegExp    string
}

// Layouts checks layout definitions for the configuration module, which
// stores them.
type Layouts interface {
	// Check compiles d as the layout engine compiles a stored definition and
	// returns the compile error, nil when d compiles. The error is the
	// engine's, unwrapped: it wraps the engine's ErrPattern and names the
	// layout and which of its patterns failed, and its text reaches clients,
	// so the caller adds its own wrapping and never rewrites it. Check reads
	// and writes nothing, and is safe for concurrent use.
	Check(d LayoutDefinition) error
}
