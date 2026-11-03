#!/usr/bin/env bash
# Static checks and mocked tests over every Terraform root under deploy/ and
# test/terraform, with pinned tools (decision 0030):
#
#   check.sh        the prove-matrix and jfrog-providers table tests and the
#                   contract lint, then fmt, validate, tflint, trivy,
#                   terraform test and the terraform-docs drift check
#   check.sh docs   rewrite the terraform-docs sections of the READMEs
#   check.sh tools  fetch the pinned tools into .tools/ and stop
#
# Each tool is fetched once into .tools/ and checked against the SHA-256 in
# its release's checksum file, so a laptop and CI run the same versions. A
# root is any directory holding *.tf files: roots are found, not listed, so
# moving one needs no change here.
set -euo pipefail

TERRAFORM_VERSION=1.17.0
TFLINT_VERSION=0.65.0
TRIVY_VERSION=0.76.1
TERRAFORM_DOCS_VERSION=0.24.0

# From each release's checksum file. A case, not an associative array,
# because macOS ships bash 3.2.
tool_sha256() {
	case $1_$2 in
	terraform_linux_amd64) echo c168cab7c637a8769fdb90089ddd19de701bc6ff8aca64dc2e2185fe46f59f67 ;;
	terraform_linux_arm64) echo f32478134ffc6d710eec474bbd316652cdddc0f4e336973a3745e8d3a3309cdf ;;
	terraform_darwin_amd64) echo 3773397524196e4ebbf7167e2b9fb478dff1962d555d649f616ba97ca446a143 ;;
	terraform_darwin_arm64) echo 7472a54cca911d46c0e399a5ebbce44444912d0d4dad7495f37f1fa51e7ca6b7 ;;
	tflint_linux_amd64) echo d4c545ed2e29d6338604b586f34f544f380f70f243aa72f7b484713835eaec61 ;;
	tflint_linux_arm64) echo 843017c47a98be1de4d88fa038daa293770fe25cd01656c4f08b2e30d1d3b3a0 ;;
	tflint_darwin_amd64) echo 27696ea23ed77c1918945147ebfe6485a74f58560b9557ccecc77379205702c5 ;;
	tflint_darwin_arm64) echo 5a71c63481ba56760864298011fdb7317a9ae80a0bbaeceefa35883c7f62ac71 ;;
	trivy_linux_amd64) echo c7cbe3137fb8a48a17574bede3dd6beb16742d1e9772929940fc90082c39d9c0 ;;
	trivy_linux_arm64) echo e174dff2a6dba5e3ae41f8d0537a6324405bd08132497340304c6b20e0f16a1f ;;
	trivy_darwin_amd64) echo 7aa762eacd582052d053b62dcf972a1ab058abe39a57ccae234ed8894945b6b7 ;;
	trivy_darwin_arm64) echo 11a9b6cd2752011b54f01ea4bd0dbfa9f0c41dc6538d5a3f9d23955f471bf4e5 ;;
	terraform-docs_linux_amd64) echo 9005daf969de0b50134493a2c00078b49f5f5b39d021cda7c89bf4d4f3d776d3 ;;
	terraform-docs_linux_arm64) echo d12bd7b73c1fc9c64efc79f8157dd713dabd559f1ecf3cfc0f42e32279a155fd ;;
	terraform-docs_darwin_amd64) echo 3c3f7f18f908457fd1209cbe341418f7f6bae78c08126cfbe8de0d1b06aa8781 ;;
	terraform-docs_darwin_arm64) echo f6b114f4b032f3f9202ab6c23bfd28c3c8e68aeeb8a8f12fc118bf2073081d71 ;;
	esac
}

mode=${1:-check}
root=$(cd "$(dirname "$0")/.." && pwd)
tools=$root/.tools
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failed=
fail() {
	echo "FAIL: $*" >&2
	failed=1
}

# The table test and the contract lint need no tools, so they run before any
# is fetched and a wrong proof rule or a root off the contract fails fast.
if [ "$mode" = check ]; then
	echo "== prove-matrix"
	"$root/deploy/ci/prove-matrix_test.sh" || fail "prove-matrix.sh chooses the wrong scenarios (deploy/ci/prove-matrix_test.sh)"
	echo "== jfrog-providers"
	"$root/deploy/ci/jfrog-providers_test.sh" || fail "jfrog-providers.sh rewrites the pins wrongly (deploy/ci/jfrog-providers_test.sh)"
	echo "== contract"
	"$root/deploy/ci/lint-contract.sh" || fail "a Terraform root breaks the reference-deployment contract (deploy/ci/lint-contract.sh)"
	"$root/deploy/ci/lint-contract.sh" --self-test || fail "the contract lint misjudges its fixtures (deploy/ci/lint-contract.sh --self-test)"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
	echo "unsupported machine $(uname -m)" >&2
	exit 1
	;;
esac

# fetch downloads one release archive into .tools/<name>-<version>,
# checks its pinned SHA-256 and unpacks it there.
fetch() {
	local name=$1 version=$2 url=$3 dir sum
	dir=$tools/$name-$version
	[ -d "$dir" ] && return
	sum=$(tool_sha256 "$name" "${os}_$arch")
	[ -n "$sum" ] || {
		echo "no pinned $name checksum for ${os}_$arch" >&2
		exit 1
	}
	mkdir -p "$dir.part"
	curl -fsSL -o "$dir.part/download" "$url"
	echo "$sum  $dir.part/download" | (sha256sum -c - 2>/dev/null || shasum -a 256 -c -) >/dev/null
	case $url in
	*.zip) unzip -q -o "$dir.part/download" -d "$dir.part" ;;
	*) tar -xzf "$dir.part/download" -C "$dir.part" ;;
	esac
	rm "$dir.part/download"
	mv "$dir.part" "$dir"
}

# A terraform already on PATH at the pinned version is used as it is.
if [ "$(terraform version 2>/dev/null | head -1)" != "Terraform v$TERRAFORM_VERSION" ]; then
	fetch terraform "$TERRAFORM_VERSION" \
		"https://releases.hashicorp.com/terraform/$TERRAFORM_VERSION/terraform_${TERRAFORM_VERSION}_${os}_$arch.zip"
	PATH=$tools/terraform-$TERRAFORM_VERSION:$PATH
fi
fetch tflint "$TFLINT_VERSION" \
	"https://github.com/terraform-linters/tflint/releases/download/v$TFLINT_VERSION/tflint_${os}_$arch.zip"
trivy_os=Linux
[ "$os" = darwin ] && trivy_os=macOS
trivy_arch=64bit
[ "$arch" = arm64 ] && trivy_arch=ARM64
fetch trivy "$TRIVY_VERSION" \
	"https://github.com/aquasecurity/trivy/releases/download/v$TRIVY_VERSION/trivy_${TRIVY_VERSION}_$trivy_os-$trivy_arch.tar.gz"
fetch terraform-docs "$TERRAFORM_DOCS_VERSION" \
	"https://github.com/terraform-docs/terraform-docs/releases/download/v$TERRAFORM_DOCS_VERSION/terraform-docs-v$TERRAFORM_DOCS_VERSION-${os}-$arch.tar.gz"
PATH=$tools/tflint-$TFLINT_VERSION:$tools/trivy-$TRIVY_VERSION:$tools/terraform-docs-$TERRAFORM_DOCS_VERSION:$PATH
export PATH TF_IN_AUTOMATION=1 TF_INPUT=0 TF_PLUGIN_CACHE_DIR=$tools/terraform-plugin-cache
mkdir -p "$TF_PLUGIN_CACHE_DIR"
# deploy/ci/jfrog-providers.sh borrows the pinned terraform from here.
[ "$mode" = tools ] && exit 0

if [ "$mode" = check ]; then
	echo "== Terraform dependency docs automation"
	PYTHONDONTWRITEBYTECODE=1 python3 "$root/deploy/ci/terraform_docs_test.py" || fail "Terraform dependency docs regression tests"
fi

cd "$root"
roots=$(find deploy test/terraform -name '*.tf' -not -path '*/.terraform/*' -exec dirname {} \; | sort -u)
# Roots on the jfrog providers validate through run.sh's filesystem mirror,
# which pins those providers' zips by SHA-256; the others install from the
# registry against their committed lock files.
jfrog_roots=
cloud_roots=
for r in $roots; do
	if grep -qs 'source *= *"jfrog/' "$r"/*.tf; then
		jfrog_roots="$jfrog_roots $r"
	else
		cloud_roots="$cloud_roots $r"
	fi
done

readme_roots=
for r in $roots; do
	grep -qs 'BEGIN_TF_DOCS' "$r/README.md" && readme_roots="$readme_roots $r"
done

# docs regenerates a root's README section in the directory given.
docs() {
	terraform-docs --config "$root/.terraform-docs.yml" "$1" >/dev/null
}

if [ "$mode" = docs ]; then
	for r in $readme_roots; do docs "$r"; done
	exit 0
fi
[ "$mode" = check ] || {
	echo "usage: $0 [check|docs|tools]" >&2
	exit 2
}

# State and provider plugins live in $work, never beside the sources.
tfin() {
	local r=$1
	shift
	TF_DATA_DIR=$work/data/$r terraform -chdir="$r" "$@"
}

echo "== terraform fmt"
terraform fmt -check -recursive -diff deploy test/terraform || fail "terraform fmt (run terraform fmt -recursive deploy test/terraform)"

echo "== terraform validate"
for r in $cloud_roots; do
	echo "-- $r"
	tfin "$r" init -backend=false -lockfile=readonly >/dev/null || {
		fail "terraform init in $r"
		continue
	}
	tfin "$r" validate -no-color || fail "terraform validate in $r"
done
if [ -n "$jfrog_roots" ]; then
	# shellcheck disable=SC2086 # the root list is word-split on purpose
	"$root/test/terraform/run.sh" validate $jfrog_roots || fail "terraform validate in$jfrog_roots"
fi

echo "== tflint"
export TFLINT_PLUGIN_DIR=$tools/tflint-plugins
tflint --init --config "$root/.tflint.hcl" >/dev/null
for r in $roots; do
	echo "-- $r"
	tflint --chdir="$r" --config "$root/.tflint.hcl" --format compact || fail "tflint in $r"
done

echo "== trivy config"
for r in $roots; do
	echo "-- $r"
	# --skip-check-update keeps the checks embedded in the pinned binary, so
	# a run never changes because the published bundle did. Accepted findings
	# are #trivy:ignore comments beside the resource, each with its reason,
	# so they move with the root.
	trivy config --quiet --skip-check-update --misconfig-scanners terraform \
		--cache-dir "$tools/trivy-cache" \
		--exit-code 1 "$r" || fail "trivy config in $r"
done

echo "== terraform test"
for r in $cloud_roots; do
	ls "$r"/tests/*.tftest.hcl >/dev/null 2>&1 || continue
	echo "-- $r"
	tfin "$r" test -no-color || fail "terraform test in $r"
done
for r in $jfrog_roots; do
	if ls "$r"/tests/*.tftest.hcl >/dev/null 2>&1; then
		fail "$r has tests, but run.sh only validates jfrog roots: teach this script to init them from its mirror"
	fi
done

echo "== terraform-docs"
for r in $readme_roots; do
	echo "-- $r"
	mkdir -p "$work/docs/$r"
	cp "$r"/*.tf "$r/README.md" "$work/docs/$r/"
	cp "$r/.terraform.lock.hcl" "$work/docs/$r/" 2>/dev/null || true
	docs "$work/docs/$r"
	diff -u "$r/README.md" "$work/docs/$r/README.md" || fail "terraform-docs section of $r/README.md is stale (run make deploy-docs)"
done

[ -z "$failed" ] || exit 1
echo "deploy checks passed"
