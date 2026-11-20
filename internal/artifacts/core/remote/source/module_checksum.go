package source

// MaxModuleChecksumDocumentBytes bounds a complete module checksum file.
const MaxModuleChecksumDocumentBytes = 16 << 20

// ModuleChecksumResponse retains original checksum-file bytes in a private
// caller-independent control. Selected filenames and verified archive digests
// belong to the adapter's validated archive descriptor, never public metadata.
type ModuleChecksumResponse struct {
	Content []byte `json:"content"`
}
