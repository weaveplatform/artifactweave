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
2. `X-Forwarded-Proto`, `X-Forwarded-Host`, `X-Forwarded-Port`
3. `base_url` from the application configuration
4. The request's own scheme and host

Set `base_url` or forward the `X-Forwarded-*` headers; otherwise clients behind the proxy
receive internal addresses. Behind a chain of proxies, such as a load balancer in front of an
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
the server refuses to start without it. A request whose host (the first
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
