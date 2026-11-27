package app

// headAnswer is how a hand-written GET route answers a HEAD through
// middleware.HeadAsGet.
type headAnswer int

const (
	// headRunsGet runs the HEAD as the GET and drops its body, as Jersey
	// runs a resource's @GET for a HEAD (JAX-RS 2.1, section 3.3.5). The
	// handler is written for the GET alone, so it sends the GET's length.
	headRunsGet headAnswer = iota + 1
	// headSeesHead hands the handler the HEAD: a file download, which then
	// reads no file and counts no delivery, as the VCS downloads do.
	headSeesHead
)

// handwrittenHeads are the hand-written GET routes, by chi pattern, that
// answer a HEAD as their GET does (#1613). 7.x serves everything under
// /artifactory/api/ with one servlet, Jersey's (7.161.26's web.xml maps
// /api/* to ArtifactoryRestServlet and nothing else; RepoFilter passes an
// API path on to it), so every one of these is a Jersey resource method,
// and a GET-only one answers a HEAD through its @GET. In 7.161.26 only
// SecurityResourceV2, ReleaseBundleResource, CheckupResource and the UI's
// UserApiKeyResource declare a @HEAD. Access is a Jersey application too
// (RTFACT-16596 shows org.jfrog.access.server.rest.resource methods invoked
// by Jersey). A route missing from here and from headUnserved fails
// TestUnitHeadDecidedForEveryGet.
var handwrittenHeads = map[string]headAnswer{
	// ArtifactResource, PingResource, ConfigResource, ReplicationResource
	// and VersionsResources declare a @GET alone.
	"/artifactory/api/storage/{repoKey}":                     headRunsGet,
	"/artifactory/api/storage/{repoKey}/{itemPath}":          headRunsGet,
	"/artifactory/api/storage/{repoKey}/*":                   headRunsGet,
	"/artifactory/api/storage/auto-trashcan":                 headRunsGet,
	"/artifactory/api/storage/auto-trashcan/*":               headRunsGet,
	"/artifactory/api/system/ping":                           headRunsGet,
	"/artifactory/api/system/configuration":                  headRunsGet,
	"/artifactory/api/replication/{repoPath}":                headRunsGet,
	"/artifactory/api/replication/*":                         headRunsGet,
	"/artifactory/api/versions/{repo}/{path}":                headRunsGet,
	"/artifactory/api/versions/{repo}/*":                     headRunsGet,
	"/artifactory/api/artifactweave/remote-status/{repoKey}": headRunsGet,
	// ServiceTrustResource declares a @GET alone; a use case may hold a
	// slash, so the wildcard serves it too.
	"/artifactory/api/v1/service_trust/pairings/{usecase}": headRunsGet,
	"/artifactory/api/v1/service_trust/pairings/*":         headRunsGet,
	// ArchiveResource.downloadFolder and DownloadResource declare a @GET
	// alone; the folder archive is streamed, so its HEAD builds none.
	"/artifactory/api/archive/download/{repoKey}/{path}": headSeesHead,
	"/artifactory/api/archive/download/{repoKey}/*":      headSeesHead,
	"/artifactory/api/download/{repoKey}/{filePath}":     headSeesHead,
	"/artifactory/api/download/{repoKey}/*":              headSeesHead,
	// Access's own probes.
	"/access/api/v1/system/ping":      headRunsGet,
	"/access/api/v1/system/readiness": headRunsGet,
	"/access/api/v1/system/liveness":  headRunsGet,
	// The package APIs of the add-ons, which are not in the OSS tree [I]:
	// Jersey resources all the same, as 7.161.26's ShadowResponseFilter, a
	// Jersey response filter, shadows /api/<package type>/ requests by
	// their resource method, the Terraform backend's among them.
	"/artifactory/api/huggingfaceml/{repoKey}/api/models/{owner}/{name}":                                                headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/models/{owner}/{name}/revision/{revision}":                            headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/models/{owner}/{name}/tree/{revision}":                                headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/models/{owner}/{name}/tree/{revision}/*":                              headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/models/{owner}/{name}/xet-read-token/{revision}":                      headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/models/{owner}/{name}/xet-write-token/{revision}":                     headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/datasets/{owner}/{name}":                                              headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/datasets/{owner}/{name}/revision/{revision}":                          headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/datasets/{owner}/{name}/tree/{revision}":                              headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/datasets/{owner}/{name}/tree/{revision}/*":                            headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/datasets/{owner}/{name}/xet-read-token/{revision}":                    headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/api/datasets/{owner}/{name}/xet-write-token/{revision}":                   headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/xet/{kind}/{owner}/{name}/{revision}/v1/reconstructions/{hash}":           headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/xet/{kind}/{owner}/{name}/{revision}/v2/reconstructions/{hash}":           headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/xet/{kind}/{owner}/{name}/{revision}/v1/chunks/{prefix}/{hash}":           headRunsGet,
	"/artifactory/api/huggingfaceml/{repoKey}/xet/{kind}/{owner}/{name}/{revision}/data/{hash}/{segment}/{start}/{end}": headRunsGet,
	"/artifactory/api/skills/{repoKey}/.well-known/clawhub.json":                                                        headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/skills":                                                                   headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/skills/{slug}":                                                            headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/skills/{slug}/versions":                                                   headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/skills/{slug}/versions/{version}":                                         headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/skills/{slug}/file":                                                       headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/search":                                                                   headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/resolve":                                                                  headRunsGet,
	"/artifactory/api/skills/{repoKey}/api/v1/download":                                                                 headSeesHead,
	"/artifactory/api/skills/{repoKey}/xrayStatus":                                                                      headRunsGet,
	"/artifactory/api/terraform/remote/v2/ping":                                                                         headRunsGet,
	"/artifactory/api/terraform/remote/v2/account/details":                                                              headRunsGet,
	"/artifactory/api/terraform/remote/v2/organizations/{repo}":                                                         headRunsGet,
	"/artifactory/api/terraform/remote/v2/organizations/{repo}/entitlement-set":                                         headRunsGet,
	"/artifactory/api/terraform/remote/v2/organizations/{repo}/workspaces":                                              headRunsGet,
	"/artifactory/api/terraform/remote/v2/organizations/{repo}/workspaces/{name}":                                       headRunsGet,
	"/artifactory/api/terraform/remote/v2/workspaces/{id}":                                                              headRunsGet,
	"/artifactory/api/terraform/remote/v2/workspaces/{id}/current-state-version":                                        headRunsGet,
	"/artifactory/api/terraform/remote/v2/state-versions":                                                               headRunsGet,
	"/artifactory/api/terraform/remote/v2/state-versions/{id}":                                                          headRunsGet,
	"/artifactory/api/terraform/remote/v2/state-versions/{id}/download":                                                 headRunsGet,
	"/artifactory/api/pub/{repoKey}/api/packages/{package}/versions/{version}":                                          headRunsGet,
	"/artifactory/api/pub/{repoKey}/api/packages/versions/new":                                                          headRunsGet,
	"/artifactory/api/pub/{repoKey}/api/packages/versions/new/complete":                                                 headRunsGet,
	"/artifactory/api/cargo/{repoKey}/api/v1/crates/{name}":                                                             headRunsGet,
	"/artifactory/api/nuget/v3/{repoKey}/query":                                                                         headRunsGet,
	"/artifactory/api/nuget/v3/{repoKey}/autocomplete":                                                                  headRunsGet,
	"/artifactory/api/machinelearning/{repoKey}/model/entity-manifest/{name}/{version}":                                 headRunsGet,
	"/artifactory/api/machinelearning/{repoKey}/model/entity-manifest/{namespace}/{name}/{version}":                     headRunsGet,
	"/artifactory/api/lfs/{repoKey}/locks":                                                                              headRunsGet,
	"/artifactory/api/docker/{repoKey}/v1/auth":                                                                         headRunsGet,
	// The protocol's own HEAD route would take these by backtracking into
	// its wildcard; JAX-RS picks the path with the most literal characters
	// before the method (section 3.7.2), so the GET answers, as npm's
	// authentication GETs do since #1598.
	"/artifactory/api/npm/{repoKey}/-/ping":                          headRunsGet,
	"/artifactory/api/npm/{repoKey}/-/whoami":                        headRunsGet,
	"/artifactory/api/npm/{repoKey}/-/v1/search":                     headRunsGet,
	"/artifactory/api/npm/{repoKey}/-/package/*":                     headRunsGet,
	"/artifactory/api/luarocks/{repoKey}/api/tool_version":           headRunsGet,
	"/artifactory/api/luarocks/{repoKey}/api/1/{key}/check_rockspec": headRunsGet,
}

// headUnserved are the GET routes with no HEAD route of their own pattern
// that HeadAsGet leaves to what chi finds, each with what answers the HEAD.
var headUnserved = map[string]string{
	"/ui/native-login":                         "405 when enabled: no HEAD browser session mutation",
	"/ui/terraform/oauth2/authorize":           "405 when enabled: no HEAD approval admission",
	"/artifactory/api/npm/{repoKey}/-/v1/done": "405 when enabled: never redeem a token through HEAD",
	// The JFrog Router is a Go service built on Traefik (the router's
	// traefik settings in RTFACT-25374, Traefik's access log in
	// RTFACT-31079), not a Jersey application, and no source shows it
	// answering a HEAD of its probes [I].
	"/router/api/v1/system/health":    "405: the JFrog Router, no Jersey resource",
	"/router/api/v1/system/readiness": "405: the JFrog Router, no Jersey resource",
	"/router/api/v1/system/liveness":  "405: the JFrog Router, no Jersey resource",
	// The PyPI protocol's HEAD route answers the JSON API itself, with the
	// GET's status, header and length.
	"/artifactory/api/pypi/{repoKey}/pypi/{package}/json":           "the PyPI protocol's HEAD",
	"/artifactory/api/pypi/{repoKey}/pypi/{package}/{version}/json": "the PyPI protocol's HEAD",
}
