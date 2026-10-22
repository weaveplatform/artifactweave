package nuget

// SetListPage replaces how many packages one read of the index lists until
// the returned func restores it.
func SetListPage(n int) func() {
	old := listPage
	listPage = n
	return func() { listPage = old }
}

var (
	ValidateRemoteDocument = validateDocument
	ValidateRemoteURL      = resourceURL
	DecodeRemote           = decodeRemote
)
