package terraform

// SetMaxInflated bounds what H1 inflates for a test and returns the undo.
func SetMaxInflated(n int64) func() {
	old := maxInflated
	maxInflated = n
	return func() { maxInflated = old }
}
