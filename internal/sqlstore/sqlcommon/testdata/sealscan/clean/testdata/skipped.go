// Package skipped sits in a testdata directory, which the scan skips.
package skipped

type store interface {
	Seal(purpose, rowID, value string)
}

func put(s store) { s.Seal("skipped.testdata", "row", "value") }
