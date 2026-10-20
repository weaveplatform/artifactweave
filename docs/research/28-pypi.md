# PyPI

Research baseline: **2026-10-19**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-19, by [#527](https://github.com/weaveplatform/artifactweave/issues/527); [section 16](#16-artifactory-7x-check) records the check.

Phase P8 serves PyPI packages from local `pypi` repositories. [Research 04](04-package-protocols.md#pypi)
sketched the protocol from the documentation and the release notes, and its own 7.x check
asked P8 to confirm it claim by claim. This report does that against JFrog's documentation,
release notes, REST reference, public Jira and the clients' sources, and corrects what research
03, 04, 12 and 17 got wrong (section 14). Decision [0037](decisions/0037-pypi.md) records the
choices.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-19 |
| **[RN]** | stated in the Artifactory release notes at the cited entry, read on 2026-10-19 |
| **[S]** | seen in source code at the cited commit |
| **[J]** | recorded in JFrog's public Jira, in the cited issue, read on 2026-10-19; it counts as 7.x behaviour under the rules in the [research README](README.md) |
| **[I]** | inferred from the documented facts; not stated by any source |

No JFrog knowledge-base article on PyPI local repositories turned up; the label **[KB]** is
not used.

Sources:
- **JFrog's documentation**, read with `.md` appended (each page's `updatedAt` in brackets):
  [pypi-repositories](https://docs.jfrog.com/artifactory/docs/pypi-repositories.md) (2026-05-13),
  [package-manager-compatibility-matrix](https://docs.jfrog.com/artifactory/docs/package-manager-compatibility-matrix.md)
  (2026-10-19), [jf-twine](https://docs.jfrog.com/artifactory/docs/jf-twine.md) (2026-07-29),
  [jf-uv](https://docs.jfrog.com/artifactory/docs/jf-uv.md) and
  [jf-pip](https://docs.jfrog.com/artifactory/docs/jf-pip.md) (2026-09-25), and the REST
  reference pages
  [calculatepypirepositorymetadata](https://docs.jfrog.com/artifactory/reference/calculatepypirepositorymetadata.md),
  [getpypilatestpackageversionmetadatainjson](https://docs.jfrog.com/artifactory/reference/getpypilatestpackageversionmetadatainjson.md)
  and [getpypipackageversionmetadatainjson](https://docs.jfrog.com/artifactory/reference/getpypipackageversionmetadatainjson.md)
  (2026-06-15). The three tables on the PyPI page are images; they were read from the image
  files the page links.
- **JFrog's REST reference as the Internet Archive kept it**:
  [Calculate PyPI Repository Metadata](http://web.archive.org/web/20251009074336/https://jfrog.com/help/r/jfrog-rest-apis/calculate-pypi-repository-metadata)
  (2025-10-09) and
  [Get PyPI Package Version Metadata in JSON](http://web.archive.org/web/20250617165309/https://jfrog.com/help/r/jfrog-rest-apis/get-pypi-package-version-metadata-in-json)
  (2025-06-17).
- **The release notes**:
  [self-managed](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases.md)
  (`updatedAt` 2026-10-05, latest 7.161.32 of 6 October 2026),
  [SaaS](https://docs.jfrog.com/releases/docs/artifactory-saas-releases.md) (2026-10-19,
  latest 7.174.0 of 15 October 2026) and
  [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues.md)
  (2026-09-23), which list nothing for PyPI local repositories.
- **JFrog's public Jira**, read through `https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`:
  the RTFACT keys cited below.
- **The specifications**: the PyPA
  [Simple repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/)
  (which now holds PEPs 503, 592, 629, 658, 691, 700 and 714),
  [Names and normalization](https://packaging.python.org/en/latest/specifications/name-normalization/),
  the [binary](https://packaging.python.org/en/latest/specifications/binary-distribution-format/)
  and [source](https://packaging.python.org/en/latest/specifications/source-distribution-format/)
  distribution formats, read from `pypa/packaging.python.org` `main` on 2026-10-19; and PyPI's
  [Upload API](https://docs.pypi.org/api/upload/) and [JSON API](https://docs.pypi.org/api/json/)
  (`pypi/warehouse` `main`, `docs/user/api/`).
- **The clients**: twine **7.0.0** (`fdb86cb45de1`) `twine/commands/upload.py`,
  `twine/repository.py`, `twine/package.py`, `twine/utils.py` and `docs/changelog.rst`, and
  twine **5.1.1** `twine/commands/upload.py`; pip **26.2.1**
  `src/pip/_internal/index/collector.py` and `src/pip/_internal/models/link.py`; uv **0.13.1**
  `crates/uv-publish/src/lib.rs` and `crates/uv-client/src/registry_client.rs`; Poetry
  **2.5.1** `src/poetry/publishing/uploader.py`.
- **The JFrog CLI**: jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/python/`
  (`python.go`, `pip.go`, `pipenv.go`, `twine.go`, `uv.go`, `native_uv.go`, `poetry.go`,
  `pypirc.go`, `dependencies/dependencies.go`); jfrog-client-go `3b4b900d8174`,
  `artifactory/services/utils/aqlquerybuilder.go`.
- **terraform-provider-artifactory** v12.11.14: `docs/resources/local_pypi_repository.md`,
  `pkg/artifactory/resource/repository/local/resource_artifactory_local_generic_repository.go`.
- **Artifactory OSS 6.20** (`knottnhupfer/artifactory-oss` at `b99e5649bcad`), for what 7.x
  sources leave out: `base/config/src/main/java/org/artifactory/common/ConstantValues.java`,
  `base/common/src/main/java/org/artifactory/addon/pypi/PypiAddon.java` and
  `backend/core/src/main/java/org/artifactory/metadata/service/provider/PypiMetadataProvider.java`.
  The PyPI handler itself (`org.jfrog.repomd.pypi`) was never open source.

## 1. Clients and versions

| Fact | Basis |
|---|---|
| Artifactory supports Poetry from 1.2.0 and uv from 0.8.15 to install and publish, pip from 20.2 to install, and twine from 3.3.0 to publish; `setup.py upload` (setuptools, distutils) also publishes | [D] pypi-repositories, "Connect Your PyPI Client to Artifactory" |
| The JFrog CLI's compatibility matrix lists pip 26.2.1, Poetry 2.5.1 and uv 0.13.1 as the latest tested, Poetry 2.1.3, 2.1.1 and 1.8.0 and uv 0.10.12 to 0.11.17 as failing, and Pipenv 2023.7.4 to 2026.6.1 as officially supported. It does not list twine | [D] package-manager-compatibility-matrix |
| Poetry and twine were added as supported clients in 7.104.5 (SaaS 7.103.0), uv in 7.133.3 (SaaS 7.128.0) | [RN] [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed), [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed) |
| Installers read `<base>/api/pypi/<repo>/simple`; publishers post to `<base>/api/pypi/<repo>`, which "does not end with `/simple`" | [D] pypi-repositories |
| pip asks for an index page with `Accept: application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html; q=0.1, text/html; q=0.01` and accepts a response whose `Content-Type` starts with one of those three types | [S] pip `collector.py:80-89,137-141` |
| uv sends `application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html;q=0.2, text/html;q=0.01`, reads the media type before any `;`, needs it to be exactly one of the three, and resolves relative URLs against the response's URL, after redirects | [S] uv `registry_client.rs:640-656,1779-1783` |
| Both read `data-requires-python`, `data-yanked`, `data-core-metadata` (falling back to `data-dist-info-metadata`) and a `#<hash>=<hex>` fragment from HTML, and `requires-python`, `yanked`, `core-metadata` (falling back to `dist-info-metadata`), `hashes` and `upload-time` from JSON; a metadata file is fetched at the file's URL without its fragment plus `.metadata` | [S] pip `link.py:330-420,539-546`; uv `registry_client.rs:1081` |
| Without core metadata, uv reads a wheel's metadata with HTTP range requests (a `HEAD`, then ranges, with `Accept-Encoding: identity`) and falls back to downloading the wheel | [S] uv `registry_client.rs:1151,1201-1283` |

## 2. The simple index (HTML)

| Fact | Basis |
|---|---|
| A request for `/simple` without the trailing slash answers 302 to `/simple/`. The `Location` honours `X-JFrog-Override-Base-Url` since 7.3.0; without that header or a reverse proxy that sets it, it pointed at `localhost:8081` | [J] RTFACT-21395 (fixed in 7.3.0), RTFACT-27315 (7.41.6, deferred), RTFACT-26155; [D] pypi-repositories, "Specify the PyPI Repository on the Command Line" |
| JFrog's intended behaviour is that the `Location` uses the configured custom base URL "just like other Artifactory redirects" when no override header is sent; RTFACT-31494 records the bug and was resolved Done on 2026-10-13, with no fix version yet | [J] RTFACT-31494 |
| A 7.x project page (between 7.37 and 7.71) reads `<html><head><title>Simple Index</title><meta name="api-version" value="2" /></head><body>`, then one anchor per line, `<a href="../../pyspark/2.4.4/pyspark-2.4.4-py2.py3-none-any.whl#sha256=4a99…" rel="internal">pyspark-2.4.4-py2.py3-none-any.whl</a>`, with no `<br />` after each anchor, which earlier versions had | [J] RTFACT-30288 (a customer's capture, open) |
| The page starts with `<!DOCTYPE html>` from 7.33.6 and 7.31.14, after pip 22.0 refused pages without it | [J] RTFACT-26750 (fixed in 7.33.6, 7.31.14 and 6.23.41) |
| The file links are relative, `../../<path in the repository>`, so from `/api/pypi/<repo>/simple/<project>/` they reach `/api/pypi/<repo>/<path>`. In 6.x they were `../../packages/<path>`, and 7.77.6 stopped serving the doubled `packages/packages/` form 7.49.8 accepted | [J] RTFACT-30288, RTFACT-18495 (a 6.7.2 link), RTFACT-30372, RTFACT-22366 |
| A `PUT` to `/api/pypi/<repo>/<path>` deploys the file, as one to `/artifactory/<repo>/<path>` does | [J] RTFACT-29834 |
| The hash fragment is `#sha256=` since 7.15.4 and 6.23.13; before, `#md5=` | [J] RTFACT-18495 (fixed in 7.15.4) |
| 6.x project lists showed `<a data-requires-python=">=3.6" href="infra-pypipkg-test" rel="internal" >infra-pypipkg-test</a><br/>`: the bare name as a relative link. That form came from a 2018 fix after Warehouse's `/simple/<name>/` links broke browsing | [J] RTFACT-20390, RTFACT-17770, RTFACT-16481 (Done, 2018) |
| What a 7.x project list (`/simple/`) holds: its link form, whether it carries `data-requires-python`, and which name it shows | [I] |
| `data-requires-python` must be HTML-escaped; an unescaped `<` broke the page in 2018 | [D] simple repository API; [J] RTFACT-17770 |
| The `yanked` state reaches the project page: 7.84.10 fixed "the `yanked` property was not applied to packages' simple index files" (SaaS 7.84.3) | [RN] [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed), RTDEV-39551 |
| On 7.49.5 a local repository's project page is answered by a query for the items whose property `pypi.normalized.name` equals the project, reading their `pypi.requires.python` and `pypi.yanked` properties | [J] RTFACT-29892 (JFrog's support write-up, with the SQL) |
| The 7.x HTML carries no `pypi:repository-version` meta tag and no `data-gpg-sig`; clients assume API version 1.0 without the tag | [J] RTFACT-30288; [D] simple repository API, "Clients" |
| Project pages carry an `ETag`, and `If-None-Match` answers 304, from 7.98.7 (SaaS 7.92.0) | [RN] [7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed), "Support for PyPI Etag Headers"; [J] RTFACT-30198 (Done, 2024-07-17) |
| The `ETag`'s form, and the `Cache-Control` 7.x sends since RTFACT-29209 (Done, 2022-12-22) asked for one that stops caching nodes from caching the pages; 7.46.8 sent `Content-Type: text/html` and no `Cache-Control` | [J] RTFACT-29209; the values [I] |
| A request for a project the caller may not read answered 404 before 2022, so pip never asked for credentials; RTFACT-17764 was resolved Done in 2022. On 7.41 a limited user got `{"errors":[{"status":403,"message":"{\"error\":\"Forbidden\",\"reason\":\"You do not have permissions to resolve packages.}"}]}` | [J] RTFACT-17764, RTFACT-27479 |
| A request for a project with an unnormalised name (`/simple/my_pkg/`) found nothing on 7.18.3, where pip, which normalises first, worked. The spec lets a repository redirect such a name and requires clients to ask for the normalised one | [J] RTFACT-29973 (closed, no fix); [D] simple repository API |
| Name normalisation: lower case, with every run of `.`, `-` and `_` replaced by one `-` | [D] Names and normalization |
| "Both underscores (_) and dots (.) are allowed in package names. However, they will not be aggregated into one package": `jfrog.pypi` and `jfrog_pypi` "will be treated as two separate packages with the same behavior as in the PyPI registry". PyPI itself normalises both to `jfrog-pypi` | [D] pypi-repositories, note under "Get Started with PyPI"; Names and normalization |
| What that note changes in 7.x's answers, given that project pages are found by `pypi.normalized.name` | [I] |
| From 7.161.15 (SaaS 7.148.0) a negative cache skips the database for repeated lookups of absent projects in a local repository | [RN] [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed) |
| `pip search` (XML-RPC) works against local and virtual repositories only | [D] pypi-repositories, "Use Pip Search" |

## 3. The JSON simple index (PEP 691)

| Fact | Basis |
|---|---|
| JSON indexing is a global opt-in, "Enable simple json format" under Artifactory Settings, Packages Settings, PyPI, from 7.146.7 (SaaS 7.139.1). Local repositories keep both HTML and JSON indexes | [D] pypi-repositories, "Enable JSON Indexing in PyPI Repositories"; [RN] [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), [SaaS 7.139.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71391-saas) |
| It is chosen through `Accept`. "If JSON is requested but not available, the system falls back to HTML. If HTML is requested but not available, the system returns nothing" | [D] pypi-repositories, "Limitations" |
| Before the feature, `Accept: application/vnd.pypi.simple.v1+json`, and also `application/vnd.pypi.simple.v1+html`, answered `{"errors":[{"status":406,"message":"Not Acceptable"}]}` (SaaS, 2024) | [J] RTFACT-30869 (closed) |
| A virtual repository serves JSON only when every member can | [D] pypi-repositories (P10) |
| The JSON project page has `meta.api-version`, `name` (normalised), `files[]` with `filename`, `url`, `hashes`, and optionally `requires-python`, `yanked`, `core-metadata`, `dist-info-metadata` and `gpg-sig`; from API version 1.1 also `versions` and a mandatory `files[].size`, and an optional `files[].upload-time` (`yyyy-mm-ddThh:mm:ss.ffffffZ`). The project list is `{"meta","projects":[{"name"}]}`. URLs may be relative to the response's URL | [D] simple repository API, "JSON-based Simple API" |
| A JSON response's `Content-Type` is `application/vnd.pypi.simple.v1+json`; `text/html` is an alias of the v1 HTML type; a server may answer a type it was not asked for, or 406 | [D] simple repository API, "Content-Types", "Version + Format Selection" |
| Which `api-version` 7.x writes, whether it sends `versions`, `size` and `upload-time` for a local repository, and what it answers without `Accept` | [I] |
| RTFACT-31464 (open) asks for PEP 700's `upload-time` on remote repositories, which uv's `exclude-newer` needs | [J] RTFACT-31464 |

## 4. Core metadata files (PEP 658 and PEP 714)

| Fact | Basis |
|---|---|
| A repository MAY add `data-dist-info-metadata` (PEP 658) or `data-core-metadata` (PEP 714's name for it) to a file's anchor, with `sha256=<hex>` of the metadata file or `true`, and then MUST serve the file's core metadata at its URL plus `.metadata`. The attribute promises a metadata file that installing does not change | [D] simple repository API, "Serve Distribution Metadata" |
| "Currently, enabling Fast Dependency Resolution (PEP 658) for PyPI repositories requires modifying the `artifactory.system.properties` file and performing a full restart"; the request asks for a REST API, or a per-repository setting, instead | [J] RTFACT-31470 (a customer request, open, 2026-05-04) |
| No release note, documentation page or Jira issue names the property, says whether 7.x writes `data-core-metadata` as well as `data-dist-info-metadata`, or for which files. RTFACT-26891, the 2022 request for PEP 658, is still open | [J] RTFACT-26891; [I] |

## 5. Upload

| Fact | Basis |
|---|---|
| twine, uv, Poetry and `setup.py upload` post `multipart/form-data` to the repository URL with `:action=file_upload`, `protocol_version=1`, the file as `content`, `filetype` (`bdist_wheel` or `sdist`), `pyversion` (the wheel's Python tag, or `source`), `metadata_version`, `name`, `version` and the other core metadata fields in snake case, one value per part for multi-use fields | [D] PyPI Upload API; [S] twine `repository.py:63-158`, uv `lib.rs:1160-1218`, Poetry `uploader.py:140-170` |
| twine 7.0.0 sends `sha256_digest` and `blake2_256_digest` (no `md5_digest`), uv 0.13.1 the same two, Poetry 2.5.1 `md5_digest` in hexadecimal as well. PyPI documents `md5_digest` as URL-safe base64 without padding | [S] twine `package.py:269-279`, uv `lib.rs:1200-1203`, Poetry `uploader.py:146-163`; [D] PyPI Upload API |
| twine sends `name` as `re.sub("[^A-Za-z0-9.]+", "-", name)`, which keeps case and dots; uv and Poetry send the metadata's name | [S] twine `package.py:46-54,254-255`; uv `lib.rs:1205-1210` |
| twine sends a detached signature as `gpg_signature` (a file part) and attestations as `attestations`; it warns that a future version may ignore signatures. Artifactory makes nothing of `gpg_signature`: storing it as `.asc` is an open request | [S] twine `commands/upload.py:156-174`, `package.py:272-276`; [J] RTFACT-15972 (open) |
| A twine upload answers 200 (a request log line `POST /api/pypi/pypi … 200`, 2019) | [J] RTFACT-20390 |
| The body of a successful upload | [I] |
| twine retries a 5xx up to five times, aborts on any redirect, fails on any status from 400, and takes anything else as success; uv and Poetry accept any 2xx | [S] twine `repository.py:160-179`, `commands/upload.py:197-216`, `utils.py:253-288`; uv `lib.rs:1364-1380`; Poetry `uploader.py:244` |
| Uploads go to the repository root only. A twine upload to `/api/pypi/<repo>/<folder>` answers 405, which twine shows as `HTTPError: 405 Method Not Allowed`; JFrog's description reads "Currently, Twine is allowed to deploy packages only to the base level" | [J] RTFACT-29834 |
| The body of that 405 | [I] |
| A twine upload whose URL ended in `/simple` answered 415 on 6.21.1 | [J] RTFACT-23876 (a user's comment) |
| uv takes a 405 as a sign that the URL is the index's rather than the upload's | [S] uv `lib.rs:1388-1410` |
| Overwriting a file needs Delete: a user without it got a 403 since 3.6.0. On 7.26.0 a REST deploy over a file answered `{"errors":[{"status":403,"message":"Not enough permissions to delete/overwrite artifact 'test-libs-release-local:abc-test-art-plugin-0.3.jar' (user: 'abc@jfrog.com' needs DELETE permission)."}]}` | [J] RTFACT-6997 (fixed in 3.6.0), RTFACT-26398 |
| twine 5.1.1's `--skip-existing` took that 403 for an existing file (`status == 403 and "overwrite artifact" in text`), and Poetry 2.5.1's still does. twine 6.2.0 removed every rule but PyPI's, so twine 7.0.0 skips only on 409, or on 400 with "already exist" in the reason or the body | [S] twine 5.1.1 `commands/upload.py:58-71`, twine 7.0.0 `commands/upload.py:52-64`, `docs/changelog.rst` (6.2.0, "Remove hacks that support `--skip-existing`"); Poetry `uploader.py:368-387` |
| uv takes a failed upload for an existing one when its `--check-url` index lists a file of that name with the same hash | [S] uv `lib.rs:793-900` |
| Without `name` or `version` in the form, the upload threw "Metadata is not complete, missing name or version" and answered 500 (4.16.0, deferred) | [J] RTFACT-14684 |
| What 7.x answers a digest that does not match the content, a form that names another package than the file, and a file that is not a distribution | [I] |
| Uploading to a virtual repository logged "Uploading to a virtual PyPI repository is not supported" on 6.21.1; the reporter then withdrew the report. 7.104.14 and 7.111.4 fixed a 400 that should have been a 404 when "Python packages were uploaded to a virtual repository" over a local one that hides unauthorised resources | [J] RTFACT-23876; [RN] [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed), RTDEV-52654 (P10) |
| `setup.py register` "is neither required nor supported" | [D] pypi-repositories, "The 'register' command should be omitted" |

## 6. Storage, properties and indexing

| Fact | Basis |
|---|---|
| A twine upload of `infra_pypipkg_test` 0.0.1 is stored at `pypi-local/infra-pypipkg-test/0.0.1/infra_pypipkg_test-0.0.1-py3-none-any.whl`: the folder carries the name with `-` for `_`, the file keeps its own name | [J] RTFACT-20390 (2019), RTFACT-29973 (7.18.3: "JFrog creates hyphen in the path instead of underscore, and but the file name still has underscore in it") |
| Whether the folder is twine's `name` field, the PEP 503 normalised name or `pypi.normalized.name`, which differ only for upper case and dots, and what folder a uv or Poetry upload gets | [I] |
| Packages uploaded through the web UI or the REST API "must be uploaded with `pypi.name` and `pypi.version` properties" for Artifactory "to handle those packages correctly as PyPI packages" | [D] pypi-repositories, "Publish PyPI Packages Manually" |
| "While indexing the newly uploaded packages Artifactory will automatically try to extract required properties from the package metadata": only `zip`, `tar`, `tgz`, `tar.gz`, `tar.bz2`, `egg` and `whl` files | [D] pypi-repositories, "Automatic extraction of properties" |
| "Indexing starts after a 60 second quiet period, counting from the last upload to the current repository"; 6.20 has `pypi.index.quietPeriodSecs` 60 | [D] pypi-repositories; [S] 6.20 `ConstantValues.java:283` |
| Files are indexed wherever they sit: the 7.49.5 project query has no path condition, `jf rt upload … pypi-local/pokemon/` deploys to a folder of the user's choosing, and a file copied from a remote cache to a local repository was served through the index | [J] RTFACT-29892, RTFACT-20305, RTFACT-30372; [D] pypi-repositories, "Publish PyPI Packages Manually" |
| A reindex once skipped files without `pypi.name`, so a deploy followed at once by a restart left the file out; resolved Done in 2023 | [J] RTFACT-19642 |
| "All PyPI packages have the properties `pypi.name`, `pypi.version` and `pypi.summary` set by the uploading client, or later during indexing"; 6.20's metadata service reads the same three keys, with `tar.gz`, `tgz` and `whl` as lead files | [D] pypi-repositories, "Use Artifactory Search"; [S] 6.20 `PypiAddon.java:32-34`, `PypiMetadataProvider.java` |
| 7.x also keeps `pypi.normalized.name`, `pypi.requires.python` and `pypi.yanked` on a local repository's files | [J] RTFACT-29892 (the 7.49.5 query reads them) |
| The release note calls the yank flag "the `yanked` property"; the query reads `pypi.yanked`. What value a yanked file carries | [RN] 7.84.10; [J] RTFACT-29892; the value [I] |
| `pypi-repo/packages/**` and `pypi-repo/simple/**` "are saved namespaces used by Artifactory for internal logic. If you attempt to deploy to these paths, Artifactory returns a 400 error"; the note sits under the publishing tools, so it covers local repositories | [D] pypi-repositories, "Additional PyPI Publishing Tools" |
| A copy from a remote cache into a local repository's `packages/` succeeded on 7.49.8. Whether 7.x refuses copies and moves into either namespace, and the 400's message | [J] RTFACT-30372; [I] |
| Metadata is read from `PKG-INFO` or `METADATA` at depth 0 or 1 of the archive, preferring depth 0 (from 6.9.0; `artifactory.pypi.index.preferMetadataOnRootDepth`). Before, any `METADATA` at any depth could be taken | [J] RTFACT-17032, RTFACT-16868 (fixed in 6.9.0); [S] 6.20 `ConstantValues.java:285` |
| Only the header part of `METADATA` holds fields; a description line that looked like a field once became `pypi.name` | [J] RTFACT-18571; [D] Core metadata specifications |
| A package without `PKG-INFO` or `METADATA` is not indexed and gets no properties | [J] RTFACT-24630 (7.12.5, closed), RTFACT-12609 |
| A wheel's metadata is `{distribution}-{version}.dist-info/METADATA`; an sdist's is `{name}-{version}/PKG-INFO` | [D] binary and source distribution formats |
| What 7.x stores for a local repository's index, if anything: 6.x kept a `simple.html` index file, and remote caches keep `.pypi/<name>.html` | [J] RTFACT-20390, RTFACT-19510, RTFACT-30116, RTFACT-27157; [I] for 7.x local repositories |

## 7. Enforce layout and naming normalisation

| Fact | Basis |
|---|---|
| Both are global system properties in `artifactory.system.properties`, apply to all PyPI repositories, and are off unless set: `artifactory.pypi.enforce.layout = true` and `artifactory.pypi.enforce.naming.normalization = true` | [D] pypi-repositories, "Use PyPI Enforce Layout", "Use PyPI File Path Name Normalization" |
| The page says both exist from 7.70.2; the self-managed notes list them under 7.90.5 | [D] pypi-repositories; [RN] [7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed), "Support for PyPI Name Normalization and Enforce Layout" |
| Neither changes stored files: an upload of a new version or an overwrite is checked | [D] pypi-repositories, "Backward Compatibility" |
| Enforce layout restricts "uploads to artifacts where the file name and version in the path match the package name and version stored in the artifact's metadata". The comparison is literal: `jfrog-pypi-1.0.0.tar.gz` with name `jfrog-pypi` is allowed, with `jfrog.pypi` not; `jfrog_pypi-1.0.0.tar.gz` with `jfrog_pypi` is allowed, with `jfrog-pypi` not; `jfrog.pypi-1.0.0.tar.gz` with `jfrog.pypi` is allowed in one row and refused in the next, which shows the same pair | [D] pypi-repositories, the enforce layout table (image) |
| "Although PyPI allows the use of the dash in the internal metadata names, our enforce layout feature is strict, and your metadata names must always match the file path name" | [D] pypi-repositories, "Use Both" |
| Enforce layout answers 403 `Action prevented due to Enforce Layout policy - package file name/version <ARTIFACT_NAME> does not match the ” + package internal name/version <ARTIFACT_NAME> in the metadata file`, the `” +` being a string join left in the documented text, or 403 `Action prevented due to Enforce Layout policy - could not find metadata file to compare` | [D] pypi-repositories |
| Naming normalisation "blocks the deployment of files with path name/distribution values that do not conform to standard PyPI naming conventions" for `{NAME}-{VERSION}-{OPTIONAL-PARAMETERS}.tar.gz` and `{DISTRIBUTION}-{VERSION}-{OPTIONAL-PARAMETERS}.whl`; its example is a dash in the name segment. `jfrog_pypi-1.0.0.tar.gz` and `jfrog.pypi-1.0.0.tar.gz` are allowed, `jfrog-pypi-1.0.0.tar.gz` is not | [D] pypi-repositories, the normalisation table (image) |
| It answers 403 `Action prevented due to artifact file name normalization requirements - artifact name <ARTIFACT_NAME> does not conform to normalized requirements` | [D] pypi-repositories |
| With both on, a file must pass both; `jfrog-pypi-1.0.0.tar.gz` with name `jfrog.pypi` breaks both | [D] pypi-repositories, the combined table (image) |
| What `<ARTIFACT_NAME>` holds in each message; whether case, other characters or a wheel's tags are checked; whether copies, moves and federated repositories are checked; and which check answers when both fail | [I] |

## 8. Downloads

| Fact | Basis |
|---|---|
| `Range` requests on downloads from local, remote and virtual PyPI repositories, for uv, from 7.133.3 (SaaS 7.128.0). On 6.18.1 the `/api/pypi/` download path answered 200 with the whole file despite `Accept-Ranges: bytes`, while the plain repository path answered 206 on 7.77.6 | [RN] [7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed), [SaaS 7.128.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71280-saas); [J] RTFACT-23108, RTFACT-30389 |
| A 6.18.1 download through `/api/pypi/` carried `ETag` equal to the SHA-1, `X-Checksum-Sha1`, `-Sha256` and `-Md5`, `Cache-Control: public, max-age=31536000`, `Accept-Ranges: bytes` and `Content-Disposition: attachment` | [J] RTFACT-23108 |
| The 7.98.7 `ETag` note is about package indexes, not downloads | [RN] 7.98.7 |

## 9. The JSON API

| Fact | Basis |
|---|---|
| `GET /api/pypi/<repo>/pypi/<package>/json` (the latest version) and `.../<package>/<version>/json`, `application/json`; any user with read permission | [D] getpypilatestpackageversionmetadatainjson, getpypipackageversionmetadatainjson; archived *Get PyPI Package Version Metadata in JSON* |
| Local repositories serve it from 7.111.4 (SaaS 7.108.3) "with most attributes", without the deprecated `releases`, `downloads`, `has_sig` and `bugtrack_url`, the `vulnerabilities` key, and nine `info` keys: `description_content_type`, `dynamic`, `license_expression`, `license_files`, `maintainer`, `maintainer_email`, `project_urls`, `provides_extra`, `requires_dist`. The archived reference lists the same without `releases` | [RN] [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed), [SaaS 7.108.3](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71083-saas); archived reference |
| 7.161.15 (SaaS 7.152.1) adds "support for several metadata fields in the PyPI Simple JSON API": the same nine keys. None of them is a key of the simple index's JSON, so the note means the JSON API's `info` | [RN] [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed), [SaaS 7.152.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71521-saas); [I] for the reading |
| PyPI's JSON API returns `info` (the latest or the named version's metadata), `last_serial`, `releases` (deprecated), `urls[]` (the version's files: `comment_text`, `core-metadata`, `digests` with `md5`, `sha256` and `blake2b_256`, `downloads`, `filename`, `has_sig`, `md5_digest`, `packagetype`, `python_version`, `requires_python`, `size`, `upload_time`, `upload_time_iso_8601`, `url`, `yanked`, `yanked_reason`) and `vulnerabilities`; "the first uploaded data for a release is stored, subsequent uploads do not update it" | [D] PyPI JSON API |
| The latest version is the latest stable release by PEP 440, and a yanked release is not the latest | [J] RTFACT-30744 (Done, 2025-03-20), RTFACT-22366 (fixed in 7.11.1) |
| Remote and virtual repositories serve it too (7.111.4); a remote's 500 was fixed in 7.111.8 | [RN] 7.111.4, [7.111.8](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71118-self-managed), RTDEV-57644; [J] RTFACT-31176 |
| Errors are in the Artifactory envelope: `{"errors":[{"status":500,"message":"Internal server error"}]}` on 7.111.4 | [J] RTFACT-31176 |
| Whether 7.x sends `last_serial`, `package_url`, `project_url`, `release_url` and `docs_url`, which digests a file lists, and what it answers for an unknown project or version | [I] |

## 10. Reindex

| Fact | Basis |
|---|---|
| `POST /api/pypi/<repoKey>/reindex[?async=0/1]` "calculates/recalculates the PyPI packages and repository indexes from the artifact metadata"; "synchronous (the default) or asynchronous"; local repositories only; 200 `text/plain` | [D] calculatepypirepositorymetadata |
| It needs "a user with Manage permissions", and its sample answer, to a call without `async`, is `Recalculating index for pypi repository pypi-local scheduled to run`; since 7.0.0 | archived *Calculate PyPI Repository Metadata* [D] |
| What a synchronous call answers, and when; what it answers for another package type, a remote or virtual repository, or a missing one | [I] |

## 11. What the JFrog CLI sends

| Fact | Basis |
|---|---|
| `jf pip install` (and `jf pipenv install`) appends `-i <rt>/api/pypi/<repo>/simple` (`--pypi-mirror` for pipenv) with the user and password or token in the URL; with an access token and no user, the user is taken from the token | [S] `python.go:130-180` |
| With a build, it collects dependencies from pip's run and fills their checksums with an AQL query per file name in the resolver repository: `items.find({"repo":…,"$or":[{"$and":[{"path":{"$match":"*"},"name":{"$match":"<file>"}}]}]}).include("name","repo","path","actual_md5","actual_sha1","sha256")` | [S] `dependencies/dependencies.go:90-105`; jfrog-client-go `aqlquerybuilder.go:234-246` |
| `jf twine upload` refuses `--repository-url`, `--username`, `--password` and `--config-file`, sets `TWINE_REPOSITORY_URL` to `<rt>/api/pypi/<deploy repo>` and `TWINE_USERNAME`/`TWINE_PASSWORD`, runs twine, finds what twine reported uploading with an AQL search by SHA-256 in the target repository, and sets `build.name`, `build.number`, `build.timestamp` and the CI VCS properties on those files | [S] `twine.go:27-43,124-236,253-265` |
| `jf pip-config` (or `jf pipenv-config`) gives `jf twine` its deploy repository | [D] jf-twine |
| `jf uv` runs uv with `UV_INDEX_<NAME>_USERNAME`/`_PASSWORD` and `UV_PUBLISH_*` credentials for indexes on the configured host, takes `publish-url` from `[tool.uv]` in `pyproject.toml`, records dependencies from `uv.lock` with checksums from an AQL query on the index's repository, and for `uv publish` finds each `dist/` file by name in the target repository and sets the build properties on it | [S] `native_uv.go:78-150,940-1000,1090-1200`; [D] jf-uv |
| `jf uv` setup writes an `[[index]]` named `jfrog-pypi` with the `/simple` URL and a `publish-url` without `/simple`, and logs in with `uv auth login --username … --password …`, because `--token` makes the user `__token__`, "a PyPI convention Artifactory doesn't recognize" | [S] `uv.go:93-174` |
| `jf poetry publish` runs Poetry with the repository configured at `<rt>/api/pypi/<repo>` (the `/simple` suffix stripped) and records the `dist/` files from disk, without asking Artifactory | [S] `poetry.go:111-186,294-345` |
| The CLI's `.pypirc` writer puts `repository = <rt>/api/pypi/<repo-name>/`, with a trailing slash | [S] `pypirc.go:15-27` |

## 12. Repository configuration and the Terraform provider

| Fact | Basis |
|---|---|
| `artifactory_local_pypi_repository` takes only the common local repository arguments: the provider builds it with the generic local resource | [S] provider `docs/resources/local_pypi_repository.md`, `resource_artifactory_local_generic_repository.go:30-50` |
| A PyPI repository's default layout is `simple-default` | [S] provider default layout map ([research 03](03-repository-configuration-model.md#repository-layouts)) |
| A local PyPI repository has no fields of its own: enforce layout and naming normalisation are system properties, the JSON index a global setting, and PEP 658 a system property (section 4) | [D] pypi-repositories; [J] RTFACT-31470 asks for a per-repository field because there is none |

## 13. Release notes from 7.84

| Release (self-managed; SaaS) | Change | Basis |
|---|---|---|
| 7.84.10; SaaS 7.84.3 | The `yanked` property reaches simple index files (RTDEV-39551); faster PyPI | [RN] 7.84.10 |
| 7.90.5 | Name normalisation and enforce layout | [RN] 7.90.5 |
| 7.98.7; SaaS 7.92.0 | `ETag` on package indexes | [RN] 7.98.7 |
| 7.104.5; SaaS 7.103.0 | Poetry and twine supported | [RN] 7.104.5 |
| 7.111.4; SaaS 7.106.2, 7.108.3, 7.110.1 | The JSON API in local, remote and virtual repositories | [RN] 7.111.4 |
| 7.111.8 | A remote's JSON API 500 fixed (RTDEV-57644) | [RN] 7.111.8 |
| 7.133.3; SaaS 7.128.0 | uv supported; `Range` on downloads | [RN] 7.133.3 |
| 7.146.7; SaaS 7.139.1 | The opt-in JSON simple index | [RN] 7.146.7 |
| 7.146.22; SaaS 7.157.0 | JSON version processing optimised | [RN] [7.146.22](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714622-self-managed) |
| 7.161.15; SaaS 7.148.0, 7.152.1, 7.153.2 | The nine JSON API `info` keys; a negative cache for absent projects in local repositories; faster remote JSON | [RN] 7.161.15 |
| SaaS 7.165.0 | A virtual repository over a priority-resolution remote returned an empty project page (RTDEV-92672) | [RN] [SaaS 7.165.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71650-saas) (P10) |

7.161.16 to 7.161.32 and SaaS 7.166.0 to 7.174.0 list nothing for PyPI.

## 14. Corrections to earlier research

| Was | Now |
|---|---|
| Research 17: the JSON index is opt-in from 7.139.1 | 7.146.7 self-managed; 7.139.1 is the SaaS release (section 3) |
| Research 17: `Range` on downloads from 7.128.0 | 7.133.3 self-managed; 7.128.0 is the SaaS release (section 8) |
| Research 17 and research 12: no evidence Artifactory serves PEP 658 | A customer request says a system property turns it on (RTFACT-31470); its name and attributes are unknown (section 4) |
| Research 04 and 17: twine uploads stored at `<name>/<version>/<filename>`, or "inconclusive" | `<name>/<version>/<filename>`, the folder with `-` for `_` (RTFACT-20390, RTFACT-29973); upper case and dots are unknown (section 6) |
| Research 04: `packages/**` and `simple/**` reserved in remote caches | Reserved for deploys in every PyPI repository, local ones included (section 6) |
| Research 04: anchors carry `data-gpg-sig`, both metadata attributes, and pages `<meta name="pypi:repository-version" content="1.0">` | 7.x pages carry `<meta name="api-version" value="2" />`, `rel="internal"` and `#sha256=`, no `data-gpg-sig`, and metadata attributes only with the PEP 658 property (sections 2 and 4) |
| Research 04: properties `pypi.name`, `pypi.version` and `pypi.summary` | Also `pypi.normalized.name`, `pypi.requires.python` and `pypi.yanked` (section 6) |
| Research 04: the enforce-layout setting from 7.70.2 | 7.70.2 in the documentation, 7.90.5 in the release notes (section 7) |
| Ticket #527: twine's `--skip-existing` understands 409, 403 and 400 | twine 6.2.0 dropped the 403 rule (Artifactory's); Poetry keeps it (section 5) |
| Ticket #527: `ETag` on downloads from 7.98.7 | On package indexes (section 2); downloads carried one already (section 8) |
| Ticket #527 and #538: the `yanked` property | The 7.x query reads `pypi.yanked` (section 6) |
| `surfaceFor` on main (research 24 fact 2.16) answers PyPI errors in plain text | Every 7.x error captured under `/api/pypi/` is in the Artifactory envelope: 403 (7.41), 406 (2024) and 500 (7.111.4) (sections 2, 3 and 9) |

## 15. Inferred and undocumented

Each item below goes on #216 for the live comparison in P19:

1. The folder of an upload whose name has upper case or dots, by twine, uv and Poetry; and
   whether it is `pypi.normalized.name` (section 6).
2. What the note that `jfrog.pypi` and `jfrog_pypi` "will not be aggregated" changes in the
   project list, the project pages and the JSON API (section 2).
3. A 7.x project list's link form, name and attributes (section 2).
4. The `ETag` form of the index pages, and the `Cache-Control` they send (section 2).
5. The JSON index: `meta.api-version`, whether `versions`, `size` and `upload-time` are sent
   for a local repository, and the answer without `Accept` (section 3).
6. PEP 658: the system property's name, its default, which attribute names 7.x writes, for
   which files, and the `.metadata` answer when it is off (section 4).
7. The bodies of a successful upload and of the 405 below the root, and what 7.x answers a
   digest mismatch, a form naming another package than the file, a file that is not a
   distribution, `:action=submit`, and a non-multipart `POST` (section 5).
8. Whether the twine path counts a same-content re-upload as an overwrite needing Delete
   (section 5).
9. The value a yanked file's `pypi.yanked` carries, and whether `yanked` is read too
   (section 6).
10. The 400 for `packages/**` and `simple/**`: its message, and whether copies and moves are
    refused (section 6).
11. What 7.x stores for a local repository's index (section 6).
12. Enforce layout and naming normalisation: `<ARTIFACT_NAME>` in each message, case and
    other characters, wheels' tags, copies, moves and federated repositories, and which
    check answers first (section 7).
13. The JSON API's `last_serial`, web URLs, digests and 404 (section 9).
14. A synchronous reindex's answer and timing, and the answers for another type, a remote or
    virtual repository, and a missing one (section 10).
15. Whether `/simple/<unnormalised>/` redirects on 7.161 (section 2).
16. RTFACT-31494's base URL fix: in which 7.x release it lands (section 2).
17. The claims that rest on Jira alone, in issues neither fixed in a 7.x release nor answered
    by JFrog staff in the issue: the `pypi.normalized.name`, `pypi.requires.python` and
    `pypi.yanked` properties and the lookup by name (RTFACT-29892); the page's head, relative
    links and `rel="internal"` (RTFACT-30288); the overwrite 403's text on the upload path
    (RTFACT-26398 shows it on a REST deploy); the upload folder (RTFACT-20390, RTFACT-29973);
    the 405 below the root and `PUT` under `/api/pypi/` (RTFACT-29834); 401 or 403 rather than
    404 for a project the caller may not read (RTFACT-17764, Done without a fix version); the
    PEP 658 property (RTFACT-31470); and the metadata depth rule, fixed in 6.9.0
    (RTFACT-17032, RTFACT-16868).
18. Whether 7.x indexes a file whose metadata `Name` is no valid project name, or whose
    `Version` holds a `/`, a space or a control character, and how (section 6; #537).
19. Whether 7.x takes the `Requires-Python` of a file indexed by its properties alone from
    `pypi.requires.python` (section 6; #537).
20. Whether 7.x reads a distribution whose extension is in upper case, such as `.WHL`
    (section 6; #537).

## 16. Artifactory 7.x check

Checked against Artifactory 7.161.32 and SaaS 7.174.0 on 2026-10-19 ([#527](https://github.com/weaveplatform/artifactweave/issues/527)).

**Sources.** The pages, release notes, Jira issues and client sources listed at the top,
read on 2026-10-19. The self-managed notes start at 7.84.3; the 7.84.10 entry rolls up changes
since 7.77. The top of both release-note pages was re-read on 2026-10-19: 7.161.32 (6 October
2026) and SaaS 7.174.0 (15 October 2026) are the latest, and neither changes PyPI.

**Confirmed for 7.x** (research 04's claims):
- The URL layout: `simple/`, the redirect from `/simple` (RTFACT-21395, fixed in 7.3.0), the
  upload at the root and the JSON API's two routes (sections 2, 5 and 9).
- The JSON index as an opt-in global setting with an HTML fallback (section 3).
- The `pypi.name`, `pypi.version` and `pypi.summary` properties, and `packages/**` and
  `simple/**` answering 400 to deploys (section 6).
- The `#sha256=` fragment (RTFACT-18495, fixed in 7.15.4) and the doctype (RTFACT-26750, fixed
  in 7.33.6) (section 2).
- The remote's `pyPIRegistryUrl` and suffix, unchanged ([research 03](03-repository-configuration-model.md)).

**Changed or added in 7.x:** section 13, and the corrections of section 14.

**Unconfirmed for 7.x:** the items of section 15, item 17 among them for what rests on Jira
alone. The 6.20 claims, the quiet period and the three `pypi.*` keys, are confirmed by the
7.x documentation; the metadata depth rule is item 17.
