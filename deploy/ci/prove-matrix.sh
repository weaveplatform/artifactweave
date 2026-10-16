#!/usr/bin/env bash
# Chooses which scenarios a pull request proves on real clouds
# (platform-scenarios.yml). Reads the pull request's changed paths, one per
# line, and prints "<cloud> <scenario>" for each scenario to prove:
#
#   - the workflow, deploy/ci/scenario-ci.sh, deploy/ci/platform-tests.sh
#     (the test step both CI and local.sh verify run) or this script: every
#     cloud's single-bucket, the baseline every scenario builds on;
#   - deploy/ci/cloudfront-key.sh or deploy/ci/cloud-cdn-key.sh: the
#     scenarios whose ci-vars.sh takes the key it makes (CLOUDFRONT_*,
#     aws/cdn; CLOUD_CDN_*, gcp/cdn);
#   - a test/platform Go file: the scenarios it names as quoted
#     "<cloud>/<scenario>" literals (mirror_test.go names the multi-region
#     scenarios and multicloud/mirror, replica_test.go the geo-replicated
#     ones); a file that names none (harness_test.go, generic_test.go), any
#     other test/platform file and a deleted one prove every cloud's
#     single-bucket;
#   - a module's top-level .tf (modules/<cloud>/<capability>/*.tf): every
#     scenario, in any cloud directory, whose Terraform sources exactly that
#     module;
#   - a scenario's top-level .tf, .terraform.lock.hcl or ci-vars.sh: that
#     scenario;
#   - anything else proves nothing: READMEs, Makefiles, tests/, docs, and the
#     rest of deploy/ci (the local runner, the lab's make files and guards,
#     which CI runs never use) change no cloud run.
#
#   prove-matrix.sh --all   prints every scenario that runs in CI, for the
#                           prove-all pull request label
#
# A scenario proven with the test lifecycle alone carries a third word,
# "test" ("azure cdn test"): platform-scenarios.yml then skips the later
# apply, destroy and reap, which would create two more instances.
#
# Only scenarios with a ci-vars.sh run in CI. The bootstraps live under
# deploy/ci/terraform, not beside the scenarios, so a bootstrap change proves
# nothing here (platform-auth-check.yml covers it).
# PROVE_MATRIX_ROOT points the script at another tree (prove-matrix_test.sh).
set -euo pipefail

root=${PROVE_MATRIX_ROOT:-$(git rev-parse --show-toplevel)}
here=deploy/platform/terraform
ci=deploy/ci

ci_capable() { [ -f "$root/$here/$1/$2/ci-vars.sh" ]; }

# Cloud directories are found, not listed, so a multicloud directory joins
# without a change here; modules/ holds modules, not scenarios.
clouds() {
  local dir
  for dir in "$root/$here"/*/; do
    [ -d "$dir" ] || continue
    dir=$(basename "$dir")
    [ "$dir" = modules ] || echo "$dir"
  done
}

scenarios() {
  local cloud dir
  for cloud in $(clouds); do
    for dir in "$root/$here/$cloud"/*/; do
      [ -d "$dir" ] || continue
      dir=$(basename "$dir")
      if ci_capable "$cloud" "$dir"; then echo "$cloud $dir"; fi
    done
  done
}

# key_users prints the scenarios whose ci-vars.sh names the variables a key
# script's key reaches it by: CLOUDFRONT_ for cloudfront-key.sh's key pair,
# CLOUD_CDN_ for cloud-cdn-key.sh's signed URL key.
key_users() { # variable prefix
  scenarios | while read -r cloud scenario; do
    if grep -qs "$1" "$root/$here/$cloud/$scenario/ci-vars.sh"; then echo "$cloud $scenario"; fi
  done
}

single_buckets() {
  local cloud
  for cloud in $(clouds); do
    if ci_capable "$cloud" single-bucket; then echo "$cloud single-bucket"; fi
  done
}

# named prints the scenarios a Go file names as quoted "<cloud>/<scenario>"
# literals. Other quoted strings with a slash ("application/json") name no
# scenario directory and drop out.
named() {
  local cloud scenario
  grep -oE '"[a-z][a-z0-9]*/[a-z][a-z0-9-]*"' "$1" 2>/dev/null | tr -d '"' | while IFS=/ read -r cloud scenario; do
    [ "$cloud" = modules ] && continue
    if ci_capable "$cloud" "$scenario"; then echo "$cloud $scenario"; fi
  done
  return 0
}

# users prints the scenarios whose Terraform sources exactly
# modules/<cloud>/<capability>: the source must end there, so a sibling such
# as modules/aws/storage-cdn does not count as modules/aws/storage.
users() {
  local module=$1 cloud scenario
  scenarios | while read -r cloud scenario; do
    if grep -Eqs "source[[:space:]]*=[[:space:]]*\"[^\"]*modules/$module\"" "$root/$here/$cloud/$scenario"/*.tf; then
      echo "$cloud $scenario"
    fi
  done
}

# Proven with the test lifecycle alone (apply, test/platform, destroy and the
# check that nothing remains): azure/cdn, whose Front Door Premium profile
# bills by the hour, about $0.45 (the maintainer's cost decision on #134).
# The other scenarios already prove the full lifecycle on the same driver.
test_only="azure/cdn"

lifecycle() {
  local cloud scenario
  while read -r cloud scenario; do
    case " $test_only " in
    *" $cloud/$scenario "*) echo "$cloud $scenario test" ;;
    *) echo "$cloud $scenario" ;;
    esac
  done
}

if [ "${1:-}" = --all ]; then
  scenarios | sort -u | lifecycle
  exit 0
fi

# Pairs are printed as found and de-duplicated by sort -u: no associative
# arrays, so the script runs on macOS's bash 3.2 too.
while IFS= read -r file; do
  case $file in
  .github/workflows/platform-scenarios.yml | "$ci/scenario-ci.sh" | "$ci/platform-tests.sh" | "$ci/prove-matrix.sh")
    single_buckets
    ;;
  "$ci/cloudfront-key.sh")
    key_users CLOUDFRONT_
    ;;
  "$ci/cloud-cdn-key.sh")
    key_users CLOUD_CDN_
    ;;
  test/platform/*.go)
    if [ -n "$(named "$root/$file")" ]; then
      named "$root/$file"
    else
      single_buckets
    fi
    ;;
  test/platform/*)
    single_buckets
    ;;
  "$here"/modules/*)
    # modules/<cloud>/<capability>/<file>.tf, a top-level file only.
    rel=${file#"$here/modules/"}
    cloud=${rel%%/*}
    rest=${rel#*/}
    capability=${rest%%/*}
    name=${rest#*/}
    case $name in */* | "$rest") continue ;; esac
    case $name in *.tf) users "$cloud/$capability" ;; esac
    ;;
  "$here"/*/*/*)
    # <cloud>/<scenario>/<file>, a top-level file only.
    rel=${file#"$here/"}
    cloud=${rel%%/*}
    rest=${rel#*/}
    scenario=${rest%%/*}
    name=${rest#*/}
    case $name in */* | "$rest") continue ;; esac
    case $name in
    *.tf | .terraform.lock.hcl | ci-vars.sh)
      if ci_capable "$cloud" "$scenario"; then echo "$cloud $scenario"; fi
      ;;
    esac
    ;;
  esac
done | sort -u | lifecycle
