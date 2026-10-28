package config

// Cran controls the legacy CRAN archive migration operation.
type Cran struct {
	// ArchiveMoverEnabled enables move-archives. It defaults off, matching
	// Artifactory's artifactory.cran.archiveMover.enabled system property.
	ArchiveMoverEnabled bool `yaml:"archive_mover_enabled"`
}
