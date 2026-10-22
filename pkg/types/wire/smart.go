package wire

// SmartStatistics is the historical Smart Remote submission shape. Current 7.x
// parity remains an inference until the #216 capture.
type SmartStatistics struct {
	RepoPath               string `json:"repoPath"`
	Path                   string `json:"path"`
	DownloadCount          int64  `json:"downloadCount"`
	LastDownloaded         int64  `json:"lastDownloaded"`
	LastDownloadedBy       string `json:"lastDownloadedBy"`
	RemoteDownloadCount    int64  `json:"remoteDownloadCount"`
	RemoteLastDownloaded   int64  `json:"remoteLastDownloaded"`
	RemoteLastDownloadedBy string `json:"remoteLastDownloadedBy"`
}
