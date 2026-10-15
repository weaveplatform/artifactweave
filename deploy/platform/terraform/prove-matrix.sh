#!/usr/bin/env bash
# Chooses which scenarios a pull request proves on real clouds
# (platform-scenarios.yml): reads the pull request's changed paths, one per
# line, and prints "<cloud> <scenario>" for each scenario to prove.
#
#   - A change to the driver (this script, scenario-ci.sh, the workflow or
#     test/platform) proves every cloud's single-bucket, the baseline every
#     scenario builds on.
#   - A change inside <cloud>/<scenario>/ proves that scenario.
#   - A change inside modules/<cloud>/ proves every scenario of that cloud
#     whose Terraform calls one of its modules.
#
# Only scenarios with a ci-vars.sh run in CI; bootstraps have none, so a
# bootstrap change proves nothing here (platform-auth-check.yml covers it).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
here=deploy/platform/terraform
clouds="gcp azure aws"

ci_capable() { [ -f "$root/$here/$1/$2/ci-vars.sh" ]; }

# Pairs are printed as found and de-duplicated by sort -u: no associative
# arrays, so the script runs on macOS's bash 3.2 too.
while IFS= read -r file; do
  case $file in
  .github/workflows/platform-scenarios.yml | "$here/scenario-ci.sh" | "$here/prove-matrix.sh" | test/platform/*)
    for cloud in $clouds; do echo "$cloud single-bucket"; done
    ;;
  "$here"/modules/*/*)
    rest=${file#"$here/modules/"}
    cloud=${rest%%/*}
    for dir in "$root/$here/$cloud"/*/; do
      scenario=$(basename "$dir")
      ci_capable "$cloud" "$scenario" || continue
      if grep -qs "modules/$cloud/" "$dir"*.tf; then echo "$cloud $scenario"; fi
    done
    ;;
  "$here"/*/*/*)
    rest=${file#"$here/"}
    cloud=${rest%%/*}
    rest=${rest#*/}
    scenario=${rest%%/*}
    case " $clouds " in *" $cloud "*) ;; *) continue ;; esac
    if ci_capable "$cloud" "$scenario"; then echo "$cloud $scenario"; fi
    ;;
  esac
done | sort -u
