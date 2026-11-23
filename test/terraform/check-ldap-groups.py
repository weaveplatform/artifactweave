"""Qualify the pinned LDAP group setting resources through the server's answers.

artifactory_ldap_group_setting_v2 writes and reads the Access API, and
artifactory_ldap_group_setting writes the descriptor by YAML PATCH and reads
the XML descriptor, so an empty plan alone would accept a server that echoed
each back from its own store. Each check therefore reads both settings
through both surfaces, which must agree, and asks for the effect: a refresh
of each setting reaches the group sync, which refuses it because the
connection they read through is disabled. Only the isolated runner's
loopback application is contacted.
"""

import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
import xml.etree.ElementTree as ET


SCENARIO = "deploy/artifactweave/terraform/ldap-groups"
RESOURCES = ["artifactory_ldap_setting_v2", "artifactory_ldap_group_setting_v2", "artifactory_ldap_group_setting"]
GROUPS = "/access/api/v1/ldap/groups"
# 7.x's LdapGroupSetting element order, which the XML descriptor keeps.
ORDER = ["name", "groupBaseDn", "groupNameAttribute", "groupMemberAttribute", "subTree", "filter",
         "descriptionAttribute", "strategy", "enabledLdap", "forceAttributeSearch"]


def inventory(root):
    fixture = root / SCENARIO
    declarations = re.findall(r'^resource "([^"]+)"', (fixture / "main.tf").read_text(), re.M)
    assert declarations == RESOURCES, "LDAP group fixture must manage the connection and both group setting resources"
    versions = (fixture / "versions.tf").read_text()
    provider = re.search(r'artifactory\s*=\s*\{([^}]+)\}', versions)
    assert provider and '"jfrog/artifactory"' in provider.group(), "artifactory provider missing"
    version = re.search(r'version\s*=\s*"([0-9.]+)"', provider.group()).group(1)
    locked = (fixture / ".terraform.lock.hcl").read_text()
    pinned = re.search(r'provider "registry.terraform.io/jfrog/artifactory" \{(.*?)\n\}', locked, re.S)
    locked_version = re.search(r'version\s*=\s*"([0-9.]+)"', pinned.group()) if pinned else None
    assert locked_version and locked_version.group(1) == version, "artifactory provider lock missing or changed"
    makefile = (root / "Makefile").read_text()
    scenarios = re.search(r'^TERRAFORM_SCENARIOS\s*\?=.*?(?=\n\n)', makefile, re.M | re.S)
    assert scenarios and SCENARIO in scenarios.group(), "LDAP groups omitted from required provider scenarios"
    runner = (root / "test/terraform/run.sh").read_text()
    assert SCENARIO + ") apply_ldap_groups ;;" in runner, "LDAP groups scenario has no native apply steps"
    for mode in ["--check", "--deleted"]:
        assert 'python3 -I "$here/check-ldap-groups.py" ' + mode in runner, "LDAP groups runner omits required effect or deletion checks"
    release = re.search(r'^ARTIFACTORY_VERSION=([0-9.]+)$', runner, re.M)
    assert release and release.group(1) == version, "artifactory release pin missing from provider mirror"
    print("LDAP groups provider inventory: three pinned resources and required native lane PASS")


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(method, path):
    base = os.environ["TF_VAR_url"]
    parsed = urlsplit(base)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port, "LDAP group proof requires the owned loopback application"
    req = Request(base + path, method=method, headers={"Authorization": "Bearer " + os.environ["TF_VAR_access_token"]})
    try:
        with build_opener(ProxyHandler({}), NoRedirects()).open(req, timeout=30) as response:
            return response.status, response.read(1 << 20)
    except HTTPError as error:
        return error.code, error.read(1 << 20)


def names():
    prefix = os.environ["TF_VAR_name_prefix"]
    return prefix + "-teams", prefix + "-units"


def access_settings():
    status, body = request("GET", GROUPS)
    assert status == 200, f"group settings list: status {status}"
    return {s["name"]: s for s in json.loads(body)}


def local(tag):
    return tag.rsplit("}", 1)[-1]


def descriptor_settings():
    status, body = request("GET", "/artifactory/api/system/configuration")
    assert status == 200, f"XML descriptor: status {status}"
    out = {}
    for element in ET.fromstring(body).iter():
        if local(element.tag) != "ldapGroupSetting":
            continue
        fields = [(local(child.tag), child.text or "") for child in element]
        order = [name for name, _ in fields]
        assert order == [name for name in ORDER if name in order], f"ldapGroupSetting element order {order}"
        out[dict(fields)["name"]] = dict(fields)
    return out


def refused_refresh(name):
    status, body = request("POST", GROUPS + "/" + quote(name, safe="") + "/refresh?operation=UPDATE")
    assert status == 400 and b"enabled_ldap names no enabled LDAP connection" in body, \
        f"refresh of {name} through the disabled connection: status {status}: {body[:200]!r}"


def check(strategy, sub_tree):
    assert strategy in ("STATIC", "DYNAMIC") and sub_tree in ("true", "false"), "expected STATIC|DYNAMIC and true|false"
    teams, units = names()
    key = os.environ["TF_VAR_connection_key"]
    member = "uniqueMember" if strategy == "STATIC" else "memberOf"
    access, xml = access_settings(), descriptor_settings()
    for name in (teams, units):
        assert name in access and name in xml, f"{name} missing from the Access API or the XML descriptor"
    t = access[teams]
    assert (t["strategy"], t["group_member_attribute"], t["sub_tree"], t["enabled_ldap"]) == \
        (strategy, member, sub_tree == "true", key), f"{teams} read back {t}"
    assert (xml[teams]["strategy"], xml[teams]["subTree"]) == (strategy, sub_tree), f"{teams} in the descriptor {xml[teams]}"
    u = xml[units]
    assert (u["strategy"], u["groupMemberAttribute"], u["subTree"], u["enabledLdap"], u["groupBaseDn"]) == \
        ("HIERARCHICAL", "ou", "false" if sub_tree == "true" else "true", key, ""), f"{units} in the descriptor {u}"
    a = access[units]
    assert (a["strategy"], a["sub_tree"]) == ("HIERARCHICAL", sub_tree != "true"), f"{units} through the Access API {a}"
    status, body = request("GET", "/access/api/v1/ldap/settings/" + quote(key, safe=""))
    assert status == 200 and json.loads(body)["enabled"] is False, f"connection {key}: status {status}"
    for name in (teams, units):
        refused_refresh(name)
    print(f"LDAP groups: {strategy} teams with sub_tree {sub_tree} and HIERARCHICAL units read back through both surfaces and synced PASS")


def deleted():
    teams, units = names()
    access, xml = access_settings(), descriptor_settings()
    for name in (teams, units):
        assert name not in access and name not in xml, f"{name} survived destroy"
        status, _ = request("GET", GROUPS + "/" + quote(name, safe=""))
        assert status == 404, f"{name} after destroy: status {status}"
    status, _ = request("GET", "/access/api/v1/ldap/settings/" + quote(os.environ["TF_VAR_connection_key"], safe=""))
    assert status == 404, f"connection after destroy: status {status}"
    print("LDAP groups destroy: both settings and the connection removed PASS")


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--inventory":
        inventory(Path(sys.argv[2]))
    elif len(sys.argv) == 4 and sys.argv[1] == "--check":
        check(*sys.argv[2:])
    elif len(sys.argv) == 2 and sys.argv[1] == "--deleted":
        deleted()
    else:
        raise SystemExit("expected --inventory ROOT, --check STRATEGY SUB_TREE, or --deleted")
