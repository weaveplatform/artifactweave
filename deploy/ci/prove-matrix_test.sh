#!/usr/bin/env bash
# Table test for prove-matrix.sh, run by deploy/check.sh. Each case feeds a
# list of changed paths to the script over a generated fixture tree and
# compares the scenarios it chooses. The fixture is built in a temporary
# directory, never committed: check.sh treats any directory holding *.tf as
# a Terraform root. A last check reads the real tree: every scenario that a
# test/platform file names must exist and run in CI, or the rule that
# proves those files would silently prove nothing.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/prove-matrix.sh
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

t=$fixture/deploy/platform/terraform
scenario() { # <cloud>/<scenario> [module source ...]
  local dir=$t/$1 source
  shift
  mkdir -p "$dir"
  : >"$dir/ci-vars.sh"
  : >"$dir/main.tf"
  for source in "$@"; do
    printf 'module "m" {\n  source = "%s"\n}\n' "$source" >>"$dir/main.tf"
  done
}
scenario aws/single-bucket
scenario aws/multi-region ../../modules/aws/storage
scenario aws/geo-replicated ../../modules/aws/storage ../../modules/aws/replication
scenario aws/cdn ../../modules/aws/storage-cdn
scenario gcp/single-bucket
scenario gcp/multi-region ../../modules/gcp/storage
scenario azure/single-bucket
scenario multicloud/mirror ../../modules/aws/storage ../../modules/gcp/storage
# The bootstraps live under deploy/ci/terraform and never run in CI.
mkdir -p "$fixture/deploy/ci/terraform/aws" && : >"$fixture/deploy/ci/terraform/aws/main.tf"
mkdir -p "$t/modules/aws/storage" "$t/modules/aws/storage-cdn" "$t/modules/gcp/storage"
mkdir -p "$fixture/test/platform"
cat >"$fixture/test/platform/harness_test.go" <<'EOF'
// ARTIFACTWEAVE_SCENARIO is <cloud>/<scenario>, for example gcp/single-bucket.
const contentType = "application/json"
EOF
cat >"$fixture/test/platform/mirror_test.go" <<'EOF'
var mirrors = []string{"gcp/multi-region", "aws/multi-region"}
EOF
cat >"$fixture/test/platform/replica_test.go" <<'EOF'
func init() { scenarioChecks["aws/geo-replicated"] = nil }
EOF
cat >"$fixture/test/platform/ghost_test.go" <<'EOF'
var gone = "aws/no-such-scenario"
EOF

failed=0
# check <case> <expected, one pair per line> <changed path>...
check() {
  local name=$1 want=$2 got
  shift 2
  got=$(printf '%s\n' "$@" | PROVE_MATRIX_ROOT=$fixture "$script")
  if [ "$got" != "$want" ]; then
    printf 'FAIL %s\n  want: %s\n  got:  %s\n' "$name" "$(echo "$want" | paste -sd, -)" "$(echo "$got" | paste -sd, -)" >&2
    failed=1
  fi
}

singles=$'aws single-bucket\nazure single-bucket\ngcp single-bucket'
p=deploy/platform/terraform
c=deploy/ci

check 'workflow change' "$singles" .github/workflows/platform-scenarios.yml
check 'driver change' "$singles" $c/scenario-ci.sh
check 'shared test step changes' "$singles" $c/platform-tests.sh
check 'local runner' '' $c/local.sh
check 'this script changes' "$singles" $c/prove-matrix.sh
check 'table test changes' '' $c/prove-matrix_test.sh
check 'module .tf' $'aws geo-replicated\naws multi-region\nmulticloud mirror' $p/modules/aws/storage/main.tf
check 'sibling module is not the module' 'aws cdn' $p/modules/aws/storage-cdn/variables.tf
check 'deleted module .tf' $'gcp multi-region\nmulticloud mirror' $p/modules/gcp/storage/gone.tf
check 'module README' '' $p/modules/aws/storage/README.md
check 'module tests' '' $p/modules/aws/storage/tests/security.tftest.hcl
check 'mirror_test.go' $'aws multi-region\ngcp multi-region' test/platform/mirror_test.go
check 'replica_test.go' 'aws geo-replicated' test/platform/replica_test.go
check 'Go file naming no scenario' "$singles" test/platform/harness_test.go
check 'Go file naming only a missing scenario' "$singles" test/platform/ghost_test.go
check 'deleted Go file' "$singles" test/platform/deleted_test.go
check 'other test/platform file' "$singles" test/platform/testdata/blob.bin
check 'scenario .tf' 'aws single-bucket' $p/aws/single-bucket/main.tf
check 'scenario lock file' 'aws single-bucket' $p/aws/single-bucket/.terraform.lock.hcl
check 'scenario ci-vars.sh' 'gcp multi-region' $p/gcp/multi-region/ci-vars.sh
check 'deleted .tf of a scenario' 'azure single-bucket' $p/azure/single-bucket/moved.tf
check 'scenario README' '' $p/aws/single-bucket/README.md
check 'scenario Makefile' '' $p/aws/single-bucket/Makefile
check 'scenario tests' '' $p/aws/single-bucket/tests/security.tftest.hcl
check 'other file in a scenario' '' $p/aws/single-bucket/notes.txt
check 'bootstrap' '' $c/terraform/aws/main.tf
check 'lab constants' '' $c/lab.mk
check 'scenario make targets' '' $c/scenario.mk
check 'bootstrap make targets' '' $c/bootstrap.mk
check 'guard' '' $c/check-account.sh
check 'environments' '' $c/github-environments.sh
check 'deleted scenario' '' $p/aws/retired/main.tf
check 'platform README' '' deploy/platform/README.md
check 'docs' '' docs/testing/ci.md
check 'no changes' ''
check 'union, sorted and unique' $'aws multi-region\naws single-bucket\ngcp multi-region' \
  $p/aws/single-bucket/main.tf test/platform/mirror_test.go $p/aws/multi-region/main.tf README.md

all=$(PROVE_MATRIX_ROOT=$fixture "$script" --all)
want=$'aws cdn\naws geo-replicated\naws multi-region\naws single-bucket\nazure single-bucket\ngcp multi-region\ngcp single-bucket\nmulticloud mirror'
if [ "$all" != "$want" ]; then
  printf 'FAIL --all\n  want: %s\n  got:  %s\n' "$(echo "$want" | paste -sd, -)" "$(echo "$all" | paste -sd, -)" >&2
  failed=1
fi

# The real tree: a test/platform file that names a scenario must name one
# that exists and runs in CI.
root=$(cd "$here/../.." && pwd)
for file in "$root"/test/platform/*.go; do
  for pair in $(grep -oE '"(gcp|azure|aws|multicloud)/[a-z][a-z0-9-]*"' "$file" | tr -d '"' | sort -u); do
    if [ ! -f "$root/$p/$pair/ci-vars.sh" ]; then
      echo "FAIL ${file#"$root/"} names $pair, which is not a scenario that runs in CI" >&2
      failed=1
    fi
  done
done

[ "$failed" = 0 ] || exit 1
echo "prove-matrix: all cases pass"
