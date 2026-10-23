package support

// MavenRoutes is the route inventory asserted by the independent Maven
// server-assembly package. The item routes are shared with other formats;
// the two REST routes are Maven-specific.
func MavenRoutes() map[string]bool {
	return map[string]bool{
		"PUT /artifactory/{repoKey}/{filePath}":                                true,
		"GET /artifactory/{repoKey}/{filePath}":                                true,
		"POST /artifactory/api/maven/calculateMetadata/{repoKey}/{folderPath}": true,
		"POST /artifactory/api/maven/generatePom/{repoKey}/{jarFilePath}":      true,
	}
}
