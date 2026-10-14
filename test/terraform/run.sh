#!/usr/bin/env bash
# Runs the Terraform fixture against artifactweave with the pinned JFrog
# Artifactory provider, installed from a filesystem mirror built from the
# release zip and its pinned SHA-256, so no run depends on the registry.
#
#   run.sh validate   init from the mirror and validate the fixture
#   run.sh apply      also start a -cover artifactweave, mint an admin access
#                     token through its token API, apply, require an empty
#                     plan, and destroy
#
# The provider authenticates only with a Bearer access token, so apply mints
# one with the bootstrap admin's password; TERRAFORM_ACCESS_TOKEN overrides it.
set -euo pipefail

PROVIDER_VERSION=12.11.14
# From terraform-provider-artifactory_${PROVIDER_VERSION}_SHA256SUMS. A case,
# not an associative array, because macOS ships bash 3.2.
provider_sha256() {
	case $1 in
	linux_amd64) echo 52380363f451b6b2db267b3183e500148c3b8a89924350b61a6884fdf43c00c2 ;;
	linux_arm64) echo 8f03320b8e1b06e3305869dd8f32ab58d73085caf083dbe8c516457ffb21c5df ;;
	darwin_amd64) echo cd5308dc8c8dcf8684638cb7f8bb01dc384b56533c27ab8b5d087bbdd620b621 ;;
	darwin_arm64) echo b2dd6b1c6ea68370ecf8b6764208053aefbbafb3d5acdc2c0cee24bb4635ea20 ;;
	esac
}

mode=${1:-validate}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
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
sum=$(provider_sha256 "$platform")
[ -n "$sum" ] || { echo "no pinned provider checksum for $platform" >&2; exit 1; }

# The packed mirror layout: <mirror>/<host>/<namespace>/<type>/<zip>.
mirror="$work/mirror/registry.terraform.io/jfrog/artifactory"
zip="terraform-provider-artifactory_${PROVIDER_VERSION}_${platform}.zip"
mkdir -p "$mirror"
if [ ! -f "$mirror/$zip" ]; then
	curl -fsSL -o "$mirror/$zip.part" \
		"https://github.com/jfrog/terraform-provider-artifactory/releases/download/v${PROVIDER_VERSION}/${zip}"
	echo "$sum  $mirror/$zip.part" | (sha256sum -c - 2>/dev/null || shasum -a 256 -c -) >/dev/null
	mv "$mirror/$zip.part" "$mirror/$zip"
fi
cat >"$work/terraformrc" <<RC
provider_installation {
  filesystem_mirror {
    path    = "$work/mirror"
    include = ["registry.terraform.io/jfrog/artifactory"]
  }
  direct {
    exclude = ["registry.terraform.io/jfrog/artifactory"]
  }
}
RC
export TF_CLI_CONFIG_FILE="$work/terraformrc" TF_IN_AUTOMATION=1 TF_INPUT=0
export TF_DATA_DIR="$work/tfdata"
# Terraform runs on a copy in $work so its local state, kept beside main.tf,
# never lands in the source tree, where a failed run's state would point the
# next run at resources on a server that no longer exists.
mkdir -p "$work/fixture"
cp "$here"/*.tf "$here/.terraform.lock.hcl" "$work/fixture/"
tf() { terraform -chdir="$work/fixture" "$@"; }

tf init -backend=false -lockfile=readonly
tf validate
[ "$mode" = apply ] || exit 0

# A -cover binary so the run's coverage joins the merged report. The flags
# match the acceptance build: unit tests run with -race, which forces atomic
# counters, and covdata merge refuses to mix counter modes.
module=$(cd "$root" && go list -m)
(cd "$root" && go build -cover -covermode=atomic -coverpkg="$module/..." 	-o "$work/artifactweave" ./cmd/artifactweave)
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
vars=(-var "url=$url" -var "access_token=$token")
tf apply -auto-approve "${vars[@]}" -var with_properties=false
curl -fsS -u "admin:$password" -X PUT --data-binary 'fixture' \
	"$url/artifactory/tf-generic-local/fixture.txt" >/dev/null
tf apply -auto-approve "${vars[@]}"
# 0 means the plan is empty: the provider reads back exactly what it wrote.
tf plan -detailed-exitcode "${vars[@]}"
tf destroy -auto-approve "${vars[@]}"
