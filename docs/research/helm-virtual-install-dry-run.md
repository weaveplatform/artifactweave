# Helm virtual install dry-run qualification

## Missing acceptance evidence

[#1713 / HLM-03](https://github.com/weaveplatform/artifactweave/issues/1713)
retains the install requirement from [EPIC-HLM #1523](https://github.com/weaveplatform/artifactweave/issues/1523).
At main `3144f8a97449bd6849da2d042a964bd801504892`, the four-row
`helm_virtual.feature` outline covers repository add/update, signed remote and
local pulls, explicit member selection, archive rendering, dependency update,
deployment-target publication and immediate index invalidation. It executes no
`helm install` command. Rendering a previously pulled archive with `helm template`
does not establish repository-reference installation.

No Helm runtime failure was reproduced in that audit. This is a native-client
qualification gap, separate from runtime correctness changes.

## Client and command contract

The maintained client is Helm **4.3.1**, pinned in
`.github/actions/registry-clients/action.yml`. Its [official install documentation](https://helm.sh/docs/helm/helm_install/)
defines `--dry-run=client` as a client-side simulation that avoids cluster
connections. A repository chart reference resolves through its configured
repository alias. `--verify` additionally requires valid provenance.

The outline now runs these commands after repository add/update/search, before
any archive pull:

```sh
helm install preview-remote aw/acme --version 0.1.0 --dry-run=client \
  --content-cache .cache/helm/install-remote
helm install preview-local aw/web --version 0.1.0 --dry-run=client \
  --content-cache .cache/helm/install-local
```

Both must render a Deployment from the independently expected chart template
and name their expected release. The access log must show successful virtual
archive requests. The subsequent signed pull must also retrieve remote provenance. Placing these assertions before any
pull prevents an earlier archive request from satisfying the install evidence.
The local and remote paths are independently specified for all four combinations
of relative URLs and member namespaces. Each install uses its own documented
content-cache directory, so it cannot warm the default cache used by the
original pulls. No verification data is seeded.

## Retained controls

The original signed pull and exact provenance/archive comparisons remain, as do
the first local duplicate winner, explicit member selection, `helm template`,
dependency update, publication through the virtual deployment target, refreshed
index visibility and subsequent pull. Install dry-run uses `aw/acme` and
`aw/web`, rather than local archive paths. It needs no Kubernetes cluster and
adds no runtime configuration, client pin or credential-forwarding exception.

The client recipe includes the same client-side install command and fingerprints
both native install executions. Fingerprints detect evidence drift; successful
native execution must be recorded separately before closing HLM-03.

## Optional signed-install limitation

An initial extra `install --verify` check failed in all four configurations.
A separate actual-server diagnostic compared direct remote and virtual routes,
clearing the client cache for each command. Signed pulls returned archive and
provenance with HTTP `200/200`. Signed installs returned `200/401`, including
with the documented explicit `--username`/`--password` flags. Plain installs
returned the archive with HTTP `200` and rendered a Deployment on both routes.

The pinned [installer](https://github.com/helm/helm/blob/v4.3.1/pkg/action/install.go#L993)
uses `DownloadToCache`. Its [provenance read](https://github.com/helm/helm/blob/v4.3.1/pkg/downloader/chart_downloader.go#L293)
omits getter options; the [HTTP getter](https://github.com/helm/helm/blob/v4.3.1/pkg/getter/httpgetter.go#L41)
uses a fresh local option copy per request. The ordinary
[pull reader](https://github.com/helm/helm/blob/v4.3.1/pkg/downloader/chart_downloader.go#L188)
passes its authenticated options when fetching provenance. This is a pinned
client limitation rather than a reproduced registry defect.

HLM-03 requires client-side install dry-run, without the optional verification
flag. The final outline uses that exact workflow and retains every original
mandatory signed-pull/provenance assertion. No anonymous-read grant, client
patch/pin change, manually seeded verification cache or credential-forwarding
exception is introduced. The failed initial four-case run and diagnostic
counters remain separate from successful final qualification.

## Qualification

The branch normally integrated main
`901a11a5c02d9e6b7be3abbf83caa865a2510fcf`, including the Bazel runtime and
PyPI fixture corrections, before final qualification. The complete final
outline with separate install caches passed on the supported **darwin/arm64** host with
Helm `v4.3.1+gc0d08d0`, Go 1.27.4, `ARTIFACTWEAVE_ACCEPT_LOCAL=1`, race
detection and shuffled test order: **4 scenarios / 360 steps, zero skips**,
19.342 seconds for the command. It executes eight repository-reference installs
and every original control. The host Helm archive SHA-256 is
`6b4e61c8cd27d99e5a801e63ff25dd5c70b36875c2cd659d3e9ce6b76fc23198`;
the executed binary matches that archive and hashes to
`c62da98a8e5728f5acc08b66bbe5c3ad1cb6b71d3d31b7bf939d9f4b6628c96d`.

The separate Linux attempt never reached runner/client startup: Docker was
unresponsive and ultimately returned a daemon connection error. It supplies no
Linux native evidence. The failed optional-verification run, diagnostic controls
and final successful counters/logs are retained separately. Linux qualification
still requires an actual successful local or CI execution. Epic closure and
final merged-main qualification remain with the maintainer.
