package model

// TrashcanSettings control the trash can.
type TrashcanSettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

// FolderDownloadSettings gate downloading a folder as one archive. As in
// Artifactory, it is off until enabled, and anonymous callers need their own
// switch.
type FolderDownloadSettings struct {
	Enabled             bool `json:"enabled"`
	EnabledForAnonymous bool `json:"enabled_for_anonymous"`
	MaxDownloadSizeMB   int  `json:"max_download_size_mb"`
	MaxFiles            int  `json:"max_files"`
}
