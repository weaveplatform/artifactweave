package wire

// MultipartUploadStatus answers the JFrog CLI's multipart upload status
// poll (POST /artifactory/api/v1/uploads/status), in the shape
// jfrog-client-go decodes (artifactory/services/utils/multipartupload.go).
type MultipartUploadStatus struct {
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	Progress      *int   `json:"progress,omitempty"`
	ChecksumToken string `json:"checksumToken,omitempty"`
}
