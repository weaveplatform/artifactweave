# Reverse proxy

Status: skeleton. Completed with P6 (Docker) and P12 (reverse proxy configuration API).

artifactweave serves every JFrog path on one listener: `/artifactory/...`, `/access/...`,
`/event/...`, `/router/api/v1/system/health`, `/.well-known/terraform.json` and
`/v2/...`. A reverse proxy in front of it only needs to forward everything and terminate
TLS.

## Base URL

Responses that embed absolute URLs (npm tarballs, Helm chart URLs, NuGet service index,
Terraform download URLs, Docker upload locations and the token realm, file info `uri`)
use this precedence, matching Artifactory:

1. `X-JFrog-Override-Base-Url` request header
2. `base_url` from the application configuration
3. `X-Forwarded-Proto`, `X-Forwarded-Host`, `X-Forwarded-Port`
4. The request's own scheme and host

**Set `base_url` in production.** It is the public URL clients reach artifactweave at, and once
it is set no request header except `X-JFrog-Override-Base-Url` changes the URLs artifactweave
hands out. Without it, artifactweave builds them from the forwarded headers, and failing those
from the request; a client whose proxy passes on its own `X-Forwarded-Host` then chooses those
URLs, the Docker token realm among them. Have the proxy set or overwrite the `X-Forwarded-*`
headers rather than pass on what the client sent, and strip `X-JFrog-Override-Base-Url` from
client requests unless the proxy sets it itself: artifactweave, like Artifactory, honours it on
every request. The legacy `X-Artifactory-Override-Base-Url` is ignored; use
`X-JFrog-Override-Base-Url`. Behind a chain of proxies, such as a load balancer in front of an
ingress, each header can arrive as a comma-separated list, one entry per proxy. artifactweave
uses each header's first entry, the client-facing one, so the outermost proxy must set the
headers and inner proxies must append to them or pass them through, never replace them.

## Docker

The repository-path method is native: `docker push proxy.example.com/<repo>/<image>:<tag>`
reaches `/v2/<repo>/<image>/...` and needs no rewrite rules. Artifactory's own path,
`/artifactory/api/docker/<repo>/v2/...`, is served too. Repository keys match without
regard to case, because Docker clients send references in lower case only; a key a client
cannot send (upper case aside, a leading `-` or `_`, or a character outside `[a-z0-9._-]`)
is reachable only through the REST API.

The subdomain method (`docker push docker-local.proxy.example.com/<image>:<tag>`) is
enabled with `docker.subdomain_match` and needs `base_url`, whose host is the parent:
the server refuses to start without it. The host the client addressed picks the
repository, while the URLs artifactweave answers with, such as an upload's `Location`, take
their host from `base_url`. A request whose host (the first
`X-Forwarded-Host` value, else `Host`) is `<repo>.<base_url host>`, where `<repo>` is a
Docker, OCI or Helm OCI repository, is for that repository, and `/v2/<image>/...` names
an image in it. Any other host, the parent included, uses the repository path. Forward
the original host, and provide wildcard DNS and a certificate for `*.<base_url host>`; a
repository used this way needs a key that is a valid DNS label.
`GET /artifactory/api/system/configuration/webServer` then reports
`dockerReverseProxyMethod` `SUBDOMAIN`, and `REPOPATHPREFIX` otherwise.
Port-per-repository is not supported.

`GET /artifactory/api/system/configuration/reverseProxy/nginx` returns a snippet
generated from the stored reverse proxy configuration once P12 lands.
