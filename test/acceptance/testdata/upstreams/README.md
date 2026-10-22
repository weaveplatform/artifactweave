# Public upstream fixture pins

`pins.json` records read-only public inputs observed on 2026-10-22. The e2e suite
requires the recorded artifact/manifest SHA-256; an upstream changing a tag or
version cannot silently replace the expected bytes. Mutable discovery documents
are checked for the pinned identity and hashed in the outcome report. The fixture
matrix and native-versus-protocol evidence are in [the CI guide](../../../../docs/testing/ci.md#public-upstream-conformance-p9).

| Input | Primary public source |
|---|---|
| Generic | [Terraform v1.16.4 README](https://raw.githubusercontent.com/hashicorp/terraform/v1.16.4/README.md) |
| Maven family | [SLF4J API 2.0.17 at Maven Central](https://repo.maven.apache.org/maven2/org/slf4j/slf4j-api/2.0.17/) |
| npm | [is-number 7.0.0 metadata](https://registry.npmjs.org/is-number/7.0.0) |
| Python | [six simple index](https://pypi.org/simple/six/) |
| NuGet | [Newtonsoft.Json 13.0.3 flat package](https://api.nuget.org/v3-flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg), [v2 feed](https://www.nuget.org/api/v2/FindPackagesById()?id=%27Newtonsoft.Json%27) |
| Classic Helm | [podinfo index](https://stefanprodan.github.io/podinfo/index.yaml) |
| Docker/OCI | [busybox official image source](https://github.com/docker-library/busybox), registry `registry-1.docker.io/library/busybox` |
| Helm OCI | [podinfo publisher](https://github.com/stefanprodan/podinfo), registry `ghcr.io/stefanprodan/charts/podinfo` |
| Terraform module | [label 0.25.0 registry metadata](https://registry.terraform.io/v1/modules/cloudposse/label/null/0.25.0), immutable [source commit](https://github.com/cloudposse/terraform-null-label/tree/488ab91e34a24a86957e397d9f7262ec5925586a) |
| Terraform provider | [null 3.3.2 release](https://releases.hashicorp.com/terraform-provider-null/3.3.2/), [registry descriptor](https://registry.terraform.io/v1/providers/hashicorp/null/3.3.2/download/linux/amd64) |

The Terraform provider registry's version list includes older null releases
advertising the major-only protocol `"4"` beside the pinned 3.3.2 release's
`"5.0"`. The adapter validates both numeric forms so an older list entry does
not block the pinned native installation. The ZIP SHA-256, checksum bundle,
signature, and signer remain independently verified.

The NuGet v2 fixture configures the `https://www.nuget.org` origin and uses the
repository's default `api/v2` feed and `api/v2/package` download contexts. On
2026-10-22 the exact `FindPackagesById()` metadata URL returned GET 200 but HEAD
404, so this fixture sets the existing `bypassHeadRequests` option. The v3 fixture
retains normal preflight behavior. This provider observation does not change the
shared transport policy or turn a failed request into a successful probe.

To update a pin, choose a small immutable version without an unbounded dependency
graph, fetch its exact artifact through the primary endpoint, verify the publisher's
integrity/signature where available, and calculate SHA-256 over the original bytes.
For OCI, resolve a concrete linux/amd64 manifest and retain its descriptor graph;
never substitute a mutable tag for the digest. For Terraform, retain the original
advertised signer and checksum/signature bundle; a hash update alone is not signer
qualification. Change the fixed version selectors with the JSON pins, run the
complete affected cold/warm case, and include its outcome artifact in review.
The current individual artifacts are below 6 MiB and the selected graphs are small.

These are observations of public registries. They do not establish Artifactory
vendor behavior. Additional layout/client versions remain the P37 interoperability
matrix; private auth, mTLS, symbols and signed Helm provenance are controlled
acceptance cases. No secret, access token or captured authenticated response belongs
in this directory.
