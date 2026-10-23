package support

// HelmRoutes is the classic Helm protocol route inventory. Helm OCI routes
// have their own registry package and test owner.
func HelmRoutes() map[string]bool {
	return map[string]bool{
		"GET /artifactory/api/helm/{repoKey}/index.yaml":  true,
		"HEAD /artifactory/api/helm/{repoKey}/index.yaml": true,
		"GET /artifactory/api/helm/{repoKey}/{path}":      true,
		"HEAD /artifactory/api/helm/{repoKey}/{path}":     true,
	}
}
