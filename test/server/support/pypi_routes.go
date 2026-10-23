package support

// PyPIRoutes is the implemented PyPI protocol inventory. The root server
// package guards registration; test/server/pypi checks status and principals.
func PyPIRoutes() map[string]bool {
	const base = "/artifactory/api/pypi/"
	return map[string]bool{
		"POST " + base + "{repoKey}":                                 true,
		"POST " + base + "{repoKey}/":                                true,
		"POST " + base + "{repoKey}/{folder}":                        true,
		"PUT " + base + "{repoKey}/{filePath}":                       true,
		"GET " + base + "{repository}/pypi/{package}/json":           true,
		"GET " + base + "{repository}/pypi/{package}/{version}/json": true,
		"HEAD " + base + "{repoKey}/pypi/{package}/json":             true,
		"HEAD " + base + "{repoKey}/pypi/{package}/{version}/json":   true,
		"GET " + base + "{repoKey}/simple":                           true,
		"HEAD " + base + "{repoKey}/simple":                          true,
		"GET " + base + "{repoKey}/simple/":                          true,
		"HEAD " + base + "{repoKey}/simple/":                         true,
		"GET " + base + "{repoKey}/simple/{project}/":                true,
		"HEAD " + base + "{repoKey}/simple/{project}/":               true,
		"GET " + base + "{repoKey}/{filePath}":                       true,
		"HEAD " + base + "{repoKey}/{filePath}":                      true,
		"GET " + base + "{repoKey}/{wheelPath}.metadata":             true,
		"HEAD " + base + "{repoKey}/{wheelPath}.metadata":            true,
	}
}
