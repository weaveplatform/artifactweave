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
receive internal addresses.

## Docker

The repository-path method is native: `docker push proxy.example.com/<repo>/<image>:<tag>`
reaches `/v2/<repo>/<image>/...` and needs no rewrite rules. The subdomain method
(`<repo>.proxy.example.com`) needs wildcard DNS and a certificate SAN and is enabled with
`docker.subdomain_match`. Port-per-repository is not supported.

`GET /artifactory/api/system/configuration/reverseProxy/nginx` returns a snippet
generated from the stored reverse proxy configuration once P12 lands.
