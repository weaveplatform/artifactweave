#!/usr/bin/env bash
# Table test for jfrog-providers.sh, run by deploy/check.sh. Every case is
# offline: the rewrites run on generated fixtures, and the signature cases
# use the real 12.11.14 SHA256SUMS and its signature, committed in
# testdata/jfrog-providers, against the committed key.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source-path=SCRIPTDIR source=jfrog-providers.sh
. "$here/jfrog-providers.sh"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
# shellcheck disable=SC2034 # read by the sourced functions
root=$fixture

failures=0
check() { # <name> <want> <got>
	if [ "$2" != "$3" ]; then
		echo "FAIL $1: want '$2', got '$3'" >&2
		failures=$((failures + 1))
	fi
}

# bump
while read -r from to want; do
	check "bump $from -> $to" "$want" "$(bump "$from" "$to")"
done <<'EOF'
12.11.14 12.11.14 none
12.11.14 12.11.15 patch
12.11.14 12.12.0 minor
12.11.14 13.0.0 major
2.2.9 2.2.10 patch
2.2.11 2.2.10 downgrade
12.11.14 11.99.99 downgrade
EOF

# pinned reads run.sh's versions.
mkdir -p "$fixture/test/terraform"
cp "$here/../../test/terraform/run.sh" "$fixture/test/terraform/run.sh"
check "pinned artifactory" "$(sed -n 's/^ARTIFACTORY_VERSION=//p' "$fixture/test/terraform/run.sh")" "$(pinned artifactory)"
check "pinned platform" "$(sed -n 's/^PLATFORM_VERSION=//p' "$fixture/test/terraform/run.sh")" "$(pinned platform)"

# rewrite_versions_tf moves only the named provider's block, wherever its
# version line sits relative to its source.
tfline() { sed -n "${1}p" "$fixture/versions.tf" | sed 's/^[[:space:]]*//' | tr -s ' '; }
cat >"$fixture/versions.tf" <<'EOF'
terraform {
  required_providers {
    artifactory = {
      source  = "jfrog/artifactory"
      version = "1.0.0"
    }
    platform = {
      version = "1.0.0"
      source  = "jfrog/platform"
    }
  }
}
EOF
rewrite_versions_tf "$fixture/versions.tf" artifactory 2.0.0
check "artifactory block moved" 'version = "2.0.0"' "$(tfline 5)"
check "platform block kept" 'version = "1.0.0"' "$(tfline 8)"
rewrite_versions_tf "$fixture/versions.tf" platform 3.0.0
check "platform version before source moved" 'version = "3.0.0"' "$(tfline 8)"
check "platform version stays above its source" 'source = "jfrog/platform"' "$(tfline 9)"
check "lines kept" 12 "$(wc -l <"$fixture/versions.tf" | tr -d ' ')"

# rewrite_run_sh moves the version and the four zip sums of one provider.
sums=$fixture/terraform-provider-artifactory_9.9.9_SHA256SUMS
for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
	printf '%s  terraform-provider-artifactory_9.9.9_%s.zip\n' "$(printf '%s' "$p" | shasum -a 256 | cut -d' ' -f1)" "$p"
done >"$sums"
platform_before=$(grep -E '^[[:space:]]*platform_' "$fixture/test/terraform/run.sh")
rewrite_run_sh "$fixture/test/terraform/run.sh" artifactory 9.9.9 "$sums"
check "run.sh version" 9.9.9 "$(pinned artifactory)"
for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
	check "run.sh $p sum" "$(sum_for "$sums" artifactory 9.9.9 "$p")" \
		"$(sed -n -E "s/^[[:space:]]*artifactory_$p\) echo ([0-9a-f]{64}) ;;$/\1/p" "$fixture/test/terraform/run.sh")"
done
check "run.sh platform lines kept" "$platform_before" "$(grep -E '^[[:space:]]*platform_' "$fixture/test/terraform/run.sh")"
bash -n "$fixture/test/terraform/run.sh" || check "run.sh still parses" ok broken
if (sum_for "$sums" artifactory 9.9.9 windows_amd64) >/dev/null 2>&1; then
	check "sum_for refuses a platform the sums lack" refused found
fi

# rewrite_prose replaces whole versions only.
mkdir -p "$fixture/deploy/artifactweave/terraform/x"
cat >"$fixture/deploy/artifactweave/terraform/x/README.md" <<'EOF'
installs 2.2.11 and v2.2.11, then 2.2.11 2.2.11 again
not 12.2.11 nor 2.2.110 nor 2.2.11.3
EOF
rewrite_prose 2.2.11 2.2.12
check "prose replaced" "installs 2.2.12 and v2.2.12, then 2.2.12 2.2.12 again" \
	"$(sed -n 1p "$fixture/deploy/artifactweave/terraform/x/README.md")"
check "prose untouched" "not 12.2.11 nor 2.2.110 nor 2.2.12.3" \
	"$(sed -n 2p "$fixture/deploy/artifactweave/terraform/x/README.md")"

# verify_sums: the real release checks, a tampered copy does not.
if command -v gpg >/dev/null; then
	data=$here/testdata/jfrog-providers
	if ! verify_sums "$data/terraform-provider-artifactory_12.11.14_SHA256SUMS" \
		"$data/terraform-provider-artifactory_12.11.14_SHA256SUMS.sig"; then
		check "the signed 12.11.14 SHA256SUMS verifies" verified refused
	fi
	cp "$data/terraform-provider-artifactory_12.11.14_SHA256SUMS" "$fixture/tampered"
	echo "0000000000000000000000000000000000000000000000000000000000000000  evil.zip" >>"$fixture/tampered"
	if verify_sums "$fixture/tampered" "$data/terraform-provider-artifactory_12.11.14_SHA256SUMS.sig"; then
		check "a tampered SHA256SUMS is refused" refused verified
	fi
	if (JFROG_SIGNING_FINGERPRINT=0000000000000000000000000000000000000000 verify_sums \
		"$data/terraform-provider-artifactory_12.11.14_SHA256SUMS" \
		"$data/terraform-provider-artifactory_12.11.14_SHA256SUMS.sig"); then
		check "a signature by a key other than the pinned one is refused" refused verified
	fi
else
	echo "skip the signature cases: no gpg" >&2
fi

if [ "$failures" -gt 0 ]; then
	echo "jfrog-providers_test: $failures failed" >&2
	exit 1
fi
echo "jfrog-providers_test: all cases pass"
