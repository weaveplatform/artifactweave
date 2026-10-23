// Package support owns the route inventory used by standalone server tests.
package support

// NuGetRoutes lists the implemented NuGet route patterns. The root package
// uses this inventory for registration/stub checks; test/server/nuget runs
// their status and principal assertions independently.
func NuGetRoutes() map[string]bool {
	return map[string]bool{
		"DELETE /artifactory/api/nuget/{repoKey}/{id}/{version}":                        true,
		"GET /artifactory/api/nuget/v3/{repoKey}/autocomplete":                          true,
		"GET /artifactory/api/nuget/v3/{repoKey}/flatcontainer/{id}/index.json":         true,
		"GET /artifactory/api/nuget/v3/{repoKey}/flatcontainer/{id}/{version}/{file}":   true,
		"GET /artifactory/api/nuget/v3/{repoKey}/index.json":                            true,
		"GET /artifactory/api/nuget/v3/{repoKey}/query":                                 true,
		"GET /artifactory/api/nuget/v3/{repoKey}/{file}.pdb/{key}/{file}.pdb":           true,
		"GET /artifactory/api/nuget/v3/{repoKey}/{hive}/Download/{id}/{version}":        true,
		"GET /artifactory/api/nuget/v3/{repoKey}/{hive}/{id}/index.json":                true,
		"GET /artifactory/api/nuget/v3/{repoKey}/{hive}/{id}/page/{lower}/{upper}.json": true,
		"GET /artifactory/api/nuget/v3/{repoKey}/{hive}/{id}/{version}.json":            true,
		"GET /artifactory/api/nuget/{repoKey}/":                                         true,
		"GET /artifactory/api/nuget/{repoKey}/$metadata":                                true,
		"GET /artifactory/api/nuget/{repoKey}/Download/{id}/{version}":                  true,
		"GET /artifactory/api/nuget/{repoKey}/FindPackagesById()":                       true,
		"GET /artifactory/api/nuget/{repoKey}/Packages()":                               true,
		"GET /artifactory/api/nuget/{repoKey}/Packages(Id='{id}',Version='{version}')":  true,
		"GET /artifactory/api/nuget/{repoKey}/Search()":                                 true,
		"GET /artifactory/api/nuget/{repoKey}/{query}/$count":                           true,
		"HEAD /artifactory/api/nuget/v3/{repoKey}/flatcontainer/{id}/{version}/{file}":  true,
		"HEAD /artifactory/api/nuget/v3/{repoKey}/index.json":                           true,
		"HEAD /artifactory/api/nuget/v3/{repoKey}/{file}.pdb/{key}/{file}.pdb":          true,
		"HEAD /artifactory/api/nuget/{repoKey}/":                                        true,
		"HEAD /artifactory/api/nuget/{repoKey}/$metadata":                               true,
		"HEAD /artifactory/api/nuget/{repoKey}/Download/{id}/{version}":                 true,
		"HEAD /artifactory/api/nuget/{repoKey}/FindPackagesById()":                      true,
		"HEAD /artifactory/api/nuget/{repoKey}/Packages()":                              true,
		"HEAD /artifactory/api/nuget/{repoKey}/Packages(Id='{id}',Version='{version}')": true,
		"HEAD /artifactory/api/nuget/{repoKey}/Search()":                                true,
		"HEAD /artifactory/api/nuget/{repoKey}/{query}/$count":                          true,
		"POST /artifactory/api/nuget/{repoKey}/{id}/{version}":                          true,
		"PUT /artifactory/api/nuget/v3/{repoKey}/symbols":                               true,
		"PUT /artifactory/api/nuget/{repoKey}/{folder}":                                 true,
	}
}
