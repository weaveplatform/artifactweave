package npm

// SetMaxFields replaces the bound on a publish body's fields until the
// returned func restores it.
func SetMaxFields(n int) func() {
	old := maxFields
	maxFields = n
	return func() { maxFields = old }
}
