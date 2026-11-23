"""Qualify the pinned artifactory_trashcan_config against the server's effects.

--check ENABLED DAYS  the XML descriptor reports the values the provider
                      wrote, and a delete follows them: kept in the trash can
                      when it is on (and purged by a collection pass when the
                      period is zero), final when it is off.
--deleted             destroy left the documented singleton reset (enabled
                      true, 14 days); the fixture repository is removed.
"""

import os
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen
import xml.etree.ElementTree as ET

REPOSITORY = "tf-trashcan-local"


def request(path, method="GET", body=None, want=200, media="application/json"):
    token = os.environ["TF_VAR_access_token"]
    headers = {"Authorization": "Bearer " + token}
    if body is not None:
        headers["Content-Type"] = media
    req = Request(os.environ["TF_VAR_url"] + path, method=method, data=body, headers=headers)
    try:
        with urlopen(req, timeout=15) as response:
            status, payload = response.status, response.read((4 << 20) + 1)
    except HTTPError as error:
        status, payload = error.code, error.read((4 << 20) + 1)
    assert len(payload) <= 4 << 20, "fixture API response exceeded bound"
    assert token.encode() not in payload, "response disclosed caller credential"
    assert status == want, f"{method} {path}: status {status}, expected {want}: {payload[:300]!r}"
    return payload


def exported():
    # The provider decodes these two elements from this document; a missing
    # section would silently drop the resource from state rather than fail.
    # The document is in Artifactory's XSD namespace, as the provider's
    # decoder ignores; match the elements by local name the same way.
    root = ET.fromstring(request("/artifactory/api/system/configuration"))
    ns = root.tag[:root.tag.index("}") + 1] if root.tag.startswith("{") else ""
    section = root.find(ns + "trashcanConfig")
    assert section is not None, "XML descriptor omits trashcanConfig"
    assert root.findtext(f"{ns}gcConfig/{ns}cronExp"), "XML descriptor omits gcConfig/cronExp"
    return section.findtext(ns + "enabled"), section.findtext(ns + "retentionPeriodDays")


def check(enabled, days):
    got = exported()
    assert got == (enabled, days), f"exported trashcanConfig {got}, provider wrote {(enabled, days)}"
    ensure_repository()
    path = f"effects/{enabled}-{days}.txt"
    request(f"/artifactory/{REPOSITORY}/{path}", "PUT", ("trash can " + path).encode(), 201, "application/octet-stream")
    request(f"/artifactory/{REPOSITORY}/{path}", "DELETE", want=204)
    trashed = f"/artifactory/api/storage/auto-trashcan/{REPOSITORY}/{path}"
    if enabled == "false":
        # Off: the delete is final; the trash can view has nothing to show.
        request(trashed, want=404)
        request(f"/artifactory/{REPOSITORY}/{path}", want=404)
        return
    request(trashed)
    if days == "0":
        # Zero days: the next collection pass purges what was trashed.
        request("/artifactory/api/system/storage/gc", "POST", b"{}")
        request(trashed, want=404)


def ensure_repository():
    token = os.environ["TF_VAR_access_token"]
    req = Request(os.environ["TF_VAR_url"] + "/artifactory/api/repositories/" + REPOSITORY,
                  headers={"Authorization": "Bearer " + token})
    try:
        with urlopen(req, timeout=15):
            return
    except HTTPError as error:
        assert error.code in (400, 404), f"repository lookup: status {error.code}"
    request("/artifactory/api/repositories/" + REPOSITORY, "PUT",
            b'{"rclass":"local","packageType":"generic"}', 200)


def deleted():
    got = exported()
    assert got == ("true", "14"), f"destroy left trashcanConfig {got}, want the reset (true, 14)"
    request("/artifactory/api/repositories/" + REPOSITORY, "DELETE")


def main(argv):
    if argv[:1] == ["--check"] and len(argv) == 3:
        check(argv[1], argv[2])
    elif argv == ["--deleted"]:
        deleted()
    else:
        sys.exit("usage: check-trashcan.py --check ENABLED DAYS | --deleted")
    print("artifactory_trashcan_config effects PASS:", " ".join(argv))


if __name__ == "__main__":
    main(sys.argv[1:])
