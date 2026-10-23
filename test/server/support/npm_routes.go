package support

// NPMRoutes names the implemented npm protocol and migration operations.
// The root registration guards and standalone npm status/matrix suites share
// this inventory without importing each other's test package.
func NPMRoutes() map[string]bool {
	const base = "/artifactory/api/npm/"
	const migrations = "/artifactory/api/v1/migrations/npm/repositories/"
	return map[string]bool{
		"POST " + base + "{repoKey}/reindex":                               true,
		"GET " + base + "{repoKey}/{package}":                              true,
		"GET " + base + "{repoKey}/{package}/{version}":                    true,
		"GET " + base + "{repoKey}/{package}/-/{tarball}":                  true,
		"HEAD " + base + "{repoKey}/{package}/-/{tarball}":                 true,
		"GET " + base + "{repoKey}/-/ping":                                 true,
		"GET " + base + "{repoKey}/-/whoami":                               true,
		"PUT " + base + "{repoKey}/{package}":                              true,
		"GET " + base + "{repoKey}/-/v1/search":                            true,
		"PUT " + base + "{repoKey}/-/user/org.couchdb.user:{user}":         true,
		"DELETE " + base + "{repoKey}/-/user/token/{token}":                true,
		"GET " + base + "auth":                                             true,
		"GET " + base + "{repoKey}/auth/{scope}":                           true,
		"GET " + base + "{repoKey}/-/package/{package}/dist-tags":          true,
		"PUT " + base + "{repoKey}/-/package/{package}/dist-tags/{tag}":    true,
		"DELETE " + base + "{repoKey}/-/package/{package}/dist-tags/{tag}": true,
		"PUT " + base + "{repoKey}/{package}/-rev/{rev}":                   true,
		"DELETE " + base + "{repoKey}/{package}/-/{tarball}/-rev/{rev}":    true,
		"DELETE " + base + "{repoKey}/{package}/-rev/{rev}":                true,
		"POST " + migrations + "{repoKey}/migrate":                         true,
		"POST " + migrations + "{repoKey}/resume":                          true,
	}
}

// NPMGeneratedRoutes is the subset implemented by generated Artifactory
// operations. The root stub guard must skip these after their status rows move.
func NPMGeneratedRoutes() map[string]bool {
	const base = "/artifactory/api/npm/"
	const migrations = "/artifactory/api/v1/migrations/npm/repositories/"
	return map[string]bool{
		"POST " + base + "{repoKey}/reindex":       true,
		"GET " + base + "auth":                     true,
		"GET " + base + "{repoKey}/auth/{scope}":   true,
		"POST " + migrations + "{repoKey}/migrate": true,
		"POST " + migrations + "{repoKey}/resume":  true,
	}
}
