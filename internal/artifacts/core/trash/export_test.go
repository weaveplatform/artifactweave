package trash

// SetPage sets the entries a Walk reads per store page, so the tests cross
// page boundaries without thousands of entries.
func SetPage(s *Service, n int) { s.page = n }
