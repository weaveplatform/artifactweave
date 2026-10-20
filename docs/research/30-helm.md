# Helm and Helm OCI

Research baseline: **2026-10-19**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-19, by [#529](https://github.com/weaveplatform/artifactweave/issues/529); [section 13](#13-artifactory-7x-check) records the check.

Phase P8 serves Helm charts from local `helm` repositories, the classic chart repositories
that Helm reads through an `index.yaml`, and P6 already serves Helm OCI (`helmoci`)
repositories through the Docker registry ([research 23](23-docker-oci-registry.md), decision
[0031](decisions/0031-docker-oci-registry.md)). [Research 04](04-package-protocols.md#helm)
sketched both from the documentation and asked for this check claim by claim. This report
collects what Helm, Artifactory 7.x, the JFrog CLI and the Terraform provider show, and
corrects what research 03, 04 and 23 got wrong (section 9). Decision
[0039](decisions/0039-helm.md) records the choices.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-19 |
| **[RN]** | stated in the Artifactory release notes or known issues at the cited URL, read on 2026-10-19 |
| **[KB]** | stated in a JFrog knowledge-base article at the cited URL, read on 2026-10-19 |
| **[J]** | recorded in JFrog's public Jira, read on 2026-10-19; it counts as 7.x behaviour under the rules in the [research README](README.md#target-version-and-sources) |
| **[S]** | seen in source code at the cited commit, or in a client's issue tracker where the cited comment says so |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **JFrog's documentation**, read with `.md` appended on 2026-10-19:
  [kubernetes-helm-chart-repositories](https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories.md)
  (updated 2026-07-05), [oci-repositories](https://docs.jfrog.com/artifactory/docs/oci-repositories.md),
  [additional-docker-information](https://docs.jfrog.com/artifactory/docs/additional-docker-information.md),
  [docker-repositories](https://docs.jfrog.com/artifactory/docs/docker-repositories.md),
  [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md),
  [jf-helm](https://docs.jfrog.com/artifactory/docs/jf-helm.md) (updated 2026-09-25),
  [package-manager-compatibility-matrix](https://docs.jfrog.com/artifactory/docs/package-manager-compatibility-matrix.md)
  (updated 2026-10-19), [createrepository](https://docs.jfrog.com/artifactory/reference/createrepository.md),
  the API references [calculatehelmchartindex](https://docs.jfrog.com/artifactory/reference/calculatehelmchartindex.md)
  and [helmchartspartialreindexing](https://docs.jfrog.com/artifactory/reference/helmchartspartialreindexing.md),
  the [configuration XSD](https://docs.jfrog.com/installation/docs/artifactory-configuration-xsd.md)
  (updated 2026-10-19) and [deprecations in process](https://docs.jfrog.com/releases/docs/deprecations-in-process.md).
- **JFrog's REST reference as the Internet Archive kept it**:
  [Calculate Helm Chart Index](http://web.archive.org/web/20260207042251/https://jfrog.com/help/r/jfrog-rest-apis/calculate-helm-chart-index)
  (2026-02-07) and [Helm Charts Partial Re-Indexing](http://web.archive.org/web/20250711095436/https://jfrog.com/help/r/jfrog-rest-apis/helm-charts-partial-re-indexing)
  (2025-07-11).
- **The release notes and known issues**:
  [self-managed](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases) 7.84.3
  to 7.161.32, [SaaS](https://docs.jfrog.com/releases/docs/artifactory-saas-releases) 7.80.0 to
  7.174.0, and the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues).
- **JFrog's knowledge base**:
  [000004492](https://jfrog.com/help/r/how-to-resolve-helm-index-yaml-virtual-repository-url-mismatch/how-to-resolve-helm-index.yaml-virtual-repository-url-mismatch)
  (the URLs in a virtual repository's `index.yaml`) and
  [000005859](https://jfrog.com/help/r/artifactory-how-to-manage-helm-charts-in-oci-based-registries-using-artifactory/artifactory-how-to-manage-helm-charts-in-oci-based-registries-using-artifactory)
  (Helm charts in OCI repositories).
- **JFrog's public Jira**, read through `https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`:
  the RTFACT keys cited below.
- **Helm** v3.22.1 (`d6c1a0e4027c`) and v4.3.1 (`c0d08d0d6d94`), the two current lines:
  `pkg/repo/index.go` (v4: `pkg/repo/v1/index.go`), `pkg/repo/chartrepo.go`,
  `pkg/chart/metadata.go` and `pkg/chart/dependency.go` (v4: `pkg/chart/v2/`),
  `pkg/registry/constants.go`, `client.go` and `reference.go`, `pkg/pusher/ocipusher.go`,
  `pkg/getter/httpgetter.go` and `pkg/downloader/chart_downloader.go`; Helm's documentation
  `helm/helm-www` at `9cc276f46afb`: `docs/topics/chart_repository.md`, `registries.mdx` and
  `provenance.mdx`; and [helm/helm#30911](https://github.com/helm/helm/issues/30911).
- **The helm-push plugin** (`cm-push`), `chartmuseum/helm-push` at `a8fa26fa3885`:
  `pkg/chartmuseum/upload.go`.
- **The JFrog CLI** `jf` 2.127.0 (`fd6c2ebec142`), which pins jfrog-cli-artifactory
  `4a0f3827cfa0` (`artifactory/commands/helm/`: `push.go`, `layers.go`, `utils.go`,
  `repository.go`, `helmcommand.go`, `buildinfo.go`) and jfrog-client-go `6053434e9301`
  (`artifactory/services/localrepository.go`); and
  [jfrog/jfrog-cli#686](https://github.com/jfrog/jfrog-cli/issues/686).
- **The JFrog Terraform provider** `jfrog/terraform-provider-artifactory` v12.11.14
  (`990e8dfdb64e`): `pkg/artifactory/resource/repository/local/resource_artifactory_local_helm_repository.go`,
  `resource_artifactory_local_helmoci_repository.go` and `docs/resources/local_helm_repository.md`,
  `local_helmoci_repository.md`.
- **Artifactory OSS 6.20** (`knottnhupfer/artifactory-oss` at `b99e5649bcad`), for what the
  others leave out: `base/common/src/main/java/org/artifactory/addon/helm/HelmAddon.java`,
  `web/rest-ui/src/main/java/org/artifactory/ui/rest/model/artifacts/search/packagesearch/criteria/PackageSearchCriteria.java`
  and `web/rest-ui/src/main/java/org/artifactory/ui/rest/model/artifacts/browse/treebrowser/action/recalculateindex/HelmIndexCalculator.java`.

Artifactory's Helm handler is closed source; 6.20 holds only the addon interface, the UI and
the package search. A claim no 7.x source shows is **[I]** and on the live-comparison list
([#216](https://github.com/weaveplatform/artifactweave/issues/216)), collected in section 11.

## 1. Clients

### 1.1 Classic chart repositories

| Fact | Basis |
|---|---|
| A repository is added with `helm repo add <name> <base>/artifactory/api/helm/<repo> --username <u> --password <p>`, then `helm repo update`; `helm install <release> <name>/<chart>` installs | [D] kubernetes-helm-chart-repositories, "Connect your Helm Client to Artifactory" |
| `helm repo add` and `helm repo update` request `GET <repo URL>/index.yaml`; a relative chart URL is resolved against the repository URL with a trailing slash added, and an absolute one is used as it is | [S] Helm `pkg/repo/chartrepo.go` (`DownloadIndexFile`, `ResolveReferenceURL`) |
| Any answer but 200 fails the fetch with `failed to fetch <url> : <status>`; the body is not read | [S] Helm `pkg/getter/httpgetter.go:94-95` |
| Helm sends the repository's Basic credentials to a chart URL only when its scheme and host equal the repository URL's, unless `--pass-credentials` is given | [S] Helm `pkg/getter/httpgetter.go:64-82` |
| Helm loads `index.yaml` with a strict YAML reader: a key outside the index, entry or `Chart.yaml` model makes the whole file unreadable | [S] Helm `pkg/repo/index.go` (`jsonOrYamlUnmarshal`, `yaml.UnmarshalStrict`) |
| Helm skips, with a warning, an entry that fails `Chart.yaml` validation, such as a version that is not SemVer; Helm's own source names JFrog: it ignores a duplicate dependency name or alias because a "JFrog repository strips alias field" | [S] Helm `pkg/repo/index.go` (`loadIndex`, `ignoreSkippableChartValidationError`, which cites helm/helm#12748) |
| `helm pull --verify` and `helm install --verify` fetch `<chart URL>.prov` beside the chart | [S] Helm `pkg/downloader/chart_downloader.go:119-128`; [D] Helm `provenance.mdx`: the file "MUST be accessible at" the chart's URL plus `.prov` |
| The page still lists "Helm version 2.9.0 or above" for classic repositories, while its top note says support for clients below 3.0.0 "will be discontinued" (section 8) | [D] kubernetes-helm-chart-repositories, "Supported Helm Clients" and the note at the top |
| To use a Helm repository, use `helm repo add`; Helm OCI commands against it "may fail" | [D] kubernetes-helm-chart-repositories |

### 1.2 Helm OCI

| Fact | Basis |
|---|---|
| Helm 3.8.0 and later speak OCI by default; earlier 3.x needs `HELM_EXPERIMENTAL_OCI=1` | [D] kubernetes-helm-chart-repositories, "Supported Helm OCI Clients" |
| Log in with `helm registry login <JFrog host>`, push with `helm push <chart>.tgz oci://<host>/<repo>`, pull with `helm pull oci://<host>/<repo>/<chart> [--version <v>]`; without `--version` the latest is pulled | [D] kubernetes-helm-chart-repositories, "Helm OCI Repositories"; [D] oci-repositories, "Helm OCI"; [KB] 000005859 |
| Helm OCI repositories "do not support non-OCI interactions"; the Docker access method decides how they are reached, "even if you are working exclusively with Helm OCI charts" | [D] kubernetes-helm-chart-repositories, the notes under "Connect your Helm OCI Client" |
| On SaaS with the subdomain method, `helm registry login` takes the Docker registry host, `<repo>.<host>`; the host alone answered 400 | [J] [RTFACT-30262](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30262) (JFrog support, Done) |
| `helm push` needs an `oci://` reference without the chart name or tag; the name is the chart's and the tag its version. A `.prov` beside the `.tgz` is pushed as a third layer | [D] Helm `registries.mdx`, "The push subcommand"; [S] `pkg/pusher/ocipusher.go:80-91` |
| A tag cannot hold `+`, so Helm replaces `+` with `_` in the tag it pushes, and reads `_` back as `+` when it lists tags | [S] Helm `pkg/registry/reference.go:57-59`, `client.go:895` (`Tags`) |
| `helm pull` without `--version` lists the tags and takes the highest SemVer | [S] Helm `pkg/registry/client.go` (`Tags`); v4 `pkg/registry/tag.go` |
| Helm 3.18.0 and later, Helm 4 included, push the manifest twice: `PUT .../manifests/sha256:<hex>` and then `PUT .../manifests/<version>`, both 201 on Artifactory. Helm keeps it open as an optimisation (an open pull request, helm/helm#31394, would push once) | [S] [helm/helm#30911](https://github.com/helm/helm/issues/30911) (request log against Artifactory; open); `client.go` `Push` (`oras.ExtendedCopy`) in v3.22.1 and v4.3.1 |

### 1.3 The JFrog CLI

| Fact | Basis |
|---|---|
| `jf helm <helm arguments>` runs the native `helm` with the given arguments and, with `--build-name` and `--build-number`, collects build-info for `package`, `push` and `dependency update`; `jf helm registry login` logs Helm in with the configured server's credentials | [D] jf-helm; [S] `helmcommand.go`, `utils.go:13-20` (`needBuildInfo`) |
| `jf helm push` is the OCI push: it requires `oci://<host>/<repo>`; there is no `jf helm` command for a classic repository's deploy, which is a REST `PUT` | [D] jf-helm, "Push a Chart to OCI Registry" and "Important Notes"; [D] kubernetes-helm-chart-repositories, "Deploy Helm Charts via API" |
| After the push, `jf helm push` reads the chart's name and version from the `.tgz`, derives candidate repository keys from the reference (the first path segment, or the host's first label for the subdomain method), and for each runs the AQL `items.find({"repo":"<repo>","path":"<sub>/<chart>/<version>"})`, keeping `manifest.json` and the `sha256__*` files; the first candidate with results wins, and none fails the build-info | [S] `push.go:85-149`, `repository.go:110-157` |
| It then sets `build.name`, `build.number`, `build.timestamp` (milliseconds) and, with `--project`, `build.project`, plus the CI VCS properties, recursively on the folder `<sub>/<chart>/<version>`, through the properties API. For a virtual repository it sets them in its `defaultDeploymentRepo`, or skips them when it has none | [S] `push.go:40-47`, `178-227` |
| It downloads that folder's `manifest.json`, takes the config digest and the layers from it, and records them as the artifacts of a module of type `helm` with the id `<chart>:<version>` | [S] `push.go:49-78`, `layers.go:235-251` |
| The path it searches holds the version as `Chart.yaml` writes it, so a version with `+` (tag `..._...`, section 1.2) is not found and the build-info step fails | [S] `push.go:96`, `buildOCIChartPath`; Helm `reference.go:57-59` |
| For a classic repository's dependency, `jf helm package` and `dependency update` search `<repo>/<name>-<version>*.tgz` without recursion | [S] `layers.go:180-203` |
| The documentation names the module type `helm` for charts in Helm OCI repositories too, and has REST clients set the build properties on "the chart version folder" | [D] jf-helm, "Build-info module type and REST API publishing" |
| JFrog's compatibility matrix lists Helm 4.3.1 as the most recent version passing the JFrog CLI's tests, while the `jf helm` page says "Helm 4.x has not been formally validated" | [D] package-manager-compatibility-matrix; [D] jf-helm, "Prerequisites" |

### 1.4 The helm-push plugin

| Fact | Basis |
|---|---|
| `helm cm-push` uploads with a `POST` to `<context path>/api/<rest of the repository URL's path>/charts`, so `<url>/api/charts` when `--context-path` is the whole path, a multipart form with the field `chart`; `--force` adds `?force` | [S] `chartmuseum/helm-push` `pkg/chartmuseum/upload.go:16-49` |
| JFrog documents only a REST `PUT` for deploying charts to a classic repository; no source shows Artifactory answering `POST .../api/charts`, nor what it answers | [D] kubernetes-helm-chart-repositories, "Deploy Helm Charts"; the answer [I] |

## 2. index.yaml

### 2.1 What Helm defines

| Fact | Basis |
|---|---|
| A chart repository is an HTTP server with an `index.yaml` and, usually, the packaged charts and their provenance files | [D] Helm `chart_repository.md`, "The chart repository structure" |
| The index is `{serverInfo, apiVersion, generated, entries, publicKeys, annotations}`, `apiVersion` `v1`; `entries` maps each chart name to a list of versions | [S] Helm `pkg/repo/index.go:81-93` |
| Each entry carries the `Chart.yaml` fields (`name`, `home`, `sources`, `version`, `description`, `keywords`, `maintainers`, `icon`, `apiVersion`, `condition`, `tags`, `appVersion`, `deprecated`, `annotations`, `kubeVersion`, `dependencies`, `type`) and `urls`, `created`, `removed` and `digest`, plus four deprecated keys a strict reader still accepts | [S] Helm `pkg/repo/index.go:267-291`; `pkg/chart/metadata.go:46-82` |
| A dependency carries `name`, `version`, `repository`, `condition`, `tags`, `enabled`, `import-values` and `alias` | [S] Helm `pkg/chart/dependency.go` |
| In canonical form each chart's versions are newest first, by SemVer; a version that does not parse goes last. Helm sorts on load anyway | [S] Helm `pkg/repo/index.go:57-79,167-177,378` |
| `digest` is the SHA-256 of the archive; `created` is when the entry was added | [S] Helm `pkg/repo/index.go:141-146,330-334` |
| A chart's `version` must be SemVer 2 and its `type` `application`, `library` or empty | [S] Helm `pkg/chart/metadata.go` (`Validate`) |
| Helm 4's index format is the same; `created` is always written | [S] diff of `pkg/repo/index.go` (v3.22.1) and `pkg/repo/v1/index.go` (v4.3.1) |

### 2.2 What Artifactory writes

| Fact | Basis |
|---|---|
| A local repository has one `index.yaml`, at its root, and it indexes charts in any folder: entries name paths such as `kcos-shell/kcos-shell-0.10.6+20210519.140020.54be389b.tgz` and `test/test2/arti2.tgz` | [J] [RTFACT-25956](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-25956), [RTFACT-26069](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26069) (Done); [RTFACT-19490](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-19490) and [RTFACT-30182](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30182), open requests for per-folder indexes |
| The index is a file in the repository: `GET /artifactory/<repo>/index.yaml` returns it, and JFrog's workaround for a stale index is to delete that file and recalculate | [J] [RTFACT-21578](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21578) (6.17 capture), [RTFACT-23237](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-23237) (Done) |
| The stored file names each chart `local://<path>`; the index served through `/api/helm/` or a virtual repository carries real URLs. On 7.x a local repository's served index needed the Custom Base URL, and without it kept `local://` | [S] [jfrog/jfrog-cli#686](https://github.com/jfrog/jfrog-cli/issues/686) (a JFrog maintainer's capture of the local and the virtual file, 2020); [J] [RTFACT-26268](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26268) (a 7.21 capture, fixed in 7.25.4 and 7.26.3), [RTFACT-27527](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27527) (2022, closed as deferred) |
| What the stored file holds once relative URLs are the default (section 2.3), and whether `local://` survives anywhere on 7.161 | [I] |
| An empty repository's index is `apiVersion: v1`, `entries: {}` and `generated` | [J] RTFACT-21578 |
| A captured entry: `apiVersion`, `appVersion`, `created` (`2019-08-07T13:44:31.486Z`), `deprecated`, `description`, `home`, `icon`, `name`, `sources`, `urls`, `version`, with keys in alphabetical order and the entries list at the key's indentation; 6.17 wrote `created` with microseconds (`2020-03-18T16:07:45.902990Z`) | [S] jfrog/jfrog-cli#686; [J] RTFACT-21578 |
| From 7.10.5 Artifactory quotes `version` and `appVersion`, so values such as `504e199` or `9.745796e+09` stay strings: "We will add quotes around appVersion and version values in the index.yaml file" | [J] [RTFACT-20334](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20334) (fixed in 7.10.5, JFrog's comment) |
| Other inputs broke indexing until fixed: single quotes doubled on each reindex (7.12.5), commas in a version (7.25.4, 7.26.3), Artifact Hub annotations (7.21.0), an annotation such as `96e29` read as a number (Done), and Windows line endings (7.12.3). A long `description` still breaks it, an open bug | [J] [RTFACT-23973](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-23973), [RTFACT-26268](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26268), [RTFACT-26006](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26006), [RTFACT-26388](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26388), [RTFACT-21994](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21994); [RTFACT-26110](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26110) (open) |
| Known issue RTDEV-27263, from 7.42.1: "Aritfactory adds unnecessary quotes to the `index.yaml` file", no fix version | [RN] known issues |
| A chart with a version Helm rejects, such as `y` or `1.0.0.0`, is indexed anyway, and Helm clients then skip it with a warning on every run; asking Artifactory to refuse or skip such charts is an open request | [J] RTFACT-20334, [RTFACT-23010](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-23010), [RTFACT-26481](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26481), [RTFACT-25805](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-25805) |
| A chart whose metadata cannot be read must be skipped and the rest indexed: a malformed `appVersion` once stopped indexing ("If there is a invalid chart, it should be skipped, and indexing should continue") | [J] [RTFACT-26509](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26509) (Done) |
| Artifactory strips a dependency's `alias` from index entries; the request to keep it was closed. A customer found `helm search` broken by its absence on Helm 3.14.0, and Helm now ignores the validation error it causes | [J] [RTFACT-30329](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30329); [S] Helm `pkg/repo/index.go:399-417` |
| When the same name and version is deployed at several paths, "Artifactory retains only the latest entry and overwrites the URL. The earlier file remains in the repository, but Helm clients cannot view it in `index.yaml`" | [D] kubernetes-helm-chart-repositories, "Prevent Duplicate Chart Paths" |
| Overwriting a chart with new content (the same name and version, another `appVersion` or keywords) must update its entry | [J] RTFACT-23237 (Done); [RTFACT-21578](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21578) (6.17) |
| Whether 7.x writes `serverInfo`, `publicKeys` or `annotations` at the top level, and the exact `created` precision on 7.161 | [I] |

### 2.3 URLs

| Fact | Basis |
|---|---|
| An absolute chart URL is the repository's item path, `<base>/artifactory/<repo>/<path>`, where `<base>` is the scheme and host (in this report, unlike research 04, `<base>` stops before `/artifactory`): `https://yehudah.jfrog.io/artifactory/helm-virtual/acs-engine-autoscaler-2.2.2.tgz`, and JFrog's KB shows `https://<ARTIFACTORY_URL>/helm-virtual/<chart>.tgz` | [S] jfrog/jfrog-cli#686; [KB] 000004492 |
| The base is the Custom Base URL or the request's URL; `X-Artifactory-Override-Base-Url` (the older name of `X-JFrog-Override-Base-Url`) overrides it | [KB] 000004492; [J] [RTFACT-27528](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27528) (open: federated repositories should honour the header "just like it does with the Virtual") |
| That both captures are of virtual repositories; that a local repository's served index uses the same form since local resolution came in 7.15.3 | [J] [RTFACT-18464](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-18464) (fixed in 6.23.13 and 7.15.3); the form [I] |
| From 7.59.5 `artifactory.helm.relative.urls.enabled=true` makes the index carry relative URLs, which are shorter and need Helm 3; it "will only apply to new chart deployments", and a reindex applies it to all | [D] kubernetes-helm-chart-repositories, "Relative URL Support for Helm Repositories" |
| "Starting November 1st, 2024, all upcoming JFrog Artifactory versions will use Helm Relative URL for indexing Helm charts", and drop Helm clients below 3.0.0; the page's top note says Artifactory "has transitioned to using Helm Relative URLs ... by default" | [D] deprecations-in-process; [D] kubernetes-helm-chart-repositories, the note at the top |
| The form of a relative URL: the chart's path from the repository root, which Helm resolves against `<base>/artifactory/api/helm/<repo>/` | [S] Helm `ResolveReferenceURL`; the form on 7.x [I] |
| Whether `artifactory.helm.relative.urls.enabled=false` still gives absolute URLs on 7.161 | [I] |
| Charts are downloaded through the same prefix: `GET /artifactory/api/helm/<repo>/<file>.tgz` | [J] [RTFACT-24682](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-24682) (request logs; the 204 it reports is a bug, Done) |

### 2.4 When the index is regenerated

| Fact | Basis |
|---|---|
| Artifactory computes the index of a local repository itself ("Metadata Calculation") | [D] kubernetes-helm-chart-repositories |
| 6.20's Helm addon adds a deployed chart through "a queue of events" and a local index calculation, and removes a deleted chart by the name and version "extracted from the file's properties, or from the metadata itself if still present" | [S] 6.20 `HelmAddon.java:33-51` |
| From 7.33.6 indexing is event based and incremental, and depends on node events; with `artifactory.node.events.enabled=false` new charts were not indexed until a manual recalculation | [J] [RTFACT-26812](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26812) (Done), [RTFACT-20595](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20595) (fixed in 7.33.6) |
| A deploy that found the index's lock lease expired (`ha.shift.events.lock.lease.period.minutes`, 30 by default) left the index without the chart, fixed in SaaS 7.98.2 (RTDEV-48582) | [RN] [SaaS 7.98.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-7982-saas) |
| A delete must update it, and does | [J] RTFACT-21578; [J] [RTFACT-30033](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30033): "A similar scenario works well ... with the deletion of charts" |
| A move must update the source's index as well as the target's: RTFACT-30033 (JFrog support, Done, 2023) fixed the source; RTFACT-30145 (JFrog support, 7.49.10 to 7.71.4) was closed without a fix version | [J] RTFACT-30033, [RTFACT-30145](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30145) |
| A copy of a chart already in the target must leave its entry: a second `jf rt bpr --copy` removed the latest package's metadata until a recalculation (JFrog support, Done, 2024) | [J] [RTFACT-30758](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30758) |
| Deleting a build with its artifacts must update the index; fixed in 7.84.10 (RTDEV-36665) | [J] [RTFACT-30097](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30097) (JFrog support, Done); [RN] [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed) |
| A recalculation must drop entries the repository no longer backs, including ones from an uploaded `index.yaml`; fixed in 7.12.3 | [J] [RTFACT-18128](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-18128) |
| Remote caches do not trigger the calculation (6.8.0) | [J] [RTFACT-18375](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-18375) |
| "Improved Helm metadata processing": one fewer version comparison per index build (SaaS 7.136.0, self-managed 7.146.7); no change to the protocol | [RN] [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), [SaaS 7.136.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71360-saas) |
| Whether a property change, or a deploy of a non-chart file, triggers a calculation | [I] |

### 2.5 Virtual repositories (P10)

| Fact | Basis |
|---|---|
| A virtual repository merges its members' indexes; from 7.80.0 the merge is computed once per permission set and cached for the Metadata Retrieval Cache Period, which JFrog recommends at 60 seconds or more; a request waits up to six minutes for a merge in progress, and `artifactory.virtual.repo.metadata.merge.lock.timeout.sec.HELM=120` restores the previous behaviour | [D] kubernetes-helm-chart-repositories, "Helm Virtual Repository Index Improvements"; [RN] [SaaS 7.80.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-7800-saas), [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed) |
| The merged index lists the charts of every member the caller can read | [J] [RTFACT-25936](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-25936) (Done) |
| A name and version in two members resolve by resolution order | [D] kubernetes-helm-chart-repositories, the note under "Enable Enforcement on Specific Repositories" |
| With `useNamespaces`, `/helm-virtual/<member>/chart.tgz` names the member (SaaS 7.24.1) | [D] kubernetes-helm-chart-repositories, "Namespace Support for Helm Virtual Repositories" |
| External dependency rewriting turns allowed URLs into `<base>/artifactory/api/helm/<virtual>/_external/<scheme>/<host>/...` | [D] kubernetes-helm-chart-repositories, "Set Multiple External Dependencies" |
| Helm and Helm OCI repositories cannot share a virtual repository | [D] kubernetes-helm-chart-repositories, "Helm OCI Limitations" |

## 3. Charts in a local repository

| Fact | Basis |
|---|---|
| A chart is deployed with a REST `PUT /artifactory/<repo>/<path>`; deploy by checksum and from an archive work too | [D] kubernetes-helm-chart-repositories, "Deploy Helm Charts via API" |
| Every chart file in the repository is indexed, in any folder (section 2.2) | [J] RTFACT-25956, RTFACT-26069 |
| 6.20 takes a file ending `tgz` or `tar.gz` as a Helm file | [S] 6.20 `HelmAddon.java:90-94` (`isHelmFile`) |
| A chart packed by hand may have `Chart.yaml` at the archive's root rather than in one top-level folder (6.0.0); Artifactory must take the chart's own `Chart.yaml`, never a subchart's under `charts/` (6.1.1) | [J] [RTFACT-16103](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-16103), [RTFACT-17119](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-17119) |
| Charts packed by Helm 3 on Windows are indexed from 7.12.3 | [J] [RTFACT-21145](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21145) |
| The provenance file is `<chart>.tgz.prov`, stored "alongside" the chart and served at the chart's URL plus `.prov`; it holds `Chart.yaml`, the SHA-256 of the archive under `files:`, and a PGP signature | [D] Helm `provenance.mdx` |
| JFrog documents nothing about `.prov` files in a classic repository: whether they are checked, whether they follow a chart that is moved, copied or deleted, and what the index says of them | [I] |
| The package search offers three Helm fields, "Chart name", "Chart version" and "Chart AppVersion" (5.9.0), over the properties `chart.name`, `chart.version` and `chart.appVersion` | [J] [RTFACT-15566](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-15566); [S] 6.20 `PackageSearchCriteria.java:322-341` |
| A 7.x chart carries its `appVersion` as a property: JFrog's workaround for a malformed one is to search for "the offending artifact property (since in this example appVersion is created as a artifact property)" | [J] RTFACT-26509 (7.27.10) |
| That 7.161 keeps the keys `chart.name`, `chart.version` and `chart.appVersion`, and sets no other Helm property | [I] |
| Xray does not index Helm repositories | [D] local-repositories, "Enable Indexing in Xray" |

## 4. Enforce Layout

| Fact | Basis |
|---|---|
| Two settings: "Prevent Duplicate Chart Paths" refuses a chart whose name and version already exist at another path of the repository; "Enforce Chart Name and Version" refuses a chart whose file name does not match `Chart.yaml`'s name and version, or whose version is not SemVer 2 | [D] kubernetes-helm-chart-repositories, "Helm Enforce Layout"; [RN] [SaaS 7.104.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71042-saas), [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed) |
| The refusals are 403, with these messages for a chart `chart-1.0.0`: | [D] kubernetes-helm-chart-repositories, Examples |
| - duplicate: `This action is prevented due to the Enforce Layout Policy, a package with the same name and version chart-1.0.0 already exists in the repository.` | |
| - not SemVer: `This action is prevented due to the Enforce Layout Policy, the specified package version chart-1.0.0.0 does not comply with the Semantic Versioning format.` | |
| - file name: `This action is prevented due to the Enforce Layout Policy, the package file chart-1.0. does not match the package name/version chart:2.0.0 in the metadata file.` | |
| - unreadable: `This action is prevented due to the Enforce Layout Policy, the metadata of the package chart-1.0.0 could not be read or is malformed.` | |
| The file-name example reads `chart-1.0.` for a file the page names `chart-1.0.0.tgz`; whether 7.x cuts one character too many or the page has a typo, and the order of the checks when a chart breaks more than one | [I] |
| It is "forward-compatible only": repositories created before 7.104.2 cannot use it, and "Enforcement is set only upon repository creation" | [D] kubernetes-helm-chart-repositories; [RN] SaaS 7.104.2, 7.104.5 |
| It can be turned on "globally for all Helm repositories" under Packages Settings, after which "the enforcement cannot be later disabled on any Helm repositories", or per repository when it is created ("Use Legacy Helm", then "Enforcement Settings") | [D] kubernetes-helm-chart-repositories, "Activate Helm Enforce Layout" |
| The global settings are the descriptor's `repositoriesGeneralConfig.helm.forceMetadataNameVersion` ("block chart upload in case of invalid name or version") and `forceNonDuplicateChart` ("block chart upload if same chart exists in other location under the repository"), both `false` by default, beside `forceOciFirstUiFlow` | [D] configuration XSD, `helmType` |
| The per-repository fields are `forceNonDuplicateChart` and `forceMetadataNameVersion` in the repository JSON; neither JFrog's repository API reference nor the XSD's repository types list them | [S] provider `resource_artifactory_local_helm_repository.go:138-142`; [D] createrepository, configuration XSD; [research 03](03-repository-configuration-model.md#keys-known-only-from-the-terraform-provider) |
| A copy, move or push replication into a repository with enforcement may skip charts that break it | [D] kubernetes-helm-chart-repositories, "Helm Limitations" |
| Enforcement did not work on federated repositories until SaaS 7.131.0 and self-managed 7.125.10 and 7.133.3 (RTDEV-66745) | [RN] [SaaS 7.131.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71310-saas), [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) |
| Updating a Helm local repository created before an upgrade answered 400 until 7.104.7 (SaaS 7.106.4, 7.107.1; RTDEV-53840) | [RN] [7.104.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71047-self-managed), [SaaS 7.107.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71071-saas) |
| What 7.x answers an update that changes either field: refused, ignored or applied | [I] |
| Without enforcement, a duplicate name and version is stored and the newest wins in the index (section 2.2) | [D] kubernetes-helm-chart-repositories |
| Whether "SemVer 2" refuses what Helm accepts, such as `v1.2.3` or `1.2` (Helm's parser is lenient) | [I]; [S] Helm `pkg/chart/metadata.go` (`isValidSemver`, Masterminds `semver.NewVersion`) |

## 5. Reindex

| Fact | Basis |
|---|---|
| `POST /api/helm/<repo>/reindex` "Calculates Helm chart index ... (local and Federated repositories only)" and answers 200 `text/plain` | [D] calculatehelmchartindex |
| It requires "the set of permissions assumed by Manage (Manage + Delete/Overwrite + Deploy/Cache + Annotate + Read)", produces `application/text`, and has existed since 5.8 | [D] archived *Calculate Helm Chart Index* (2026-02-07) |
| `POST /api/helm/<repo>/<path>/reindex` "Recalculates partial indexing" for a folder of charts or one chart archive, with the same permission; sample `POST /artifactory/api/helm/helm-local/org/project/reindex` | [D] helmchartspartialreindexing; kubernetes-helm-chart-repositories, "Helm Charts Partial Re-Indexing"; archived *Helm Charts Partial Re-Indexing* (2025-07-11) |
| Partial reindex arrived in SaaS 7.105.2; the self-managed notes list it in 7.111.4 | [RN] [SaaS 7.105.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71052-saas), [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed); [D] kubernetes-helm-chart-repositories ("available starting from Artifactory version 7.105.2") |
| The UI's "Recalculate Index" schedules the calculation and reports "Recalculating index for repository REPO scheduled to run"; 6.20's UI asks for it asynchronously | [J] [RTFACT-27188](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27188) (7.37.14, open request for progress); [S] 6.20 `HelmIndexCalculator.java:30-39` |
| Whether the REST operations also answer before the calculation ends, and their body text | [I] |
| What partial reindex answers for a missing path, a path holding no chart, and whether a folder is read recursively | [I] |

## 6. Helm OCI

### 6.1 Manifests and media types

| Fact | Basis |
|---|---|
| A chart is an OCI image manifest (`application/vnd.oci.image.manifest.v1+json`) with no `artifactType`, whose config is the chart's `Chart.yaml` as JSON, of type `application/vnd.cncf.helm.config.v1+json`, and whose layers are the chart, `application/vnd.cncf.helm.chart.content.v1.tar+gzip`, and, when a `.prov` was beside it, `application/vnd.cncf.helm.chart.provenance.v1.prov` | [S] Helm `pkg/registry/constants.go:26-36`, `client.go:741-858,1007-1024` (`Push`, `tagManifest`); [D] Helm `registries.mdx`, "Helm chart manifest" |
| Helm still pulls the legacy layer type `application/tar+gzip`, with a warning | [S] Helm `client.go:550,611-613` |
| The manifest carries the OCI annotations `org.opencontainers.image.{title,version,description,url,created,source,authors}` from `Chart.yaml`, and the chart's own annotations | [S] Helm `pkg/registry/util.go:127-175` |
| A Helm OCI repository can hold any OCI artifact, not only charts | [D] kubernetes-helm-chart-repositories, "Helm OCI Limitations" |
| Moving, copying or deleting an index manifest does not take its images along | [D] kubernetes-helm-chart-repositories, "Helm OCI Limitations" |
| The referrers API works for Helm OCI from 7.90.1 | [D] kubernetes-helm-chart-repositories, the note under "View Individual Helm OCI Chart Information" |
| Xray does not scan Helm charts, and its indexing option is hidden for OCI and Helm OCI repositories (SaaS 7.84.3, self-managed 7.84.10; RTDEV-40796) | [D] kubernetes-helm-chart-repositories; [RN] [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed) |
| A Helm OCI package's versions must show as versions, not digests, in the Packages view | [J] [RTFACT-31105](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31105) (JFrog support, 7.104.5, Done) |
| Whether 7.x sets Helm-specific properties on a chart's `manifest.json` | [I] |

### 6.2 Tags, retention and Helm 3.18's two pushes

| Fact | Basis |
|---|---|
| Max Unique Tags (`maxUniqueTags`, 0 by default) and Tag Retention (`dockerTagRetention`, 1 by default) apply to Docker, OCI and Helm OCI local repositories | [D] additional-docker-information, "Tag Retention Logic"; research 23 section 5.3 |
| From 7.75.3 a manifest removed by those limits takes "its nested images, layers, and blobs" with it; before, they stayed and used storage | [D] additional-docker-information, "Tag Retention Logic" |
| Enforce Strict Tag Overwrite applies to Docker, OCI and Helm OCI (SaaS 7.144.2, self-managed 7.146.7); its settings were not applied until 7.146.22 (SaaS 7.155.3, RTDEV-87274) | [RN] [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), [7.146.22](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714622-self-managed) |
| Known issue RTDEV-88173 (no fix version): cleanup policies that keep the last N versions (`numOfVersionsToKeep`) "may retain digest references instead of the human-readable tags", because Helm 3.18.0 and later create "two manifest nodes per version, one for the tag and one for the digest" and "Artifactory counts each manifest node as a separate version". It affects Helm OCI and Docker repositories; JFrog's advice is to raise N | [RN] known issues, "OCI-Compliant Helm Charts" |
| Whether Max Unique Tags and Tag Retention count the digest node of a Helm 3.18 push, and whether removing a tag removes it | [I] |
| A local repository keeps a manifest pushed by digest in `<image>/sha256:<hex>/` (research 23 section 5.1), so a Helm 3.18 push leaves `<chart>/sha256:<hex>/` beside `<chart>/<version>/`, both holding the same `manifest.json` | [S] research 23 section 5.1; [S] helm/helm#30911 |
| Helm OCI repositories got the Enable Redirect Download setting in 7.146.12 (RTFE-4987) | [RN] [7.146.12](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714612-self-managed) |
| Cleanup policies and archiving support Helm OCI (SaaS 7.102.0, self-managed 7.104.5), with N versions from SaaS 7.115.3 and self-managed 7.117.5 | [RN] [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed), [7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed) |

### 6.3 What P6 already does, and what charts need

| Fact | Basis |
|---|---|
| `helmoci` is served by the Docker format and the registry: `docker.Serves` names `docker`, `oci` and `helmoci` | [S] `internal/format/docker/layout.go` (`Serves`) |
| `ParseManifest` accepts the four manifest types whatever the config's media type, so a chart manifest is stored like any image; its layers are not foreign, so each must be in the image before the manifest | [S] `internal/format/docker/manifest.go` (`ParseManifest`, `elsewhere`) |
| `ReadConfig` reads only the OCI and Docker image configs, so a chart sets no `docker.os`, `docker.architecture` or `docker.label.*` | [S] `internal/format/docker/manifest.go` (`ReadConfig`) |
| A chart's tag folder holds `manifest.json` and its blobs as `sha256__<hex>`, the paths `jf helm push` searches; the five manifest properties are set as for any image | [S] decision 0031, "Where a manifest lives" and "Properties" |
| The package index has the image (`<sub>/<chart>`) as the package and each tag folder as a version, of package type `helmoci`, ordered by push; digest folders are not versions | [S] `internal/format/docker/index.go` (`syncTag`, `affected`) |
| `maxUniqueTags` and `dockerTagRetention` are applied to `helmoci` as to Docker, counting tags only; the defaults and the v2 read keep them for `helmoci` | [S] `internal/service/registry/retention.go` (`retain`, `replace`); `pkg/repo/defaults.go`, `pkg/repo/views.go` |
| A tag's previous manifest is kept by Tag Retention in its digest folder unless that folder already exists "because the manifest was also pushed by digest"; a kept manifest is found by its `docker.manifest` naming the tag, so a Helm 3.18 digest folder, which names itself, is neither counted nor removed by Max Unique Tags or Tag Retention | [S] `internal/service/registry/retention.go` (`replace`), `internal/format/docker/retention.go` (`Retained`) |
| Strict Tag Overwrite (DV-035), the tags list, the referrers API, the catalog, Docker promotion of Helm OCI images and download redirects already apply | [S] decision 0031; `api/artifactory.openapi.yaml` (promote: "Helm charts in helmoci repositories are promoted the same way") |
| The recursive property edit `jf helm push` sends is served by the storage API | [S] `internal/server/storage.go` (`editProperties`, `recursive`) |
| What a chart still lacks: anything that reads the chart's version from its config (the tag holds `_` for `+`), and an answer for the digest folder of Helm 3.18's two pushes | [I] |

## 7. Repository creation and the Terraform provider

| Fact | Basis |
|---|---|
| From 7.75.3 the UI creates a Helm OCI repository when Helm is chosen; "Use Legacy Helm" or clearing "Helm repository creation set OCI flow first" in Packages Settings gives a classic one | [D] kubernetes-helm-chart-repositories, "Create a Helm OCI Repository"; [KB] 000005859 |
| That switch is the UI-only descriptor setting `repositoriesGeneralConfig.helm.forceOciFirstUiFlow`, `true` by default | [D] configuration XSD, `helmType` |
| The repository API takes `helm` and `helmoci` as separate package types, so `packageType: helm` creates a classic repository whatever the UI default | [D] createrepository (both values in each class's enumeration); [S] provider, separate `local_helm` and `local_helmoci` resources; the absence of a REST switch [I] |
| `artifactory_local_helm_repository` adds `force_non_duplicate_chart` and `force_metadata_name_version` (JSON `forceNonDuplicateChart`, `forceMetadataNameVersion`), both optional and computed, "Only available for 7.104.0 onward. Cannot be updated after it is set." | [S] provider `resource_artifactory_local_helm_repository.go:42,138-166`; `docs/resources/local_helm_repository.md` |
| The provider refuses a change to either in the plan ("<attr> cannot be updated after it is set."), sends both on every create and update, and reads them back as null when the server leaves them out | [S] provider `resource_artifactory_local_helm_repository.go:89-136,175-219` |
| Its comment says the fields are "filtered out in Create/Update if version doesn't support them", but `HelmEnforceSupportedVersion` is used only in the descriptions | [S] provider `resource_artifactory_local_helm_repository.go:104`; a code search of the repository for the constant |
| `artifactory_local_helmoci_repository` adds `max_unique_tags` (JSON `maxUniqueTags`, default 0, at least 0) and `tag_retention` (JSON `dockerTagRetention`, default 1, at least 1), always sent | [S] provider `resource_artifactory_local_helmoci_repository.go:85-160`; `docs/resources/local_helmoci_repository.md` |
| jfrog-client-go's `HelmLocalRepositoryParams` has no Helm field | [S] jfrog-client-go `localrepository.go:297-303` |

## 8. Deprecations and release-note history from 7.84

| Fact | Basis |
|---|---|
| Helm clients below 3.0.0 reached end of life on 2024-01-01, and from 2024-11-01 Artifactory uses relative URLs and drops them | [D] deprecations-in-process |
| Self-managed 7.84.10 and SaaS 7.84.3: build deletes update the index (RTDEV-36665), virtual `dependency` commands (RTDEV-40052), Xray's option hidden for OCI and Helm OCI (RTDEV-40796) | [RN] [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed) |
| 7.84.10, 7.84.12 and 7.90.5: smart remote fixes (RTDEV-40319, RTDEV-4982, RTDEV-41317), remote and virtual only | [RN] [7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed) |
| SaaS 7.98.2: a deploy that missed the index under an expired lock lease (RTDEV-48582) | [RN] [SaaS 7.98.2](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-7982-saas) |
| SaaS 7.104.2 and self-managed 7.104.5: Helm Enforce Layout; self-managed 7.104.5 and SaaS 7.102.0: Helm OCI in cleanup and archiving | [RN] SaaS 7.104.2, 7.104.5 |
| 7.104.7 (SaaS 7.106.4, 7.107.1): updating Helm local repositories after an upgrade (RTDEV-53840) | [RN] 7.104.7 |
| SaaS 7.105.2 and self-managed 7.111.4: partial reindex | [RN] SaaS 7.105.2, 7.111.4 |
| 7.117.5 (SaaS 7.115.3): cleanup policies keep N versions for Helm OCI; 7.117.15 and 7.125.4 (SaaS 7.123.0): cleanup no longer deletes Helm packages sharing a name prefix (RTDEV-61861) | [RN] 7.117.5, [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed) |
| 7.125.4 (SaaS 7.119.0): Enable Token Authentication on Helm OCI remote repositories (P9) | [RN] 7.125.4 |
| 7.125.10 and 7.133.3 (SaaS 7.131.0): enforcement on federated repositories (RTDEV-66745) | [RN] 7.133.3 |
| 7.146.7 (SaaS 7.136.0, 7.144.2): faster index generation; Strict Tag Overwrite for Helm OCI | [RN] 7.146.7 |
| 7.146.12: Enable Redirect Download for Helm OCI; 7.146.22 (SaaS 7.155.3): Docker, OCI and Helm OCI package settings applied (RTDEV-87274); 7.146.22 and 7.161.15 (SaaS 7.160.0): anonymous Helm OCI smart remotes under the subdomain method (RTDEV-87361, P9) | [RN] 7.146.12, 7.146.22, [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed) |
| The other Helm lines up to 7.161.32 and SaaS 7.174.0, such as those of 7.161.15 and 7.161.16, concern JFrog's own Helm charts for installing Artifactory, not Helm repositories | [RN] self-managed and SaaS notes |

## 9. Corrections to earlier research

| Was | Now |
|---|---|
| Research 04: absolute chart URLs are `<base>/api/helm/<repo>/<chart>-<ver>.tgz` | The captures show `<base>/<repo>/<path>`, with research 04's `<base>`, which includes `/artifactory` (section 2.3) |
| Research 04: URLs are relative "when `artifactory.helm.relative.urls.enabled` is set (7.59.5+)" | Relative URLs have been the default since November 2024 (section 2.3) |
| Research 04: "A local repository regenerates on upload and delete" | Also on move (source and target), copy and build deletes, by JFrog's fixes; incrementally from events since 7.33.6 (section 2.4) |
| Research 04: partial reindex "(7.105.2+)" | SaaS 7.105.2; the self-managed notes list it in 7.111.4 (section 5) |
| Research 04: with enforce layout "the file name must match the Chart.yaml name and version and duplicates answer 403" | Two separate fields, set only at creation, with four 403 messages and a SemVer check; also a global switch (section 4) |
| Research 04: "Nothing in this section is unverified" | Section 11 lists what is not |
| Research 03: the enforce fields as "Helm enforce layout, 7.104.2+" | The same, set only at creation, with the global pair in `repositoriesGeneralConfig.helm` (section 4) |
| Research 23 section 8: digest folders do not count towards Max Unique Tags | Still so; Helm 3.18 and later create one for every chart push (section 6.2), which research 23 now notes |

## 10. artifactweave at the baseline

| Fact | Basis |
|---|---|
| No Helm format exists: `formats.For(repo.Helm)` is the generic handler, which keeps no index | [S] `internal/format/formats/formats.go` |
| `POST /api/helm/{repoKey}/reindex` is generated and answers 501 until the format is a `Reindexer`, then needs an administrator and runs before it answers; #516 moves it to Manage and the queue | [S] `internal/server/reindex.go` (`reindex`, `CalculateHelmChartIndex`) |
| `pkg/repo.Config` has `forceMetadataNameVersion` and `forceNonDuplicateChart`, `false` by default on local and federated repositories, and the v2 read keeps them for `helm` only; nothing stops an update from changing them | [S] `pkg/repo/repo.go`, `defaults.go`, `views.go` |
| `types.ComparatorFor(repo.Helm)` is `CompareSemver`, and Helm OCI's is push order | [S] `pkg/types/version.go` |
| `format.SidecarOwner` means that uploads named as checksum sidecars (`.sha1` and the like) are checksums of their file; only Maven implements it | [S] `internal/format/format.go` (`SidecarOwner`) |
| `item.MimeType` gives `.tgz` `application/x-gzip` and has no entry for `.yaml` or `.prov` | [S] `internal/service/item/item.go` |
| The base URL resolver already names Helm chart URLs among its users, and honours `X-JFrog-Override-Base-Url` | [S] `internal/server/baseurl/baseurl.go` |
| Research 10 gives Helm's protocol errors the plain-text envelope | [S] [research 10](10-scope-and-tiers.md) |

## 11. Inferred and undocumented

Each item below is on [#216](https://github.com/weaveplatform/artifactweave/issues/216) for the live comparison in P19:

1. What a local repository's stored `index.yaml` holds on 7.161 (`local://` URLs, relative paths
   or absolute URLs), and whether `artifactory.helm.relative.urls.enabled=false` still gives
   absolute URLs, of the form `<base>/artifactory/<repo>/<path>` (sections 2.2, 2.3).
2. The exact form of a relative URL (the path from the repository root), the `created`
   precision, the key order, and whether 7.x writes top-level `serverInfo`, `publicKeys` or
   `annotations` (section 2.2).
3. Whether a property change or a deploy of a non-chart triggers a calculation (section 2.4).
4. What `POST /api/helm/<repo>/api/charts` (helm cm-push) answers (section 1.4).
5. What 7.x does with a `.prov` file when its chart is moved, copied or deleted, and whether it
   checks one (section 3).
6. Whether 7.161 sets `chart.name`, `chart.version` and `chart.appVersion`, and no other Helm
   property, on a chart (section 3), and any Helm property on a Helm OCI manifest (section 6.1).
7. The file-name message's `chart-1.0.` and the order of the four checks (section 4).
8. What 7.x answers an update that changes `forceNonDuplicateChart` or
   `forceMetadataNameVersion` (section 4).
9. Whether Enforce Chart Name and Version refuses `v1.2.3` or `1.2`, which Helm accepts
   (section 4).
10. The body of `POST /api/helm/<repo>/reindex` and of the partial reindex, whether either
    answers before the calculation ends, and what the partial reindex answers for a missing path
    or a path without charts, and whether it recurses (section 5).
11. Whether Max Unique Tags and Tag Retention count the digest folder a Helm 3.18 push leaves,
    and whether removing the tag removes it (section 6.2).
12. Whether `GET /api/helm/<repo>/index.yaml` lists charts at paths the caller cannot read, and
    what it answers a caller with no Read in the repository (decision 0039).
13. What `/api/helm/<repo>/` answers for a repository of another type, a missing repository,
    and a method other than `GET` and `HEAD` (decision 0039).
14. Whether 7.161 indexes a chart named `.tar.gz`, as 6.20's `isHelmFile` reads it, and whether
    Enforce Chart Name and Version applies to one (sections 3, 4).

## 12. Versions at the research baseline

| Component | Version |
|---|---|
| Helm | v3.22.1 (`d6c1a0e4027c`) and v4.3.1 (`c0d08d0d6d94`) |
| Helm documentation (`helm/helm-www`) | `9cc276f46afb`, read 2026-10-19 |
| `chartmuseum/helm-push` | `a8fa26fa3885` (release v0.11.1) |
| jf | 2.127.0 (`fd6c2ebec142`) |
| jfrog-cli-artifactory | `4a0f3827cfa0` |
| jfrog-client-go | `6053434e9301` |
| `jfrog/terraform-provider-artifactory` | v12.11.14 (`990e8dfdb64e`) |
| Artifactory OSS | 6.20 (`knottnhupfer/artifactory-oss` `b99e5649bcad`) |

## 13. Artifactory 7.x check

Checked against Artifactory 7.161.32 and SaaS 7.174.0 on 2026-10-19 ([#529](https://github.com/weaveplatform/artifactweave/issues/529)).

**Sources.** The pages, release notes, known issues, KB articles, Jira issues and client sources
listed at the top. The self-managed notes were read from 7.84.3 to 7.161.32 and the SaaS notes from
7.80.0 to 7.174.0 (released 2026-10-15), the newest on 2026-10-19; neither 7.161.32 nor 7.174.0
lists a Helm change.

**Confirmed for 7.x** (research 04's claims, one by one):
- `helm repo add <base>/artifactory/api/helm/<repo>` and `GET .../index.yaml` (section 1.1).
- `index.yaml` with `apiVersion: v1`, `generated` and `entries` by chart name, each entry the
  `Chart.yaml` fields with `created`, `digest` (the archive's SHA-256) and `urls`, newest first
  (sections 2.1, 2.2).
- Relative URLs from 7.59.5 through `artifactory.helm.relative.urls.enabled` (section 2.3).
- Upload by `PUT` beside an optional `.prov` (section 3).
- Enforce Layout from 7.104.2 with 403 answers (section 4).
- Full and partial reindex (section 5).
- Helm OCI through the Docker path with `helmoci`, Helm 3.8 and later, the UI default from
  7.75.3 with `packageType: helm` over REST still classic, no mixing in a virtual repository, and
  Docker promotion of Helm OCI images (sections 6, 7; research 23 section 7).
- The virtual cache and its six-minute merge wait (section 2.5; P10).

**Changed or added in 7.x:**
- Relative URLs became the default for every release after 2024-11-01, and Helm clients below
  3 lost support (sections 2.3 and 8).
- Enforcement is per repository and set only at creation, with a global switch and four
  documented messages; federated repositories enforce it from 7.131.0 and 7.133.3 (section 4).
- Index regeneration covers moves, copies and build deletes, from JFrog's fixes (section 2.4).
- Helm 3.18 and later push every chart twice, and JFrog's cleanup policies count both nodes
  (known issue RTDEV-88173, section 6.2).
- Strict Tag Overwrite, cleanup policies, archiving and download redirects cover Helm OCI
  (sections 6.2, 8).
- Partial reindex is listed in self-managed 7.111.4, after SaaS 7.105.2 (section 5).

**Differs from research 04:** the absolute URL form, the relative default, the triggers of
regeneration and the enforcement model (section 9).

**Unconfirmed for 7.x:** the **[I]** items of section 11, on
[#216](https://github.com/weaveplatform/artifactweave/issues/216). The **[S]** claims from 6.20
(`isHelmFile`, the properties `chart.name`, `chart.version` and `chart.appVersion`, and the
asynchronous recalculation) stand where a 7.x source agrees: the `appVersion` property
([J] RTFACT-26509) and the scheduled recalculation ([J] RTFACT-27188). The property keys and
`.tar.gz` stay on #216.
