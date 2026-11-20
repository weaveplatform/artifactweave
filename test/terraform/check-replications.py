"""Assert pinned-provider replication configuration without logging secrets."""

import json
import os
from pathlib import Path
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT_PATH = "/artifactory/api/replications/"
SECRETS = tuple("Terraform-replication-" + kind + "-" + str(revision)
                for kind in ("target", "upstream") for revision in (1, 2))


def request(path, method="GET", body=None, want=200):
    data = None if body is None else json.dumps(body).encode()
    req = Request(os.environ["TF_VAR_url"] + path, method=method, data=data,
                  headers={"Authorization": "Bearer " + os.environ["TF_VAR_access_token"],
                           "Content-Type": "application/json"})
    try:
        with urlopen(req, timeout=20) as response:
            status, payload = response.status, response.read()
            if method == "GET" and status == 200:
                assert response.headers.get_content_type() == "application/json", "configuration read has wrong media type"
    except HTTPError as error:
        status, payload = error.code, error.read()
    assert all(secret.encode() not in payload for secret in SECRETS), "API leaked a fixture credential"
    assert status == want, f"{method} {path}: status {status}, expected {want}"
    return json.loads(payload) if payload else None


def proxy(remove):
    value = None if remove else {"host": "127.0.0.1", "port": int(sys.argv[2])}
    # artifactory_proxy requires XML descriptor reads owned by P12. Use the
    # already-implemented strict PATCH to provide the referenced proxy here.
    data = json.dumps({"proxies": {os.environ["TF_VAR_proxy_key"]: value}}).encode()
    req = Request(os.environ["TF_VAR_url"] + "/artifactory/api/system/configuration", data=data, method="PATCH",
                  headers={"Authorization": "Bearer " + os.environ["TF_VAR_access_token"], "Content-Type": "application/yaml"})
    with urlopen(req, timeout=20) as response:
        assert response.status == 200, "fixture proxy update failed"


def check(keys_file, identity_file, data_dir, server_log):
    keys = json.loads(keys_file.read_text())
    assert set(keys) == {"push", "pull"} and len(set(keys.values())) == 2, "both single-replication resources are required"
    revision = int(os.environ["TF_VAR_revision"])
    identities = {}
    for direction, key in keys.items():
        value = request(ROOT_PATH + key)
        expected = {"repoKey": key, "enabled": revision == 2, "syncDeletes": revision == 2,
                    "syncProperties": revision == 1, "enableEventReplication": False,
                    "checkBinaryExistenceInFilestore": False,
                    "cronExp": "0 0 2 * * ?" if revision == 1 else "0 30 3 * * ?",
                    "includePathPrefixPattern": "org/**" if revision == 1 else "release/**",
                    "excludePathPrefixPattern": "**/temporary/**"}
        expected["url"] = os.environ["TF_VAR_target_url" if direction == "push" else "TF_VAR_upstream_url"]
        if direction == "push":
            expected.update({"username": "replicator-" + str(revision), "socketTimeoutMillis": 15000 if revision == 1 else 25000,
                             "syncStatistics": revision == 2, "proxyRef": os.environ["TF_VAR_proxy_key"], "disableProxy": revision == 2})
            assert value.get("password") and value["password"] not in SECRETS, "push password was not masked"
        for field, want in expected.items():
            assert value.get(field) == want, direction + ": configuration mismatch: " + field
        identity = value.get("replicationKey")
        assert isinstance(identity, str) and identity, direction + ": no server-owned target identity"
        assert not any("revision" in field.lower() for field in value), "private worker revision reached the API"
        identities[direction] = identity
        print(direction + ": revision " + str(revision) + " JSON fields/redaction PASS")
    if revision == 1:
        identity_file.write_text(json.dumps(identities))
    else:
        assert json.loads(identity_file.read_text()) == identities, "configuration update changed stable target identity"
    for path in data_dir.rglob("*"):
        if path.is_file():
            content = path.read_bytes()
            assert all(secret.encode() not in content for secret in SECRETS), "replication credential persisted without sealing"
    log = server_log.read_bytes()
    assert all(secret.encode() not in log for secret in SECRETS), "replication log disclosed a credential"
    for line in log.splitlines():
        entry = json.loads(line)
        assert entry.get("path") not in ("/artifactory/controlled-replication-target", "/artifactory/controlled-replication-source"), "configuration performed deferred transfer I/O"
    print("Both single-target resources: fields, stable identity, sealed/redacted credentials and no transfer I/O PASS")


mode = sys.argv[1]
if mode in ("--proxy", "--remove-proxy"):
    proxy(mode == "--remove-proxy")
elif mode == "--check":
    check(*(Path(value) for value in sys.argv[2:]))
elif mode == "--deleted":
    for direction, key in json.loads(Path(sys.argv[2]).read_text()).items():
        request(ROOT_PATH + key, want=404)
        print(direction + ": deleted configuration GET404 PASS")
else:
    raise SystemExit("unknown replication conformance mode")
