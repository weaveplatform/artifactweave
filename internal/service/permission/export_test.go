package permission

// SetListPage overrides the page size List reads with, for a test.
func SetListPage(n int) func() {
	old := listPage
	listPage = n
	return func() { listPage = old }
}
