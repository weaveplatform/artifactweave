#!/usr/bin/env bash
# Static checks and mocked tests over every Terraform root under deploy/ and
# test/terraform, with pinned tools (decision 0030):
#
#   check.sh        the prove-matrix table test and the contract lint, then
#                   fmt, validate, tflint, trivy, terraform test and the
#                   terraform-docs drift check
#   check.sh docs   rewrite the terraform-docs sections of the READMEs
#
# Each tool is fetched once into .tools/ and checked against the SHA-256 in
# its release's checksum file, so a laptop and CI run the same versions. A
# root is any directory holding *.tf files: roots are found, not listed, so
# moving one needs no change here.
set -euo pipefail

TERRAFORM_VERSION=1.16.4
TFLINT_VERSION=0.64.0
TRIVY_VERSION=0.76.0
TERRAFORM_DOCS_VERSION=0.24.0

# From each release's checksum file. A case, not an associative array,
# because macOS ships bash 3.2.
tool_sha256() {
	case $1_$2 in
	terraform_linux_amd64) echo dc94af0eef1147718ad7c8daea792ed199e3e0492eec180d0adafa2a65a879df ;;
	terraform_linux_arm64) echo 8263f301cb1a24489a4adeed147bf28504053f77237b3ea97a0ef2972659de30 ;;
	terraform_darwin_amd64) echo 2ee4b62064086e4b24b0d6cf2e61718fbaf0556feba990f708a5e32557554b3b ;;
	terraform_darwin_arm64) echo 42cfdf97ad722f79085fe2279b06d4b8680172de3534b22eeddd9a0fbbe7b8f1 ;;
	tflint_linux_amd64) echo cca9d13e2e1d7a2c627af60ff899a3c9b74212899416aeb96ec764d2ef954537 ;;
	tflint_linux_arm64) echo 560da89aacf59389d4eb029730dd5b109b7288096c32f2726a0d9e783a5ea8eb ;;
	tflint_darwin_amd64) echo 0f3a9fd17526014646a2dfc3f9122f7b4161abe3d6b0f0f03f9014483ddf4d19 ;;
	tflint_darwin_arm64) echo 2496e9cb3d24992d553b45e7c87a0fdc9449ca975233876247a9bfeda857e6c0 ;;
	trivy_linux_amd64) echo e24b1c51293c1b15e4ba8d45bbc7891ba053f90278ec2698f3562c993e145fe7 ;;
	trivy_linux_arm64) echo aab21e677a7b7071fc79f1711480840754c18a1b9d9cefa7ccd06af63a0e8f4c ;;
	trivy_darwin_amd64) echo 89563d4d495cf4ef06a0d1950f8710b124e9f7bd2615ec6c47b03673f4ad45a2 ;;
	trivy_darwin_arm64) echo e7ec0d21fe842553f59af99aba2efbcc822369fbaed27019c575408b6d135fce ;;
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
	echo "usage: $0 [check|docs]" >&2
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
