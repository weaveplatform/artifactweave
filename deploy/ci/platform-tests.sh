#!/usr/bin/env bash
# Runs test/platform against an applied scenario (decision 0030). One script
# for both callers, so a local check is the same check CI makes:
#
#   scenario-ci.sh test   on CI, against the run's copy of the scenario
#   local.sh verify       on a workstation, against the scenario directory
#
#   platform-tests.sh <terraform-dir> <cloud>/<scenario> <out-dir>
#
# It reads the scenario's outputs, builds artifactweave, and runs the suite,
# which starts that binary itself with the rendered blob configuration.
# Credentials come from the caller's environment. The timeout leaves room for
# gcp/cdn's wait for its certificate and load balancer, up to 45 minutes,
# before the checks themselves.
set -euo pipefail

dir=${1:?usage: platform-tests.sh <terraform-dir> <cloud>/<scenario> <out-dir>}
name=${2:?usage: platform-tests.sh <terraform-dir> <cloud>/<scenario> <out-dir>}
out=${3:?usage: platform-tests.sh <terraform-dir> <cloud>/<scenario> <out-dir>}
root=$(git rev-parse --show-toplevel)
terraform=${TERRAFORM:-terraform}
export GOWORK=off

mkdir -p "$out"
"$terraform" -chdir="$dir" output -raw artifactweave_config >"$out/blob.yaml"
# Every output, for the checks a scenario adds (the mirror's members, for
# one). The directory is the runner's or .local/, never uploaded or committed.
"$terraform" -chdir="$dir" output -json >"$out/outputs.json"
host=$("$terraform" -chdir="$dir" output -raw storage_host)
(cd "$root" && go build -trimpath -o "$out/artifactweave" ./cmd/artifactweave)
cd "$root"
ARTIFACTWEAVE_SCENARIO="$name" ARTIFACTWEAVE_BIN="$out/artifactweave" \
  ARTIFACTWEAVE_SCENARIO_CONFIG="$out/blob.yaml" ARTIFACTWEAVE_SCENARIO_STORAGE_HOST="$host" \
  ARTIFACTWEAVE_SCENARIO_OUTPUTS="$out/outputs.json" \
  go test -count=1 -v -timeout 75m ./test/platform/...
