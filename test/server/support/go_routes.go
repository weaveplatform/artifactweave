package support

// GoRoutes is the implemented Go module protocol route inventory. The root
// package retains registration guards while test/server/golang runs status
// and principal assertions independently.
func GoRoutes() map[string]bool {
	const base = "/artifactory/api/go/{repoKey}/{module}/"
	return map[string]bool{
		"GET " + base + "@v/list":            true,
		"HEAD " + base + "@v/list":           true,
		"GET " + base + "@v/{version}.info":  true,
		"HEAD " + base + "@v/{version}.info": true,
		"GET " + base + "@v/{version}.mod":   true,
		"HEAD " + base + "@v/{version}.mod":  true,
		"GET " + base + "@v/{version}.zip":   true,
		"HEAD " + base + "@v/{version}.zip":  true,
		"GET " + base + "@latest":            true,
		"HEAD " + base + "@latest":           true,
		"GET " + base + "@v/latest.info":     true,
		"PUT " + base + "@v/{version}.{ext}": true,
	}
}
