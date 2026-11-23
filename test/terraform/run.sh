#!/usr/bin/env bash
# Runs Terraform scenarios against artifactweave with the pinned JFrog
# providers, installed from a filesystem mirror built from the release zips
# and their pinned SHA-256s, so no run depends on the registry.
#
#   run.sh validate [scenario...]  init from the mirror and validate each
#   run.sh apply [scenario...]     also start one -cover artifactweave, mint
#                                  an admin access token through its token
#                                  API, then apply each scenario, require an
#                                  empty plan, and destroy it; a failed
#                                  run prints the end of the server log
#
# A scenario is a directory relative to the repository root; the default is
# the repositories reference in deploy/artifactweave/terraform. The providers
# authenticate only with a Bearer access token, so apply mints one with the
# bootstrap admin's password, which the server takes because it starts with
# token.allow_basic_auth on; TERRAFORM_ACCESS_TOKEN overrides it.
set -euo pipefail
umask 077

ARTIFACTORY_VERSION=12.11.14
PLATFORM_VERSION=2.2.11
PROJECT_VERSION=1.9.9
# From each release's SHA256SUMS. A case, not an associative array, because
# macOS ships bash 3.2.
provider_sha256() {
	case $1_$2 in
	artifactory_linux_amd64) echo 52380363f451b6b2db267b3183e500148c3b8a89924350b61a6884fdf43c00c2 ;;
	artifactory_linux_arm64) echo 8f03320b8e1b06e3305869dd8f32ab58d73085caf083dbe8c516457ffb21c5df ;;
	artifactory_darwin_amd64) echo cd5308dc8c8dcf8684638cb7f8bb01dc384b56533c27ab8b5d087bbdd620b621 ;;
	artifactory_darwin_arm64) echo b2dd6b1c6ea68370ecf8b6764208053aefbbafb3d5acdc2c0cee24bb4635ea20 ;;
	platform_linux_amd64) echo dc46dd205c98acf460ad399ff725f3fdfc7c4c84d6f589721893f223f34d1ccd ;;
	platform_linux_arm64) echo 5375c863eb7d1f530dfabdde697c8452f0c09199b740a8116cb54e35858e4a37 ;;
	platform_darwin_amd64) echo 3f4c08b6a6e5b210d37074a048c56a3a03cbada451a5e5fd2b9a8257bf07f953 ;;
	platform_darwin_arm64) echo 7af6f2b15b6cea1357bdf35da3dfe4da8656e15369ad672069ad994f3418e50b ;;
	project_linux_amd64) echo 142ff8bf830be159625cb7ff5f7faa8019a70a45be6c60656f46f1eecab5bed1 ;;
	project_linux_arm64) echo 23a3659229c2b994642ea2f823dae017e101201205b0e67fa5a2c1e53f37bb9b ;;
	project_darwin_amd64) echo 1fb9ae65b8372e110a312afee0bf5a5e8d983fc57bff55d260371c87a4089238 ;;
	project_darwin_arm64) echo cf2ad3e93fdd891ab2de2705b967dba5c11c281b37c6454e858750784d596e87 ;;
	esac
}

mode=${1:-validate}
shift || true
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
python3 -I "$here/check-webhooks.py" --inventory "$root"
python3 -I "$here/check-projects.py" --inventory "$root"
python3 -I "$here/check-cleanup.py" --inventory "$root"
python3 -I "$here/check-general-security.py" --inventory "$root"
scenarios=("$@")
[ ${#scenarios[@]} -gt 0 ] || scenarios=(deploy/artifactweave/terraform/repositories)
if [ -n "${TERRAFORM_WORK:-}" ]; then
	mkdir -p "$TERRAFORM_WORK"
	work=$(mktemp -d "$TERRAFORM_WORK/run.XXXXXX")
else
	work=$(mktemp -d)
	owned=1
fi
# Copies, configuration, state and the startup log all belong to this run,
# including when the caller retains several runs in TERRAFORM_WORK.
work=$(cd "$work" && pwd)
echo "Terraform provider workspace: $work"
# Python effect checkers and native providers also contact the fixture.
# Preserve proxy routing for release downloads, but every fixture client
# must bypass it for the private loopback origin.
fixture_no_proxy="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}${no_proxy:+,$no_proxy}"
export NO_PROXY="$fixture_no_proxy" no_proxy="$fixture_no_proxy"
server=
forwarder=
server_ready=
require_server() {
	if ! kill -0 "$server" 2>/dev/null; then
		local status=0
		wait "$server" || status=$?
		server=
		echo "Terraform fixture server exited (status $status)" >&2
		exit 1
	fi
	if [ -n "$forwarder" ] && ! kill -0 "$forwarder" 2>/dev/null; then
		wait "$forwarder" || true
		forwarder=
		echo "Terraform fixture connection owner proof failed" >&2
		exit 1
	fi
	if [ -f "$work/forwarder.failed" ]; then
		echo "Terraform fixture frontend is quarantined after connection ownership failure" >&2
		exit 1
	fi
}
# Direct fixture HTTP must neither use an ambient proxy nor proceed after
# the owned server has stopped. Mirror downloads run before server is set.
curl() {
	if [ -n "$server_ready" ]; then
		require_server
		local status=0
		command curl --noproxy '*' "$@" || status=$?
		require_server
		return "$status"
	else
		command curl "$@"
	fi
}
cleanup() {
	rc=$?
	if [ -n "$forwarder" ]; then
		kill -TERM "$forwarder" 2>/dev/null || true
		wait "$forwarder" 2>/dev/null || true
	fi
	if [ -f "$work/forwarder.failed" ]; then rc=1; fi
	if [ -n "$server" ]; then
		kill -TERM "$server" 2>/dev/null || true
		wait "$server" 2>/dev/null || true
	fi
	# Terraform reports only the status of a failed call. The error behind a
	# 5xx is in the server log, which goes when the work directory does.
	if [ "$rc" -ne 0 ] && [ -s "$work/server.log" ]; then
		echo "run.sh: exit $rc; the last 300 lines of the server log:" >&2
		tail -n 300 "$work/server.log" >&2
	fi
	if [ "$rc" -ne 0 ] && [ -s "$work/forwarder.log" ]; then
		tail -n 30 "$work/forwarder.log" >&2
	fi
	if [ -n "${owned:-}" ]; then
		if [ "$rc" -eq 0 ]; then
			rm -rf "$work"
		else
			echo "run.sh: retained private failure evidence in $work" >&2
		fi
	fi
	exit "$rc"
}
trap cleanup EXIT

platform="$(go env GOOS)_$(go env GOARCH)"

# mirror_provider fetches one provider's zip into the packed mirror layout,
# <mirror>/<host>/<namespace>/<type>/<zip>, checking its pinned SHA-256.
#
# The zip has one source (#1341). JFrog publishes its providers only as
# GitHub release assets, and the download APIs of registry.terraform.io and
# registry.opentofu.org both point at that same asset, so no second host
# serves the bytes. When GitHub is down the job fails, which the maintainer
# accepts (#1322).
mirror_provider() {
	local name=$1 version=$2 sum dir zip
	sum=$(provider_sha256 "$name" "$platform")
	[ -n "$sum" ] || { echo "no pinned $name provider checksum for $platform" >&2; exit 1; }
	dir="$work/mirror/registry.terraform.io/jfrog/$name"
	zip="terraform-provider-${name}_${version}_${platform}.zip"
	mkdir -p "$dir"
	if [ ! -f "$dir/$zip" ]; then
		curl -fsSL -o "$dir/$zip.part" \
			"https://github.com/jfrog/terraform-provider-${name}/releases/download/v${version}/${zip}"
		echo "$sum  $dir/$zip.part" | (sha256sum -c - 2>/dev/null || shasum -a 256 -c -) >/dev/null
		mv "$dir/$zip.part" "$dir/$zip"
	fi
}
mirror=${TERRAFORM_PROVIDER_MIRROR:-$work/mirror}
if [ -z "${TERRAFORM_PROVIDER_MIRROR:-}" ]; then
	mirror_provider artifactory "$ARTIFACTORY_VERSION"
	mirror_provider platform "$PLATFORM_VERSION"
	mirror_provider project "$PROJECT_VERSION"
fi
cat >"$work/terraformrc" <<RC
provider_installation {
  filesystem_mirror {
    path    = "$mirror"
    include = ["registry.terraform.io/jfrog/artifactory", "registry.terraform.io/jfrog/platform", "registry.terraform.io/jfrog/project"]
  }
  direct {
    exclude = ["registry.terraform.io/jfrog/artifactory", "registry.terraform.io/jfrog/platform", "registry.terraform.io/jfrog/project"]
  }
}
RC
export TF_CLI_CONFIG_FILE="$work/terraformrc" TF_IN_AUTOMATION=1 TF_INPUT=0

# Each scenario runs on a copy in $work so its local state, kept beside
# main.tf, never lands in the source tree, where a failed run's state would
# point the next run at resources on a server that no longer exists.
dir=
tf() {
	if [ -n "$server_ready" ]; then require_server; fi
	local status=0
	TF_DATA_DIR="$dir/.terraform" terraform -chdir="$dir" "$@" || status=$?
	if [ -n "$server_ready" ]; then require_server; fi
	return "$status"
}

for scenario in "${scenarios[@]}"; do
	dir="$work/scenarios/$scenario"
	mkdir -p "$dir"
	cp "$root/$scenario"/*.tf "$root/$scenario/.terraform.lock.hcl" "$dir/"
	tf init -backend=false -lockfile=readonly
	tf validate
done
[ "$mode" = apply ] || exit 0

# A -cover binary so the run's coverage joins the merged report. The flags
# match the acceptance build: unit tests run with -race, which forces atomic
# counters, and covdata merge refuses to mix counter modes.
module=$(cd "$root" && go list -m)
TERRAFORM_APPLICATION_COMMIT=$(git -C "$root" rev-parse HEAD)
export TERRAFORM_APPLICATION_COMMIT
echo "Terraform provider conformance: application $TERRAFORM_APPLICATION_COMMIT"
terraform version
TERRAFORM_CLI_VERSION=$(terraform version -json | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["terraform_version"])')
export TERRAFORM_CLI_VERSION
(cd "$root" && go build -cover -covermode=atomic -coverpkg="${TERRAFORM_COVERPKG:-$module/...}" -o "$work/artifactweave" ./cmd/artifactweave)
port=${TERRAFORM_PORT:-18081}
url="http://127.0.0.1:$port"
password=terraform-admin
mkdir -p "${GOCOVERDIR:-$work/cover}"
# Terraform deletes up to ten resources at once (its default parallelism).
# On SQLite each request-budget check is a write that waits behind those
# deletes for the one writer, and past api.budget_timeout (500ms by
# default) admission fails closed with a 503 that the provider does not
# retry (#1147). This job tests the providers against the API, not that
# default, so the server allows the maximum.
#
# The provider posts a usage report (POST /api/system/usage) for every
# resource it creates, reads, updates or deletes. Those are ordinary
# requests (decision 0043, "Classes moved since"), so the default budgets
# hold the virtual-repositories scenario's 102 resources (#1597).
python3 -I "$here/fixture-server.py" launch "$work" "$port" "${GOCOVERDIR:-$work/cover}" \
	>"$work/server.log" 2>&1 &
server=$!
startup_deadline=$((SECONDS + 10))
while [ "$SECONDS" -lt "$startup_deadline" ]; do
	require_server
	if backend=$(python3 -I "$here/fixture-server.py" address "$work/server.log"); then
		require_server
		if [ -z "$forwarder" ]; then
			python3 -I "$here/fixture-server.py" forward "$server" "$backend" "$port" "$work/forwarder.ready" \
				>"$work/forwarder.log" 2>&1 &
			forwarder=$!
		fi
		if [ -f "$work/forwarder.ready" ] && command curl --noproxy '*' --max-time 2 -fs "$url/artifactory/api/v1/system/readiness" >/dev/null; then
			require_server
			server_ready=1
			break
		fi
	fi
	sleep 0.1
done
[ -n "$server_ready" ] || { echo "Terraform fixture server did not become ready with owned listener evidence" >&2; exit 1; }
require_server

token=${TERRAFORM_ACCESS_TOKEN:-}
if [ -z "$token" ]; then
	token=$(curl -fsS -u "admin:$password" -H 'Content-Type: application/json' \
		-d '{"scope":"applied-permissions/admin","expires_in":3600}' \
		"$url/access/api/v1/tokens" |
		python3 -I -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
fi
export TF_VAR_url=$url TF_VAR_access_token=$token

# Each reference takes its names as inputs; the run passes the fixture's,
# exported so the plan and destroy that follow see the same values.

# The repositories scenario: the provider cannot deploy content, so the file
# its properties go on is deployed between a first apply without them and
# the full one. A fresh GPG key pair per run comes from tools/gpgkey rather
# than gpg, which is not on every host, and is passed through the
# environment, as the identity scenario's RSA pair is.
apply_repositories() {
	export TF_VAR_repository_key=tf-generic-local TF_VAR_item_path=fixture.txt \
		TF_VAR_gpg_keypair_name=tf-terraform-providers-gpg
	(cd "$root" && GOWORK=off go run ./tools/gpgkey "$work/gpg")
	TF_VAR_gpg_private_key=$(cat "$work/gpg/private.asc")
	TF_VAR_gpg_public_key=$(cat "$work/gpg/public.asc")
	export TF_VAR_gpg_private_key TF_VAR_gpg_public_key
	tf apply -auto-approve -var with_properties=false
	curl -fsS -u "admin:$password" -X PUT --data-binary 'fixture' \
		"$url/artifactory/$TF_VAR_repository_key/$TF_VAR_item_path" >/dev/null
	# Provider 12.11.14 cannot set primary_keypair_ref on a Terraform provider
	# repository, so the pair is associated through the repositories API, as
	# the reference's README says. The apply and the empty plan that follow
	# show the provider leaves the association alone.
	local providers
	providers=$(tf output -json package_repository_keys |
		python3 -I -c 'import json, sys; print(json.load(sys.stdin)["terraform_provider"])')
	curl -fsS -u "admin:$password" -X POST -H 'Content-Type: application/json' \
		-d "{\"primaryKeyPairRef\":\"$TF_VAR_gpg_keypair_name\"}" \
		"$url/artifactory/api/repositories/$providers" >/dev/null
	tf apply -auto-approve
	curl -fsS -u "admin:$password" "$url/artifactory/api/repositories/$providers" | python3 -I -c '
import json, sys
ref = json.load(sys.stdin).get("primaryKeyPairRef")
sys.exit(None if ref == sys.argv[1] else f"primaryKeyPairRef is {ref!r}, want {sys.argv[1]!r}")
' "$TF_VAR_gpg_keypair_name"
	# The repositories data source must list the generic repository with
	# the package type spelled as Artifactory lists it, as the provider's own
	# acceptance test expects "Alpine" for alpine.
	tf output -json local_generic_repositories | python3 -I -c '
import json, sys
listed = json.load(sys.stdin).get(sys.argv[1])
sys.exit(None if listed == "Generic" else f"{sys.argv[1]} is listed as {listed!r}, want Generic")
' "$TF_VAR_repository_key"
}

# rsa_keypair writes a fresh 2048-bit RSA pair to $1.pem and $1.pub. The
# provider accepts only a PKCS#1 private key ("RSA PRIVATE KEY"), which
# OpenSSL 3 writes only with -traditional and LibreSSL writes by default.
rsa_keypair() {
	openssl genrsa -out "$1.pem" 2048 2>/dev/null
	if ! grep -q 'BEGIN RSA PRIVATE KEY' "$1.pem"; then
		openssl rsa -in "$1.pem" -traditional -out "$1.pem" 2>/dev/null
	fi
	openssl rsa -in "$1.pem" -pubout -out "$1.pub" 2>/dev/null
}

# The identity scenario: a fresh RSA key pair per run, passed through the
# environment so the private key never appears in a process listing.
apply_identity() {
	rsa_keypair "$work/keypair"
	TF_VAR_keypair_private_key=$(cat "$work/keypair.pem")
	TF_VAR_keypair_public_key=$(cat "$work/keypair.pub")
	TF_VAR_user_password=Terraform-user-1
	export TF_VAR_keypair_private_key TF_VAR_keypair_public_key TF_VAR_user_password
	export TF_VAR_repository_key=tf-team-local TF_VAR_developers_group=tf-developers \
		TF_VAR_auditors_group=tf-auditors TF_VAR_developer_user=tf-developer \
		TF_VAR_ci_user=tf-ci TF_VAR_permission_name=tf-team TF_VAR_keypair_name=tf-signing
	tf apply -auto-approve
	# The permission written through Access v2 must read back through the
	# legacy v2 permission target API with its repository. The state is read
	# whole before the grep: grep -q exits at its first match, Terraform then
	# dies of SIGPIPE on its next write, and pipefail fails the check although
	# the repository was there.
	local team
	team=$(tf state show data.artifactory_permission_target.team)
	grep -q "\"$TF_VAR_repository_key\"" <<<"$team" ||
		{ echo "the legacy permission target API does not report $TF_VAR_repository_key" >&2; exit 1; }
}

# The oidc scenario: the providers are never contacted, so any organisation
# and issuer will do.
apply_oidc() {
	export TF_VAR_github_organization=octo-org TF_VAR_ci_project_path='octo-org/**' \
		TF_VAR_github_provider_name=tf-github TF_VAR_ci_provider_name=tf-ci
	tf apply -auto-approve
}

# The cdn-repositories scenario: the redirect flags must read back as
# written, so the plan after the apply is empty.
apply_cdn_repositories() {
	export TF_VAR_cdn_repository_key=tf-generic-cdn TF_VAR_direct_repository_key=tf-generic-direct
	tf apply -auto-approve
}

# Remote management uses only this fixture's loopback server. No content is
# fetched: the native adapter scenarios own protocol and download evidence.
apply_remote_repositories() {
	export TF_VAR_repository_prefix=tf-remote TF_VAR_revision=1 \
		TF_VAR_proxy_key=tf-remote-proxy \
		TF_VAR_upstream_password=Terraform-upstream-1 \
		TF_VAR_header_secret=Terraform-header-1 \
		TF_VAR_proxy_password=Terraform-proxy-1
	TF_VAR_upstream_urls=$(python3 -I -c '
import json,sys
formats = "generic docker oci helmoci npm go maven gradle ivy sbt pypi nuget helm terraform".split()
print(json.dumps({name: sys.argv[1] + "/controlled-upstream/" + name for name in formats}))
' "$url")
	export TF_VAR_upstream_urls
	python3 -I "$here/check-remotes.py" --seed-proxy "$port"
	tf apply -auto-approve
	tf plan -detailed-exitcode
	python3 -I "$here/check-remotes.py" "$work/data" "$dir/terraform.tfstate"
	# Both a write-only password rotation and ordinary configuration changes
	# must survive refresh. Never print sensitive Terraform state to the log.
	export TF_VAR_revision=2 TF_VAR_upstream_password=Terraform-upstream-2 \
		TF_VAR_header_secret=Terraform-header-2 TF_VAR_proxy_password=Terraform-proxy-2
	python3 -I "$here/check-remotes.py" --seed-proxy "$port"
	tf apply -auto-approve
	python3 -I "$here/check-remotes.py" "$work/data" "$dir/terraform.tfstate"
}

# check_virtual_members reads every virtual of the virtual-repositories
# scenario through the repositories API and requires its members in the
# order the revision gave them, with the local member as its deployment
# repository. The empty plan already shows the provider reads back what it
# wrote; this reads the API itself, so the order and the deployment
# repository do not rest on how the provider compares lists, and the count
# catches a virtual dropped from the scenario.
check_virtual_members() {
	tf output -json virtual_repository_keys >"$work/virtual-keys.json"
	tf output -json virtual_members >"$work/virtual-members.json"
	TERRAFORM_TOKEN=$token python3 -I - "$url" "$work/virtual-keys.json" "$work/virtual-members.json" <<'PY'
import json, os, sys, urllib.request
url, keys, members = sys.argv[1], json.load(open(sys.argv[2])), json.load(open(sys.argv[3]))
token = os.environ["TERRAFORM_TOKEN"]
bad = []
for kind, key in sorted(keys.items()):
    req = urllib.request.Request(f"{url}/artifactory/api/repositories/{key}",
                                 headers={"Authorization": f"Bearer {token}"})
    got = json.load(urllib.request.urlopen(req))
    want = members[kind]
    local = next(m for m in want if m.endswith("-local"))
    if got.get("repositories") != want or got.get("defaultDeploymentRepo") != local:
        bad.append(f"{key}: repositories {got.get('repositories')} deploying to "
                   f"{got.get('defaultDeploymentRepo')!r}, want {want} deploying to {local!r}")
if len(keys) != 33:
    bad.append(f"{len(keys)} virtual repositories, want 33")
sys.exit("\n".join(bad) or None)
PY
}

# The virtual-repositories scenario: every provider virtual resource over a
# local and a remote member. Remote URLs point at this fixture's loopback
# server and nothing is fetched. The GPG pairs come from tools/gpgkey and
# the RSA pair from openssl, as the repositories and identity scenarios make
# theirs. Revision 2 reverses every member list and changes one field of
# each virtual, and must plan empty again before the shared plan and destroy.
apply_virtual_repositories() {
	export TF_VAR_repository_prefix=tf-virtual TF_VAR_revision=1 \
		TF_VAR_upstream_url="$url/controlled-upstream" \
		TF_VAR_gpg_primary_keypair_name=tf-virtual-gpg-primary \
		TF_VAR_gpg_secondary_keypair_name=tf-virtual-gpg-secondary \
		TF_VAR_rsa_keypair_name=tf-virtual-rsa
	(cd "$root" && GOWORK=off go run ./tools/gpgkey "$work/virtual-gpg-primary")
	(cd "$root" && GOWORK=off go run ./tools/gpgkey "$work/virtual-gpg-secondary")
	rsa_keypair "$work/virtual-rsa"
	TF_VAR_gpg_primary_private_key=$(cat "$work/virtual-gpg-primary/private.asc")
	TF_VAR_gpg_primary_public_key=$(cat "$work/virtual-gpg-primary/public.asc")
	TF_VAR_gpg_secondary_private_key=$(cat "$work/virtual-gpg-secondary/private.asc")
	TF_VAR_gpg_secondary_public_key=$(cat "$work/virtual-gpg-secondary/public.asc")
	TF_VAR_rsa_private_key=$(cat "$work/virtual-rsa.pem")
	TF_VAR_rsa_public_key=$(cat "$work/virtual-rsa.pub")
	export TF_VAR_gpg_primary_private_key TF_VAR_gpg_primary_public_key \
		TF_VAR_gpg_secondary_private_key TF_VAR_gpg_secondary_public_key \
		TF_VAR_rsa_private_key TF_VAR_rsa_public_key
	tf apply -auto-approve
	tf plan -detailed-exitcode
	check_virtual_members
	export TF_VAR_revision=2
	tf apply -auto-approve
	tf plan -detailed-exitcode
	check_virtual_members
}

# Administration only: these receivers are stored, never contacted. Both
# revisions must refresh cleanly without reading the write-only secret back.
apply_webhooks() {
	export TF_VAR_receiver_url="$url/controlled-webhook-receiver" \
		TF_VAR_revision=1 TF_VAR_handler_secret=Terraform-webhook-secret-1
	tf apply -auto-approve
	tf plan -detailed-exitcode
	tf output -json webhook_keys >"$work/webhook-keys.json"
	python3 -I "$here/check-webhooks.py" --check "$root" "$work/webhook-keys.json" "$dir/terraform.tfstate" "$work/data" "$work/server.log"
	export TF_VAR_revision=2 TF_VAR_handler_secret=Terraform-webhook-secret-2
	tf apply -auto-approve
	tf plan -detailed-exitcode
	python3 -I "$here/check-webhooks.py" --check "$root" "$work/webhook-keys.json" "$dir/terraform.tfstate" "$work/data" "$work/server.log"
	python3 -I "$here/check-webhooks.py" --controls "$work/server.log"
}

# Configuration only. Both URLs point at unused paths on this fixture server;
# no transfer or execution is part of this scenario's qualification.
apply_single_replication() {
	export TF_VAR_repository_prefix=tf-single-replication TF_VAR_revision=1 \
		TF_VAR_target_url="$url/artifactory/controlled-replication-target" \
		TF_VAR_upstream_url="$url/artifactory/controlled-replication-source" \
		TF_VAR_target_password=Terraform-replication-target-1 \
		TF_VAR_upstream_password=Terraform-replication-upstream-1 \
		TF_VAR_proxy_key=tf-single-replication-proxy
	python3 -I "$here/check-replications.py" --proxy "$port"
	tf apply -auto-approve
	tf plan -detailed-exitcode
	tf output -json repository_keys >"$work/replication-repositories.json"
	python3 -I "$here/check-replications.py" --check "$work/replication-repositories.json" "$work/replication-identity.json" "$work/data" "$work/server.log"
	export TF_VAR_revision=2 TF_VAR_target_password=Terraform-replication-target-2 \
		TF_VAR_upstream_password=Terraform-replication-upstream-2
	tf apply -auto-approve
	tf plan -detailed-exitcode
	python3 -I "$here/check-replications.py" --check "$work/replication-repositories.json" "$work/replication-identity.json" "$work/data" "$work/server.log"
}

# A real native provider proof, including current content authority and quota
# effects. Only names/digests and assertion results join retained CI coverage;
# sensitive Terraform state remains in the isolated private work directory.
apply_projects() {
	export TF_VAR_resource_prefix=tfp11 TF_VAR_revision=1 \
		TF_VAR_user_password=Terraform-project-user-1
	project_evidence="${GOCOVERDIR:-$work/cover}/project-provider-evidence.json"
	tf apply -auto-approve
	tf plan -detailed-exitcode
	tf output -json fixture >"$work/project-fixture.json"
	python3 -I "$here/check-projects.py" --check "$root" "$work/project-fixture.json" "$dir/terraform.tfstate" "$project_evidence"
	export TF_VAR_revision=2
	tf apply -auto-approve
	tf plan -detailed-exitcode
	tf output -json fixture >"$work/project-fixture.json"
	python3 -I "$here/check-projects.py" --check "$root" "$work/project-fixture.json" "$dir/terraform.tfstate" "$project_evidence"
}

apply_package_cleanup() {
	export TF_VAR_repository_prefix=tf-p11-cleanup TF_VAR_revision=1
	tf apply -auto-approve
	tf output -json fixture >"$work/cleanup-fixture.json"
	python3 -I "$here/check-cleanup.py" --prepare "$work/cleanup-fixture.json" "$work/cleanup-runs.json" "$work/server.log"
	tf plan -detailed-exitcode
	export TF_VAR_revision=2
	tf apply -auto-approve
	tf output -json fixture >"$work/cleanup-fixture.json"
	python3 -I "$here/check-cleanup.py" --check "$work/cleanup-fixture.json" "$work/cleanup-runs.json" "$work/server.log"
}

# Compare the actual server secret through its canonical store/keyring, not
# Terraform's retained Password state (the pinned provider never reads it).
configuration_proof() {
	CONFIGURATION_PROOF_MASTER_KEY=terraform-master-key-0123456789abcdef0123456789abcdef \
		CONFIGURATION_PROOF_PASSWORD=${TF_VAR_proxy_password:-} \
		"$work/configurationproof" "$work/data/artifactweave.db" "proxy/$TF_VAR_proxy_key" "$1" "$work/configuration-secret-proof.json"
	CONFIGURATION_PROOF_MASTER_KEY=terraform-master-key-0123456789abcdef0123456789abcdef \
		CONFIGURATION_PROOF_PASSWORD=${TF_VAR_mail_password:-} \
		"$work/configurationproof" "$work/data/artifactweave.db" mail_server/mailserver "$1" "$work/mail-secret-proof.json"
}

# configuration_patch sends a YAML descriptor patch as the admin; JSON is
# YAML, so the body is the JSON form of its argument.
configuration_patch() {
	CONFIGURATION_PATCH="$1" python3 -I - <<'PYTHON'
import os
from urllib.request import ProxyHandler, Request, build_opener
req = Request(os.environ["TF_VAR_url"] + "/artifactory/api/system/configuration",
              data=os.environ["CONFIGURATION_PATCH"].encode(), method="PATCH",
              headers={"Authorization": "Bearer " + os.environ["TF_VAR_access_token"],
                       "Content-Type": "application/yaml"})
with build_opener(ProxyHandler({})).open(req, timeout=10) as response:
    assert response.status == 200, "descriptor patch failed"
PYTHON
}

# A backup definition is configuration only (P36 runs backups): no provider
# step may create a backup directory beside the server's data.
no_backup_directory() {
	test -z "$(find "$work/data" -iname '*backup*' -print -quit)"
}

# The reference is consumer-facing; revisions and password-clear orchestration
# live only here. Missing XML GET fails the pinned provider apply, never skips.
apply_configuration() {
	export TF_VAR_proxy_key=tf-configuration-proxy TF_VAR_proxy_host=127.0.0.1 \
		TF_VAR_property_set_name=tf-configuration-properties \
		TF_VAR_proxy_redirect_to_hosts='["one.invalid","two.invalid"]' \
		TF_VAR_backup_key=tf-configuration-backup TF_VAR_mail_host=127.0.0.1 \
		TF_VAR_mail_artifactory_url=https://registry.invalid/artifactory/
	(cd "$root" && GOWORK=off go build -o "$work/configurationproof" ./tools/configurationproof)
	# Backup exclusions must name existing repositories; the module owns none.
	configuration_patch '{"localRepositories":{"tf-backup-skip-a":{"type":"generic"},"tf-backup-skip-b":{"type":"generic"},"tf-backup-skip-c":{"type":"generic"}}}'
	for revision in 1 2; do
		export TF_VAR_proxy_port=$((3128 + revision)) TF_VAR_proxy_username=fixture-user-$revision \
			TF_VAR_proxy_password=descriptor-private-fixture-$revision \
			TF_VAR_mail_port=$((2524 + revision)) TF_VAR_mail_username=mail-user-$revision \
			TF_VAR_mail_password=mail-private-fixture-$revision \
			TF_VAR_mail_from=sender-$revision@example.invalid TF_VAR_mail_subject_prefix="[revision $revision]"
		if [ "$revision" = 1 ]; then
			export TF_VAR_proxy_platform_default=true TF_VAR_proxy_services='[]' TF_VAR_property_set_visible=true
			export TF_VAR_properties='{"release":{"closed_predefined_values":true,"multiple_choice":true,"predefined_values":{"ready":true,"waiting":false}},"free":{}}'
			export TF_VAR_backup_cron_exp='0 0 2 ? * MON-FRI' TF_VAR_backup_enabled=true \
				TF_VAR_backup_retention_period_hours=168 TF_VAR_backup_excluded_repositories='["tf-backup-skip-a","tf-backup-skip-b"]' \
				TF_VAR_backup_create_archive=false TF_VAR_backup_send_mail_on_error=true \
				TF_VAR_mail_enabled=true TF_VAR_mail_use_tls=true TF_VAR_mail_use_ssl=false
		else
			export TF_VAR_proxy_platform_default=false TF_VAR_proxy_services='["jfrt","jfxr"]' TF_VAR_property_set_visible=false
			export TF_VAR_properties='{"release":{"closed_predefined_values":true,"multiple_choice":false,"predefined_values":{"ready":false,"waiting":true}},"free":{}}'
			# A list left out of the provider's patch keeps the stored one
			# (excludedRepositories is omitempty), so revisions change it.
			export TF_VAR_backup_cron_exp='0 30 3 * * ?' TF_VAR_backup_enabled=false \
				TF_VAR_backup_retention_period_hours=0 TF_VAR_backup_excluded_repositories='["tf-backup-skip-c"]' \
				TF_VAR_backup_create_archive=true TF_VAR_backup_send_mail_on_error=false \
				TF_VAR_mail_enabled=false TF_VAR_mail_use_tls=false TF_VAR_mail_use_ssl=true
		fi
		tf apply -auto-approve
		if [ "$revision" = 1 ]; then configuration_proof created; else configuration_proof updated; fi
		tf apply -refresh-only -auto-approve
		configuration_proof refreshed
		tf plan -detailed-exitcode
		no_backup_directory
	done
	# ProxyAPIModel.Password and MailServerAPIModel.Password are not
	# omitempty: only a partial descriptor can omit them. Prove preservation
	# before the provider restores its metadata.
	configuration_patch "{\"proxies\":{\"$TF_VAR_proxy_key\":{\"username\":\"retention-check\"}},\"mailServer\":{\"username\":\"retention-check\"}}"
	configuration_proof retained
	tf apply -auto-approve
	configuration_proof updated
	# Null makes the pinned providers send an empty or null password; actual
	# row absence is required even if retained Terraform state looks empty.
	unset TF_VAR_proxy_password TF_VAR_mail_password
	tf apply -auto-approve
	configuration_proof cleared
	tf apply -refresh-only -auto-approve
	configuration_proof cleared
}

# The two settings change one at a time, so a provider that sent or read
# back only one of them cannot pass; each revision is read back, refreshed
# and planned, and the checker asks the server for the effect as well.
# The provider's delete sends nothing, so the last revision restores the
# defaults before the destroy that follows.
general_security_revision() {
	export TF_VAR_enable_anonymous_access=$1 TF_VAR_encryption_policy=$2
	tf apply -auto-approve
	python3 -I "$here/check-general-security.py" --check "$1" "$2" "$work/general-security-encrypted"
	tf apply -refresh-only -auto-approve
	python3 -I "$here/check-general-security.py" --check "$1" "$2" "$work/general-security-encrypted"
	tf plan -detailed-exitcode
}

apply_general_security() {
	export GENERAL_SECURITY_ADMIN_PASSWORD=$password
	general_security_revision false SUPPORTED
	general_security_revision true SUPPORTED
	general_security_revision true REQUIRED
	general_security_revision false REQUIRED
	general_security_revision false UNSUPPORTED
	general_security_revision false SUPPORTED
}

# A singleton every instance has. Each revision is checked against the XML
# descriptor the provider reads and against what a delete then does, so a
# value stored but not obeyed fails. Import takes any ID for a singleton.
apply_trashcan() {
	for revision in 1 2; do
		if [ "$revision" = 1 ]; then
			export TF_VAR_enabled=false TF_VAR_retention_period_days=3
		else
			export TF_VAR_enabled=true TF_VAR_retention_period_days=0
		fi
		tf apply -auto-approve
		tf plan -detailed-exitcode
		python3 -I "$here/check-trashcan.py" --check "$TF_VAR_enabled" "$TF_VAR_retention_period_days"
	done
	tf state rm artifactory_trashcan_config.this
	tf import artifactory_trashcan_config.this trashcan
	tf plan -detailed-exitcode
}

for scenario in "${scenarios[@]}"; do
	dir="$work/scenarios/$scenario"
	case $scenario in
	deploy/artifactweave/terraform/repositories) apply_repositories ;;
	deploy/artifactweave/terraform/identity) apply_identity ;;
	deploy/artifactweave/terraform/oidc) apply_oidc ;;
	deploy/artifactweave/terraform/cdn-repositories) apply_cdn_repositories ;;
	deploy/artifactweave/terraform/remote-repositories) apply_remote_repositories ;;
	deploy/artifactweave/terraform/virtual-repositories) apply_virtual_repositories ;;
	deploy/artifactweave/terraform/webhooks) apply_webhooks ;;
	deploy/artifactweave/terraform/single-replication) apply_single_replication ;;
	deploy/artifactweave/terraform/projects) apply_projects ;;
	deploy/artifactweave/terraform/package-cleanup) apply_package_cleanup ;;
	deploy/artifactweave/terraform/configuration) apply_configuration ;;
	deploy/artifactweave/terraform/general-security) apply_general_security ;;
	deploy/artifactweave/terraform/trashcan) apply_trashcan ;;
	*)
		echo "run.sh has no apply steps for $scenario" >&2
		exit 1
		;;
	esac
	# 0 means the plan is empty: the providers read back exactly what they
	# wrote.
	tf plan -detailed-exitcode
	tf destroy -auto-approve
	if [ "$scenario" = deploy/artifactweave/terraform/configuration ]; then
		configuration_proof deleted
		tf state list >"$work/descriptor-remaining-resources.txt"
		test ! -s "$work/descriptor-remaining-resources.txt"
		no_backup_directory
		configuration_patch '{"localRepositories":{"tf-backup-skip-a":null,"tf-backup-skip-b":null,"tf-backup-skip-c":null}}'
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/general-security ]; then
		python3 -I "$here/check-general-security.py" --deleted "$work/general-security-encrypted"
		tf state list >"$work/general-security-remaining-resources.txt"
		test ! -s "$work/general-security-remaining-resources.txt"
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/remote-repositories ]; then
		python3 -I "$here/check-remotes.py" --remove-proxy
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/webhooks ]; then
		python3 -I "$here/check-webhooks.py" --deleted "$work/webhook-keys.json" "$work/server.log"
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/single-replication ]; then
		python3 -I "$here/check-replications.py" --deleted "$work/replication-repositories.json"
		python3 -I "$here/check-replications.py" --remove-proxy
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/projects ]; then
		python3 -I "$here/check-projects.py" --deleted "$work/project-fixture.json" "$project_evidence"
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/trashcan ]; then
		python3 -I "$here/check-trashcan.py" --deleted
	fi
	if [ "$scenario" = deploy/artifactweave/terraform/package-cleanup ]; then
		python3 -I "$here/check-cleanup.py" --deleted "$work/cleanup-fixture.json" "$work/cleanup-runs.json" "$work/server.log"
	fi
done
