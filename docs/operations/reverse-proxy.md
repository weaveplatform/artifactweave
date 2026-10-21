# Reverse proxy

Status: routing guidance for the implemented P0–P8 protocols. Reverse proxy configuration
and generated snippets remain P12 work.

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
`/artifactory/api/docker/<repo>/v2/...`, is served too. A Docker, OCI or Helm OCI key has
no upper case letters and no `_`, and keys match without regard to case. A key a client
cannot send (a leading `-` or `.`, a trailing separator, or a non-ASCII letter) is
reachable only through the REST API.

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
Port-per-repository is currently unsupported and belongs to P28.

`GET /artifactory/api/system/configuration/reverseProxy/nginx` returns a snippet
generated from the stored reverse proxy configuration once P12 lands.

## Terraform

Forward `/.well-known/terraform.json` at the public host's root as well as the
`/artifactory/api/terraform/...` routes. Native Terraform discovery uses HTTPS, so the
public `base_url` and certificate must describe the host clients use. The discovery
document and backend ping are public even when anonymous repository access is disabled.

Preserve signed download and state-upload URLs, including their query strings. The modern
backend upload is a `PUT` to a `state-versions/<id>/content?token=...` URL without an
Authorization header; its signed capability supplies authorization. An extra proxy login
challenge on that route prevents native state uploads. Registry downloads similarly use
short-lived `?sig=...` URLs. Avoid recording these capability query values in proxy logs.
Any proxy request-size limit must allow the state sizes in use: artifactweave imposes no
20 MiB backend-state ceiling, and native acceptance covers a 21 MiB state.

Pasted-token `terraform login` and saved credentials work through `tfe.v2` account details.
The browser approval/token-page flow (`login.v1`, #262) is not implemented and belongs to
P21, together with npm web approval and enterprise browser authentication. P21 must also
define trusted proxy identity headers for HTTP-SSO; forwarding headers today does not
authenticate a user. P39 owns the full web console. See the
[capability roadmap](../implementation/capability-roadmap.md) for these separate exits.
