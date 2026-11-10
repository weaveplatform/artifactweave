package sql

// SealedPurposes exposes the purpose of every sealed column the modules
// register, which Reseal covers, to the external tests, in registration
// order and with any repeat kept.
func SealedPurposes() []string {
	var out []string
	for _, m := range Modules() {
		for _, c := range m.Sealed {
			out = append(out, c.Purpose)
		}
	}
	return out
}

// ModuleNames exposes the names of the registered modules, in order, to the
// external tests.
func ModuleNames() []string {
	var out []string
	for _, m := range Modules() {
		out = append(out, m.Name)
	}
	return out
}
