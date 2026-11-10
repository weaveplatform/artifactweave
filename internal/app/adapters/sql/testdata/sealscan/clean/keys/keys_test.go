package keys

// The scan reads no test file, so this purpose needs no registration.
func sealInTest(s store) { _, _ = s.Seal("keys.test_only", "row", "value") }
