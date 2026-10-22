"""Assert provider-managed remote configuration and secret boundaries.

Only the acceptance fixture calls this script. It receives test credentials via
its environment and reports field names, never actual secret values or bodies.
"""

import json
import os
from pathlib import Path
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

base = os.environ["TF_VAR_url"]
token = os.environ["TF_VAR_access_token"]
prefix = os.environ["TF_VAR_repository_prefix"]
revision = int(os.environ["TF_VAR_revision"])
urls = json.loads(os.environ["TF_VAR_upstream_urls"])
secrets = [os.environ["TF_VAR_" + name] for name in
           ("upstream_password", "header_secret", "proxy_password")]


def request(path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = Request(base + path, data=data,
                  headers={"Authorization": "Bearer " + token,
                           "Content-Type": "application/json"},
                  method="GET" if body is None else "PUT")
    with urlopen(req, timeout=10) as response:
        data = response.read()
    assert all(secret.encode() not in data for secret in secrets), "API leaked a fixture secret"
    return json.loads(data)


def check(config, fields):
    for name, expected in fields.items():
        assert config.get(name) == expected, "remote field disagrees: " + name


if sys.argv[1] in ("--seed-proxy", "--remove-proxy"):
    # artifactory_proxy requires XML GET, owned by P12/#303. Exercise the
    # repository's actual proxy reference using the implemented strict PATCH.
    entry = None if sys.argv[1] == "--remove-proxy" else {
        "host": "127.0.0.1", "port": int(sys.argv[2]),
        "username": "remote-proxy-user", "password": os.environ["TF_VAR_proxy_password"]}
    body = {"proxies": {os.environ["TF_VAR_proxy_key"]: entry}}
    req = Request(base + "/artifactory/api/system/configuration", method="PATCH",
                  data=json.dumps(body).encode(),
                  headers={"Authorization": "Bearer " + token, "Content-Type": "application/yaml"})
    with urlopen(req, timeout=10) as response:
        assert response.status == 200, "proxy descriptor update failed"
    sys.exit(0)


for name, upstream in urls.items():
    config = request("/artifactory/api/repositories/" + prefix + "-" + name)
    check(config, {"rclass": "remote", "packageType": name, "url": upstream,
                   "description": "remote management revision " + str(revision),
                   "retrievalCachePeriodSecs": 600 + revision})
    assert not config.get("password"), "remote password was not redacted"
    if name == "generic":
        check(config, {"username": "remote-upstream-user", "proxy": prefix + "-proxy",
                       "disableProxy": False, "hardFail": True, "offline": revision == 2,
                       "storeArtifactsLocally": revision == 1, "socketTimeoutMillis": 5000,
                       "missedRetrievalCachePeriodSecs": 61 + revision,
                       "metadataRetrievalTimeoutSecs": 20,
                       "assumedOfflinePeriodSecs": 42 + revision,
                       "unusedArtifactsCleanupPeriodHours": 24 + revision,
                       "allowAnyHostAuth": False, "bypassHeadRequests": True,
                       "retrieveSha256FromServer": True, "propagateQueryParams": True,
                       "enableCookieManagement": True, "enableTokenAuthentication": True,
                       "queryParams": "fixture=management", "synchronizeProperties": True})
        headers = {h["name"]: h for h in config["customHttpHeaders"]}
        assert headers["X-Fixture-Secret"].get("value", "") == "", "header was not redacted"
        assert headers["X-Fixture-Secret"]["sensitive"], "header lost sensitivity"
        assert headers["X-Fixture-Public"]["value"] == "revision-" + str(revision)
    else:
        # Omitted attributes must round-trip to the provider's defaults too.
        check(config, {"hardFail": False, "offline": False, "storeArtifactsLocally": True,
                       "metadataRetrievalTimeoutSecs": 60})
    if name in ("docker", "oci", "helmoci", "helm"):
        suffix = "/charts/**" if name == "helm" else "/blobs/**"
        check(config, {"externalDependenciesEnabled": True,
                       "externalDependenciesPatterns": [upstream + suffix]})
    if name == "docker":
        check(config, {"dockerProjectId": "fixture-project"})
        assert config["blockPushingSchema1"] in (True, "true"), "schema1 policy changed"
    if name == "go":
        check(config, {"vcsGitProvider": "GITHUB"})
    if name in ("maven", "gradle", "ivy", "sbt"):
        check(config, {"handleReleases": True, "handleSnapshots": False,
                       "fetchJarsEagerly": True, "fetchSourcesEagerly": True,
                       "rejectInvalidJars": True, "maxUniqueSnapshots": 5,
                       "remoteRepoChecksumPolicyType": "generate-if-absent",
                       "suppressPomConsistencyChecks": False})
    if name == "pypi":
        check(config, {"pyPIRegistryUrl": upstream + "/registry", "pyPIRepositorySuffix": "simple/"})
    if name == "nuget":
        check(config, {"feedContextPath": "api/v2", "downloadContextPath": "api/v2/package",
                       "v3FeedUrl": upstream + "/v3/index.json", "symbolServerUrl": upstream + "/symbols",
                       "forceNugetAuthentication": True})
    if name == "helm":
        check(config, {"chartsBaseUrl": upstream + "/charts"})
    if name == "terraform":
        check(config, {"terraformRegistryUrl": upstream + "/modules",
                       "terraformProvidersUrl": upstream + "/providers"})

# The REST model rejects an invalid class/format combination. There is no
# remote terraformbackend resource in provider 12.11.14 to manufacture here.
try:
    request("/artifactory/api/repositories/" + prefix + "-invalid-backend",
            {"rclass": "remote", "packageType": "terraformbackend", "url": base})
except HTTPError as error:
    assert error.code == 400, "unexpected remote terraformbackend validation status"
else:
    raise AssertionError("remote terraformbackend unexpectedly accepted")

# Write-only upstream credentials must stay out of Terraform's own state.
state = Path(sys.argv[2]).read_bytes()
for version in (1, 2):
    assert ("Terraform-upstream-" + str(version)).encode() not in state, "write-only password reached Terraform state"

# Scan persistent bytes including SQLite WAL pages: neither new nor previous
# revisions of the fixture credentials may be stored without sealing.
for path in Path(sys.argv[1]).rglob("*"):
    if path.is_file():
        content = path.read_bytes()
        for kind in ("upstream", "header", "proxy"):
            for version in (1, 2):
                value = ("Terraform-" + kind + "-" + str(version)).encode()
                assert value not in content, "fixture credential persisted without sealing"
print("remote configuration, defaults, validation and sealed/redacted secrets: revision", revision)
