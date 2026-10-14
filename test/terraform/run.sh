#!/usr/bin/env bash
# Runs Terraform scenarios against artifactweave with the pinned JFrog
# providers, installed from a filesystem mirror built from the release zips
# and their pinned SHA-256s, so no run depends on the registry.
#
#   run.sh validate [scenario...]  init from the mirror and validate each
#   run.sh apply [scenario...]     also start one -cover artifactweave, mint
#                                  an admin access token through its token
#                                  API, then apply each scenario, require an
#                                  empty plan, and destroy it
#
# A scenario is a directory relative to the repository root; the default is
# this directory's repositories fixture. The providers authenticate only with
# a Bearer access token, so apply mints one with the bootstrap admin's
# password; TERRAFORM_ACCESS_TOKEN overrides it.
set -euo pipefail

ARTIFACTORY_VERSION=12.11.14
PLATFORM_VERSION=2.2.11
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
	esac
}

mode=${1:-validate}
shift || true
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
scenarios=("$@")
[ ${#scenarios[@]} -gt 0 ] || scenarios=(test/terraform)
work=${TERRAFORM_WORK:-}
if [ -z "$work" ]; then
	work=$(mktemp -d)
	owned=1
fi
mkdir -p "$work"
server=
cleanup() {
	if [ -n "$server" ]; then
		kill -TERM "$server" 2>/dev/null || true
		wait "$server" 2>/dev/null || true
	fi
	if [ -n "${owned:-}" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT

platform="$(go env GOOS)_$(go env GOARCH)"

# mirror_provider fetches one provider's zip into the packed mirror layout,
# <mirror>/<host>/<namespace>/<type>/<zip>, checking its pinned SHA-256.
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
mirror_provider artifactory "$ARTIFACTORY_VERSION"
mirror_provider platform "$PLATFORM_VERSION"
cat >"$work/terraformrc" <<RC
provider_installation {
  filesystem_mirror {
    path    = "$work/mirror"
    include = ["registry.terraform.io/jfrog/artifactory", "registry.terraform.io/jfrog/platform"]
  }
  direct {
    exclude = ["registry.terraform.io/jfrog/artifactory", "registry.terraform.io/jfrog/platform"]
  }
}
RC
export TF_CLI_CONFIG_FILE="$work/terraformrc" TF_IN_AUTOMATION=1 TF_INPUT=0

# Each scenario runs on a copy in $work so its local state, kept beside
# main.tf, never lands in the source tree, where a failed run's state would
# point the next run at resources on a server that no longer exists.
dir=
tf() { TF_DATA_DIR="$dir/.terraform" terraform -chdir="$dir" "$@"; }

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
(cd "$root" && go build -cover -covermode=atomic -coverpkg="$module/..." -o "$work/artifactweave" ./cmd/artifactweave)
port=${TERRAFORM_PORT:-18081}
url="http://127.0.0.1:$port"
password=terraform-admin
mkdir -p "${GOCOVERDIR:-$work/cover}"
GOCOVERDIR=${GOCOVERDIR:-$work/cover} \
	ARTIFACTWEAVE_MASTER_KEY=terraform-master-key-0123456789abcdef0123456789abcdef \
	ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=$password \
	"$work/artifactweave" serve --listen "127.0.0.1:$port" --data-dir "$work/data" \
	--base-url "$url" >"$work/server.log" 2>&1 &
server=$!
for _ in $(seq 100); do
	curl -fs "$url/artifactory/api/v1/system/readiness" >/dev/null && break
	sleep 0.1
done

token=${TERRAFORM_ACCESS_TOKEN:-}
if [ -z "$token" ]; then
	token=$(curl -fsS -u "admin:$password" -H 'Content-Type: application/json' \
		-d '{"scope":"applied-permissions/admin","expires_in":3600}' \
		"$url/access/api/v1/tokens" |
		python3 -I -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
fi
export TF_VAR_url=$url TF_VAR_access_token=$token

# The repositories fixture: the provider cannot deploy content, so the file
# its properties go on is deployed between a first apply without them and
# the full one.
apply_repositories() {
	tf apply -auto-approve -var with_properties=false
	curl -fsS -u "admin:$password" -X PUT --data-binary 'fixture' \
		"$url/artifactory/tf-generic-local/fixture.txt" >/dev/null
	tf apply -auto-approve
}

# The identity scenario: a fresh RSA key pair per run, passed through the
# environment so the private key never appears in a process listing. The
# provider accepts only a PKCS#1 private key ("RSA PRIVATE KEY"), which
# OpenSSL 3 writes only with -traditional and LibreSSL writes by default.
apply_identity() {
	openssl genrsa -out "$work/keypair.pem" 2048 2>/dev/null
	if ! grep -q 'BEGIN RSA PRIVATE KEY' "$work/keypair.pem"; then
		openssl rsa -in "$work/keypair.pem" -traditional -out "$work/keypair.pem" 2>/dev/null
	fi
	openssl rsa -in "$work/keypair.pem" -pubout -out "$work/keypair.pub" 2>/dev/null
	TF_VAR_keypair_private_key=$(cat "$work/keypair.pem")
	TF_VAR_keypair_public_key=$(cat "$work/keypair.pub")
	TF_VAR_user_password=Terraform-user-1
	export TF_VAR_keypair_private_key TF_VAR_keypair_public_key TF_VAR_user_password
	tf apply -auto-approve
	# The permission written through Access v2 must read back through the
	# legacy v2 permission target API with its repository.
	tf state show data.artifactory_permission_target.team | grep -q '"tf-team-local"' ||
		{ echo "the legacy permission target API does not report tf-team-local" >&2; exit 1; }
}

for scenario in "${scenarios[@]}"; do
	dir="$work/scenarios/$scenario"
	case $scenario in
	test/terraform) apply_repositories ;;
	deploy/artifactweave/terraform/identity) apply_identity ;;
	*)
		echo "run.sh has no apply steps for $scenario" >&2
		exit 1
		;;
	esac
	# 0 means the plan is empty: the providers read back exactly what they
	# wrote.
	tf plan -detailed-exitcode
	tf destroy -auto-approve
done
