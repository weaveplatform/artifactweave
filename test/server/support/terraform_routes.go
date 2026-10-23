package support

// TerraformRoutes is the native module/provider registry and network mirror
// route inventory. Backend state routes have a separate owner.
func TerraformRoutes() map[string]bool {
	paths := []string{
		"/.well-known/terraform.json",
		"/artifactory/api/terraform/v1/modules/{repo}__{namespace}/{name}/{system}/versions",
		"/artifactory/api/terraform/v1/modules/{repo}__{namespace}/{name}/{system}/{version}/download",
		"/artifactory/api/terraform/{repo}/v1/modules/{namespace}/{name}/{system}/versions",
		"/artifactory/api/terraform/{repo}/v1/modules/{namespace}/{name}/{system}/{version}/download",
		"/artifactory/api/terraform/{repo}/v1/modules/{file}?sig={token}",
		"/artifactory/api/terraform/v1/providers/{repo}-rt-ns-{namespace}/{type}/versions",
		"/artifactory/api/terraform/v1/providers/{repo}-rt-ns-{namespace}/{type}/{version}/download/{os}/{arch}",
		"/artifactory/api/terraform/{repo}/v1/providers/{namespace}/{type}/versions",
		"/artifactory/api/terraform/{repo}/v1/providers/{namespace}/{type}/{version}/download/{os}/{arch}",
		"/artifactory/api/terraform/{repo}/providers/{hostname}/{namespace}/{type}/index.json",
		"/artifactory/api/terraform/{repo}/providers/{hostname}/{namespace}/{type}/{version}.json",
		"/artifactory/api/terraform/{repo}/providers/{file}?sig={token}",
	}
	routes := make(map[string]bool, 2*len(paths))
	for _, path := range paths {
		routes["GET "+path] = true
		routes["HEAD "+path] = true
	}
	return routes
}
