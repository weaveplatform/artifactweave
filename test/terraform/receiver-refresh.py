#!/usr/bin/env python3
"""Qualify provider 12.11.14 refresh of a live receiver federation.

Run from the peer scenario via ARTIFACTWEAVE_RECEIVER_PROVIDER_CHECK pointing
at this file. ARTIFACTWEAVE_RECEIVER_PROVIDER_MIRROR supplies the already
verified filesystem mirror used by test/terraform/run.sh. The scenario passes
its isolated URL, key and administrator password in the environment. No
credential or Terraform state is retained in the evidence file.
"""
import json
from datetime import datetime, timezone
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request
import urllib.parse
import base64


def main():
    origin = os.environ["AW_RECEIVER_URL"]
    key = os.environ["AW_RECEIVER_KEY"]
    password = os.environ["AW_RECEIVER_PASSWORD"]
    authorization = "Basic " + base64.b64encode(("admin:" + password).encode()).decode()

    def request(path, method="GET", form=None, body=None):
        headers = {"Authorization": authorization}
        data = None
        if form is not None:
            headers["Content-Type"] = "application/x-www-form-urlencoded"
            data = urllib.parse.urlencode(form).encode()
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        req = urllib.request.Request(origin + path, method=method, data=data, headers=headers)
        with urllib.request.urlopen(req, timeout=30) as response:
            raw = response.read()
            return json.loads(raw) if raw and "application/json" in response.headers.get("Content-Type", "") else None

    access_token = request("/artifactory/api/security/token", "POST",
                           {"scope": "applied-permissions/admin", "expires_in": "3600"})["access_token"]
    config = request("/artifactory/api/repositories/" + key)
    members = config["members"]
    assert any(member.get("mode") == "receiver" for member in members), "no receiver to qualify"
    assert any(member.get("mode", "bidirectional") == "bidirectional" for member in members)
    mirror = Path(os.environ["ARTIFACTWEAVE_RECEIVER_PROVIDER_MIRROR"]).resolve()
    evidence = Path(os.environ["ARTIFACTWEAVE_RECEIVER_PROVIDER_EVIDENCE"])
    proof_key = "tf-receiver-refresh"
    members = [dict(m, url=m["url"].rsplit("/", 1)[0] + "/" + proof_key) for m in members]
    with tempfile.TemporaryDirectory(prefix="aw-receiver-provider-") as directory:
        work = Path(directory)
        terraform_config = {
            "terraform": {"required_providers": {"artifactory": {
                "source": "jfrog/artifactory", "version": "12.11.14"}}},
            "variable": {"access_token": {"type": "string", "sensitive": True}},
            "provider": {"artifactory": {"url": origin, "access_token": "${var.access_token}"}},
            "resource": {"artifactory_federated_generic_repository": {"receiver": {
                "key": proof_key, "cleanup_on_delete": False,
                "member": [{"url": origin + "/artifactory/" + proof_key, "enabled": True}]}}},
        }
        (work / "main.tf.json").write_text(json.dumps(terraform_config))
        (work / "terraform.rc").write_text('provider_installation { filesystem_mirror { path = '
                                              + json.dumps(str(mirror)) + ' } }\n')
        env = dict(os.environ, TF_CLI_CONFIG_FILE=str(work / "terraform.rc"),
                   TF_VAR_access_token=access_token, TF_IN_AUTOMATION="1", TF_INPUT="0")
        results = []

        def run(args):
            result = subprocess.run(["terraform", *args], cwd=work, env=env,
                                    text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    timeout=180, check=False)
            results.append({"command": "terraform " + " ".join(args), "exit_code": result.returncode})
            if result.returncode:
                print(result.stdout.replace(access_token, "[redacted]").replace(password, "[redacted]"))
                raise SystemExit(result.returncode)

        # Establish the provider's own defaults and state, avoiding unrelated
        # import-only default drift. The initial member is bidirectional.
        run(["init", "-no-color"])
        run(["apply", "-auto-approve", "-no-color"])
        run(["plan", "-detailed-exitcode", "-no-color"])
        request("/artifactory/api/repositories/" + proof_key, "POST", body={"members": members})
        resource = terraform_config["resource"]["artifactory_federated_generic_repository"]["receiver"]
        resource["member"] = [{"url": m["url"], "enabled": m.get("enabled", True)} for m in members]
        (work / "main.tf.json").write_text(json.dumps(terraform_config))
        run(["apply", "-refresh-only", "-auto-approve", "-no-color"])
        run(["plan", "-detailed-exitcode", "-no-color"])
        after = request("/artifactory/api/repositories/" + proof_key)
        assert any(m.get("mode") == "receiver" for m in after["members"]), "refresh lost receiver mode"
        run(["destroy", "-auto-approve", "-no-color"])
        evidence.parent.mkdir(parents=True, exist_ok=True)
        cli = json.loads(subprocess.check_output(["terraform", "version", "-json"], cwd=work, env=env, text=True))
        evidence.write_text(json.dumps({"checked_at": datetime.now(timezone.utc).isoformat(),
                                       "terraform_cli": cli["terraform_version"], "platform": cli["platform"],
                                       "metadata_driver": os.environ.get("AW_RECEIVER_DRIVER", "sqlite"),
                                       "provider": "jfrog/artifactory", "version": "12.11.14",
                                       "resource": "artifactory_federated_generic_repository",
                                       "receiver_members": sum(m.get("mode") == "receiver" for m in members),
                                       "bidirectional_members": sum(m.get("mode", "bidirectional") == "bidirectional" for m in members),
                                       "commands": results, "empty_followup_plan": True}, indent=2) + "\n")
        print("Provider 12.11.14 receiver refresh and empty bidirectional follow-up plan passed")


if __name__ == "__main__":
    main()
