#!/usr/bin/env bash
# Chooses which scenarios a pull request proves on real clouds
# (platform-scenarios.yml). Reads the pull request's changed paths, one per
# line, and prints "<cloud> <scenario>" for each scenario to prove:
#
#   - the workflow, scenario-ci.sh or this script: every cloud's
#     single-bucket, the baseline every scenario builds on;
#   - a test/platform Go file: the scenarios it names as quoted
#     "<cloud>/<scenario>" literals (mirror_test.go names the multi-region
#     scenarios, replica_test.go the geo-replicated ones); a file that names
#     none (harness_test.go, generic_test.go), any other test/platform file
#     and a deleted one prove every cloud's single-bucket;
#   - a module's top-level .tf (modules/<cloud>/<capability>/*.tf): every
#     scenario, in any cloud directory, whose Terraform sources exactly that
#     module;
#   - a scenario's top-level .tf, .terraform.lock.hcl or ci-vars.sh: that
#     scenario;
#   - anything else proves nothing: READMEs, Makefiles, tests/, the local
#     container's scripts and docs change no cloud run.
#
#   prove-matrix.sh --all   prints every scenario that runs in CI, for the
#                           prove-all pull request label
#
# Only scenarios with a ci-vars.sh run in CI; bootstraps have none, so a
# bootstrap change proves nothing here (platform-auth-check.yml covers it).
# PROVE_MATRIX_ROOT points the script at another tree (prove-matrix_test.sh).
set -euo pipefail

root=${PROVE_MATRIX_ROOT:-$(git rev-parse --show-toplevel)}
here=deploy/platform/terraform

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

if [ "${1:-}" = --all ]; then
  scenarios | sort -u
  exit 0
fi

# Pairs are printed as found and de-duplicated by sort -u: no associative
# arrays, so the script runs on macOS's bash 3.2 too.
while IFS= read -r file; do
  case $file in
  .github/workflows/platform-scenarios.yml | "$here/scenario-ci.sh" | "$here/prove-matrix.sh")
    single_buckets
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
done | sort -u
