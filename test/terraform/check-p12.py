"""Qualify the seven P12 Terraform resources through the server's own answers.

The configuration, reverse-proxy and ldap-groups references manage
platform_reverse_proxy, artifactory_backup, artifactory_mail_server,
artifactory_property_set, artifactory_proxy, artifactory_repository_layout
and artifactory_ldap_setting_v2. An empty plan shows only that the provider
reads back what it wrote, which a server echoing its own store would pass, so
each --check reads every resource of a scenario through the surface other
clients read (the XML descriptor, the webServer API, the Access LDAP API)
against the driver's TF_VAR_* inputs, and asks for an effect where the server
has one: the layout drives a version search and the reverse proxy the nginx
snippet. Repositories and LDAP connections are not read from the XML
descriptor, which 7.161.26 does not carry them in (#1893).

run.sh records each step it takes (--step) and each --check and --deleted
records its verification in the private ledger P12_LEDGER. --complete fails a
run in which a resource of any P12 scenario that ran lacks a required step,
scans the run's persistent data, logs and retained evidence for the fixture
secrets, and only then writes sanitized provenance: digests, versions, step
names and counts, never a secret or Terraform state. Only the isolated
runner's loopback application is contacted.
"""

import hashlib
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
import xml.etree.ElementTree as ET


CONFIGURATION = "deploy/artifactweave/terraform/configuration"
REVERSE_PROXY = "deploy/artifactweave/terraform/reverse-proxy"
LDAP = "deploy/artifactweave/terraform/ldap-groups"
# The seven resources #1837 qualifies, by the scenario that manages each.
SCENARIOS = {
    CONFIGURATION: ["artifactory_proxy", "artifactory_property_set", "artifactory_backup",
                    "artifactory_mail_server", "artifactory_repository_layout"],
    REVERSE_PROXY: ["platform_reverse_proxy"],
    LDAP: ["artifactory_ldap_setting_v2"],
}
SEVEN = {"platform_reverse_proxy", "artifactory_backup", "artifactory_mail_server", "artifactory_property_set",
         "artifactory_proxy", "artifactory_repository_layout", "artifactory_ldap_setting_v2"}
FUNCTIONS = {CONFIGURATION: "apply_configuration", REVERSE_PROXY: "apply_reverse_proxy", LDAP: "apply_ldap_groups"}
PROVIDERS = {"artifactory": "ARTIFACTORY_VERSION", "platform": "PLATFORM_VERSION"}
# Two revisions, each applied, planned empty and verified, then destroy and
# the absence (or the documented singleton state) checked. A secret-bearing
# scenario also verifies an omitted password kept and a cleared one gone.
STEPS = ["apply-1", "plan-1", "verify-1", "apply-2", "plan-2", "verify-2", "destroy", "deleted"]
SECRET_STEPS = {CONFIGURATION: ["verify-retained", "verify-cleared"], LDAP: ["verify-retained", "verify-cleared"]}
# Every P12 fixture secret carries this marker, so the scan needs no copy of
# a secret, and --check refuses a fixture secret without it rather than let
# the scan pass vacuously.
MARKER = "private-fixture"
SECRET_VARIABLES = {CONFIGURATION: ["TF_VAR_proxy_password", "TF_VAR_mail_password"], LDAP: ["TF_VAR_manager_password"]}
CONFIGURATION_XML = "/artifactory/api/system/configuration"
WEB_SERVER = "/artifactory/api/system/configuration/webServer"
SNIPPET = "/artifactory/api/system/configuration/reverseProxy/nginx"
LDAP_SETTINGS = "/access/api/v1/ldap/settings/"
# The layout effect: a generic repository on the qualified layout, holding
# one file each revision's artifact pattern reads as a version.
LAYOUT_REPOSITORY = "tf-configuration-layout-effect"
LAYOUT_FILES = {"1.0": "acme/tool/1.0/tool-1.0.bin", "2.0": "releases/acme/tool/2.0/tool-2.0.bin"}
METHODS = {"SUBDOMAIN": "subDomain", "REPOPATHPREFIX": "path"}


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def inventory(root):
    makefile = (root / "Makefile").read_text()
    listed = re.search(r'^TERRAFORM_SCENARIOS\s*\?=.*?(?=\n\n)', makefile, re.M | re.S)
    runner = (root / "test/terraform/run.sh").read_text()
    covered = set()
    for scenario, resources in SCENARIOS.items():
        fixture = root / scenario
        declared = re.findall(r'^resource "([^"]+)"', (fixture / "main.tf").read_text(), re.M)
        for resource in resources:
            assert resource in declared, f"{scenario} no longer declares {resource}"
        covered |= set(resources)
        assert listed and scenario in listed.group(), f"{scenario} omitted from the required provider scenarios"
        assert f"{scenario}) {FUNCTIONS[scenario]} ;;" in runner, f"{scenario} has no native apply steps"
        versions = (fixture / "versions.tf").read_text()
        locked = (fixture / ".terraform.lock.hcl").read_text()
        minimum = re.search(r'required_version\s*=\s*">=\s*(\d+)\.(\d+)', versions)
        assert minimum and tuple(map(int, minimum.groups())) >= (1, 17), f"{scenario} must require Terraform >=1.17"
        for name in re.findall(r'source\s*=\s*"jfrog/([a-z]+)"', versions):
            block = re.search(name + r'\s*=\s*\{([^}]+)\}', versions)
            version = re.search(r'version\s*=\s*"([0-9.]+)"', block.group(1)).group(1)
            pin = re.search(r'^' + PROVIDERS[name] + r'=([0-9.]+)$', runner, re.M)
            assert pin and pin.group(1) == version, f"{scenario} pins {name} {version}, which run.sh does not mirror"
            lock = re.search(r'provider "registry.terraform.io/jfrog/' + name + r'" \{(.*?)\n\}', locked, re.S)
            assert lock and f'version     = "{version}"' in lock.group(1), f"{scenario} lock for {name} missing or changed"
    assert covered == SEVEN, f"P12 scenarios cover {sorted(covered)}, not the seven resources"
    for mode in ["--check", "--deleted", "--step", "--complete"]:
        assert 'python3 -I "$here/check-p12.py" ' + mode in runner, f"run.sh omits check-p12.py {mode}"
    print("P12 provider inventory: seven pinned resources in three required scenarios PASS")


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(method, path, data=None, content_type=None):
    base = os.environ["TF_VAR_url"]
    parsed = urlsplit(base)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port, \
        "P12 proof requires the owned loopback application"
    headers = {"Authorization": "Bearer " + os.environ["TF_VAR_access_token"]}
    if content_type:
        headers["Content-Type"] = content_type
    req = Request(base + path, data=data, method=method, headers=headers)
    try:
        with build_opener(ProxyHandler({}), NoRedirects()).open(req, timeout=30) as response:
            status, body = response.status, response.read(4 << 20)
    except HTTPError as error:
        status, body = error.code, error.read(4 << 20)
    # No answer may carry a fixture secret, whatever its status.
    assert MARKER.encode() not in body, f"{method} {path} returned a fixture secret"
    return status, body


def get_json(path):
    status, body = request("GET", path)
    assert status == 200, f"GET {path}: status {status}"
    return json.loads(body)


def local(tag):
    return tag.rsplit("}", 1)[-1]


def descriptor():
    status, body = request("GET", CONFIGURATION_XML)
    assert status == 200, f"XML descriptor: status {status}"
    assert b"<password>" not in body and b"<managerPassword>" not in body, "XML descriptor exports a password"
    return ET.fromstring(body)


def section(root, container, element, identity):
    """The entries of container/element by identity, as child name to element."""
    out = {}
    for parent in root:
        if local(parent.tag) != container:
            continue
        for entry in parent:
            if local(entry.tag) == element:
                fields = {local(child.tag): child for child in entry}
                out[fields[identity].text or ""] = fields
    return out


def text(fields, name):
    return fields[name].text or "" if name in fields else None


def env(name, default=None):
    value = os.environ.get("TF_VAR_" + name)
    return default if value is None else value


def flag(name, default):
    return env(name, default)


def expect(label, got, want):
    assert got == want, f"{label}: read back {got!r}, want {want!r}"


def record(step, scenario):
    ledger = os.environ["P12_LEDGER"]
    with open(ledger, "a", encoding="utf-8") as out:
        for resource in SCENARIOS[scenario]:
            out.write(json.dumps({"resource": resource, "step": step}) + "\n")


def fixture_secrets(scenario):
    for name in SECRET_VARIABLES.get(scenario, []):
        value = os.environ.get(name)
        assert not value or MARKER in value, f"{name} lacks the {MARKER!r} marker the secret scan looks for"


def check_proxy(xml):
    key = env("proxy_key")
    proxies = section(xml, "proxies", "proxy", "key")
    assert key in proxies, f"proxy {key} missing from the XML descriptor"
    p = proxies[key]
    expect("proxy host", text(p, "host"), env("proxy_host"))
    expect("proxy port", text(p, "port"), env("proxy_port"))
    expect("proxy username", text(p, "username") or None, env("proxy_username"))
    expect("proxy platformDefault", text(p, "platformDefault"), flag("proxy_platform_default", "false"))
    for field, variable in [("redirectedToHosts", "proxy_redirect_to_hosts"), ("services", "proxy_services")]:
        got = {item for item in (text(p, field) or "").split(",") if item}
        expect("proxy " + field, got, set(json.loads(env(variable, "[]"))))
    assert "password" not in p, "the XML descriptor exports the proxy password"


def check_property_set(xml):
    name = env("property_set_name")
    sets = section(xml, "propertySets", "propertySet", "name")
    assert name in sets, f"property set {name} missing from the XML descriptor"
    s = sets[name]
    expect("property set visible", text(s, "visible"), flag("property_set_visible", "true"))
    got = {}
    for prop in s["properties"]:
        fields = {local(child.tag): child for child in prop}
        values = {}
        for value in fields["predefinedValues"]:
            v = {local(child.tag): child.text or "" for child in value}
            values[v["value"]] = v.get("defaultValue") == "true"
        got[fields["name"].text] = {
            "closed_predefined_values": text(fields, "closedPredefinedValues") == "true",
            "multiple_choice": text(fields, "multipleChoice") == "true",
            "predefined_values": values,
        }
    want = {
        prop: {"closed_predefined_values": spec.get("closed_predefined_values", False),
               "multiple_choice": spec.get("multiple_choice", False),
               "predefined_values": spec.get("predefined_values", {})}
        for prop, spec in json.loads(env("properties")).items()
    }
    expect("property set properties", got, want)


def check_backup(xml):
    key = env("backup_key")
    backups = section(xml, "backups", "backup", "key")
    assert key in backups, f"backup {key} missing from the XML descriptor"
    b = backups[key]
    for field, variable, default in [
        ("enabled", "backup_enabled", "true"), ("cronExp", "backup_cron_exp", None),
        ("retentionPeriodHours", "backup_retention_period_hours", "168"),
        ("createArchive", "backup_create_archive", "false"),
        ("sendMailOnError", "backup_send_mail_on_error", "true"),
        ("excludeNewRepositories", "backup_exclude_new_repositories", "false"),
        ("precalculate", "backup_verify_disk_space", "false"),
        ("exportMissionControl", "backup_export_mission_control", "false"),
    ]:
        expect("backup " + field, text(b, field), env(variable, default))
    excluded = [ref.text for ref in b["excludedRepositories"]]
    expect("backup excludedRepositories", excluded, json.loads(env("backup_excluded_repositories", "[]")))


def check_mail(xml):
    mail = [entry for entry in xml if local(entry.tag) == "mailServer"]
    assert len(mail) == 1, "mail server settings missing from the XML descriptor"
    m = {local(child.tag): child for child in mail[0]}
    for field, variable, default in [
        ("enabled", "mail_enabled", "true"), ("host", "mail_host", None), ("port", "mail_port", "25"),
        ("username", "mail_username", None), ("from", "mail_from", None),
        ("subjectPrefix", "mail_subject_prefix", "[artifactweave]"),
        ("artifactoryUrl", "mail_artifactory_url", None),
        ("tls", "mail_use_tls", "false"), ("ssl", "mail_use_ssl", "false"),
    ]:
        expect("mail " + field, text(m, field) or None, env(variable, default))
    assert "password" not in m, "the XML descriptor exports the mail password"


def check_layout(xml):
    name = env("layout_name")
    layouts = section(xml, "repoLayouts", "repoLayout", "name")
    assert name in layouts, f"layout {name} missing from the XML descriptor"
    lay = layouts[name]
    descriptor_pattern = env("layout_descriptor_path_pattern")
    expect("layout artifactPathPattern", text(lay, "artifactPathPattern"), env("layout_artifact_path_pattern"))
    expect("layout distinctiveDescriptorPathPattern", text(lay, "distinctiveDescriptorPathPattern"),
           "true" if descriptor_pattern else "false")
    expect("layout descriptorPathPattern", text(lay, "descriptorPathPattern"), descriptor_pattern or "")
    expect("layout folderIntegrationRevisionRegExp", text(lay, "folderIntegrationRevisionRegExp"),
           env("layout_folder_integration_revision_regexp"))
    expect("layout fileIntegrationRevisionRegExp", text(lay, "fileIntegrationRevisionRegExp"),
           env("layout_file_integration_revision_regexp"))
    # The effect: the version search reads the repository's files through the
    # stored pattern, so it finds exactly the file this revision's pattern
    # describes. A layout stored but not obeyed reads the other one.
    repository = get_json("/artifactory/api/repositories/" + LAYOUT_REPOSITORY)
    expect("effect repository repoLayoutRef", repository.get("repoLayoutRef"), name)
    query = urlencode({"g": "acme", "a": "tool", "repos": LAYOUT_REPOSITORY})
    found = sorted(result["version"] for result in get_json("/artifactory/api/search/versions?" + query)["results"])
    expect("layout version search", found, [os.environ["P12_LAYOUT_VERSION"]])


def layout_effect(action):
    path = "/artifactory/api/repositories/" + LAYOUT_REPOSITORY
    if action == "delete":
        status, _ = request("DELETE", path)
        assert status == 200, f"delete {LAYOUT_REPOSITORY}: status {status}"
        return
    body = json.dumps({"rclass": "local", "packageType": "generic", "repoLayoutRef": env("layout_name")})
    status, _ = request("PUT", path, body.encode(), "application/json")
    assert status == 200, f"create {LAYOUT_REPOSITORY}: status {status}"
    for version, file in LAYOUT_FILES.items():
        status, _ = request("PUT", f"/artifactory/{LAYOUT_REPOSITORY}/{file}", version.encode(), "application/octet-stream")
        assert status == 201, f"deploy {file}: status {status}"
    print(f"P12 layout effect: {LAYOUT_REPOSITORY} on {env('layout_name')} with {len(LAYOUT_FILES)} files")


def check_reverse_proxy():
    provider = env("server_provider")
    https = flag("use_https", "false") == "true"
    got = get_json(WEB_SERVER)
    want = {
        "key": provider.lower(), "webServerType": provider, "serverName": env("public_server_name"),
        "artifactoryServerName": env("internal_hostname", "localhost"),
        "dockerReverseProxyMethod": env("docker_reverse_proxy_method", "SUBDOMAIN"),
        "useHttp": True, "useHttps": https, "httpPort": int(env("http_port", "80")),
        "httpsPort": int(env("https_port", "443")), "sslPort": int(env("https_port", "443")),
        "sslKey": env("ssl_key_path", ""), "sslCertificate": env("ssl_certificate_path", ""),
    }
    expect("webServer", {name: got.get(name) for name in want}, want)
    proxies = section(descriptor(), "reverseProxies", "reverseProxy", "key")
    expect("XML reverse proxies", sorted(proxies), [provider.lower()])
    x = proxies[provider.lower()]
    expect("XML reverse proxy", {name: text(x, name) for name in
                                 ["webServerType", "serverName", "dockerReverseProxyMethod", "useHttps", "httpPort", "sslPort"]},
           {"webServerType": provider.lower(), "serverName": want["serverName"],
            "dockerReverseProxyMethod": METHODS[want["dockerReverseProxyMethod"]], "useHttps": str(https).lower(),
            "httpPort": str(want["httpPort"]), "sslPort": str(want["sslPort"])})
    # The effect: the nginx snippet is rendered from the stored settings, and
    # only for an NGINX proxy.
    status, body = request("GET", SNIPPET)
    if provider != "NGINX":
        assert status == 400 and b"not of the NGINX type" in body, f"snippet for {provider}: status {status}"
        return
    assert status == 200, f"nginx snippet: status {status}"
    snippet = body.decode()
    for line in [f"    listen {want['httpPort']};\n",
                 f"        proxy_pass          http://{want['artifactoryServerName']}:8081;\n"]:
        assert line in snippet, f"nginx snippet lacks {line.strip()!r}"
    if want["dockerReverseProxyMethod"] == "REPOPATHPREFIX":
        assert f"    server_name {want['serverName']};\n" in snippet and "rewrite" not in snippet, \
            "nginx snippet does not follow the repository path method"


def ldap_expected():
    search = json.loads(env("search", "null") or "null")
    return {
        "key": env("connection_key"), "enabled": False, "ldap_url": env("ldap_url"),
        "user_dn_pattern": env("user_dn_pattern", "uid={0},ou=people"),
        "email_attribute": env("email_attribute", "mail"),
        "auto_create_user": flag("auto_create_user", "true") == "true",
        "allow_user_to_access_profile": flag("allow_user_to_access_profile", "false") == "true",
        "paging_support_enabled": flag("paging_support_enabled", "true") == "true",
        "ldap_poisoning_protection": True,
        "search": {
            "search_filter": search["filter"] if search else "",
            "search_base": search["base"] if search else "",
            "search_sub_tree": search["sub_tree"] if search else True,
            "manager_dn": search["manager_dn"] if search else "",
        },
    }


def check_ldap():
    want = ldap_expected()
    got = get_json(LDAP_SETTINGS + quote(want["key"], safe=""))
    assert not got.get("search", {}).get("manager_password"), "the Access API returned the manager password"
    got_search = {name: got.get("search", {}).get(name) for name in want["search"]}
    got = {name: got.get(name) for name in want if name != "search"}
    got["search"] = got_search
    expect("LDAP connection", got, want)


def check(scenario, label):
    fixture_secrets(scenario)
    if scenario == CONFIGURATION:
        xml = descriptor()
        for verify in (check_proxy, check_property_set, check_backup, check_mail, check_layout):
            verify(xml)
    elif scenario == REVERSE_PROXY:
        check_reverse_proxy()
    elif scenario == LDAP:
        check_ldap()
    else:
        raise SystemExit(f"{scenario} is not a P12 scenario")
    record("verify-" + label, scenario)
    print(f"P12 {scenario.rsplit('/', 1)[1]} {label}: {', '.join(SCENARIOS[scenario])} read back with effects PASS")


def deleted(scenario):
    if scenario == CONFIGURATION:
        xml = descriptor()
        for container, element, identity, variable in [
            ("proxies", "proxy", "key", "proxy_key"), ("propertySets", "propertySet", "name", "property_set_name"),
            ("backups", "backup", "key", "backup_key"), ("repoLayouts", "repoLayout", "name", "layout_name"),
        ]:
            assert env(variable) not in section(xml, container, element, identity), f"{env(variable)} survived destroy"
        assert not [entry for entry in xml if local(entry.tag) == "mailServer"], "mail server settings survived destroy"
        status, _ = request("GET", "/artifactory/api/repositories/" + LAYOUT_REPOSITORY)
        assert status == 400 or status == 404, f"{LAYOUT_REPOSITORY} survived: status {status}"
    elif scenario == REVERSE_PROXY:
        # The provider's delete sends nothing ("Reverse proxy cannot be
        # deleted"), so the last revision must still be served, unchanged.
        check_reverse_proxy()
    elif scenario == LDAP:
        status, _ = request("GET", LDAP_SETTINGS + quote(env("connection_key"), safe=""))
        assert status == 404, f"LDAP connection after destroy: status {status}"
    else:
        raise SystemExit(f"{scenario} is not a P12 scenario")
    record("deleted", scenario)
    print(f"P12 {scenario.rsplit('/', 1)[1]} destroy: {', '.join(SCENARIOS[scenario])} absent or at their documented state PASS")


def scan(work, coverage):
    """Count the files scanned; fail on any holding the fixture secret marker."""
    paths = [work / "server.log", work / "forwarder.log", *work.glob("*-proof.json")]
    paths += [p for p in (work / "data").rglob("*") if p.is_file()]
    paths += [p for p in coverage.rglob("*") if p.is_file()]
    assert (work / "data").is_dir() and (work / "server.log").is_file(), "the run left no persistent data or log to scan"
    leaked = [str(p) for p in paths if p.is_file() and MARKER.encode() in p.read_bytes()]
    assert not leaked, f"plaintext fixture secrets in {leaked}"
    return sum(1 for p in paths if p.is_file())


def complete(root, mirror, work, coverage, scenarios):
    ran = [scenario for scenario in scenarios if scenario in SCENARIOS]
    if not ran:
        return
    ledger = Path(os.environ["P12_LEDGER"])
    recorded = {}
    for line in ledger.read_text().splitlines() if ledger.exists() else []:
        entry = json.loads(line)
        recorded.setdefault(entry["resource"], []).append(entry["step"])
    missing = []
    for scenario in ran:
        for resource in SCENARIOS[scenario]:
            for step in STEPS + SECRET_STEPS.get(scenario, []):
                if step not in recorded.get(resource, []):
                    missing.append(f"{resource} {step}")
    assert not missing, "P12 qualification steps missing: " + ", ".join(missing)
    files = scan(work, coverage)
    providers = {}
    for zip_path in sorted(mirror.rglob("*.zip")):
        providers[zip_path.name] = sha256(zip_path)
    evidence = {
        "application_commit": os.environ["TERRAFORM_APPLICATION_COMMIT"],
        "terraform_version": os.environ["TERRAFORM_CLI_VERSION"],
        "driver": {name: sha256(root / "test/terraform" / name) for name in ["run.sh", "check-p12.py"]},
        "provider_zips": providers,
        "scenarios": {
            scenario: {p.name: sha256(p) for p in sorted((root / scenario).iterdir())
                       if p.suffix == ".tf" or p.name == ".terraform.lock.hcl"}
            for scenario in ran
        },
        "resources": {resource: recorded[resource] for scenario in ran for resource in SCENARIOS[scenario]},
        "secret_scan": {"files": files, "marker_hits": 0},
    }
    out = coverage / "p12-provider-evidence.json"
    out.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n")
    out.chmod(0o600)
    qualified = sorted(evidence["resources"])
    print(f"P12 provider qualification: {len(qualified)} resources, every step, {files} files scanned clean PASS")


if __name__ == "__main__":
    args = sys.argv[1:]
    if len(args) == 2 and args[0] == "--inventory":
        inventory(Path(args[1]))
    elif len(args) == 3 and args[0] == "--step":
        if args[1] in SCENARIOS:
            record(args[2], args[1])
    elif len(args) == 3 and args[0] == "--check":
        check(args[1], args[2])
    elif len(args) == 2 and args[0] == "--deleted":
        deleted(args[1])
    elif len(args) == 2 and args[0] == "--layout-effect" and args[1] in ("create", "delete"):
        layout_effect(args[1])
    elif len(args) >= 5 and args[0] == "--complete":
        complete(Path(args[1]), Path(args[2]), Path(args[3]), Path(args[4]), args[5:])
    else:
        raise SystemExit("expected --inventory ROOT, --step SCENARIO STEP, --check SCENARIO LABEL, "
                         "--deleted SCENARIO, --layout-effect create|delete, "
                         "or --complete ROOT MIRROR WORK COVERAGE SCENARIO...")
