#!/usr/bin/env bash
# Runs one action of a reference-deployment scenario as the deployer identity
# the bootstraps trust (platform-scenarios.yml, decision 0030).
#
#   scenario-ci.sh apply        apply CLOUD/SCENARIO as INSTANCE, labelled to
#                               expire TTL_MINUTES from now
#   scenario-ci.sh test         apply, then start artifactweave against the
#                               scenario and run test/platform; the workflow
#                               destroys in a step that always runs
#   scenario-ci.sh destroy      destroy INSTANCE from its remote state, then
#                               delete the emptied state
#   scenario-ci.sh reap         destroy every CLOUD instance whose expires-at
#                               has passed, then delete labelled or prefixed
#                               resources that outlived their state; with
#                               REAP_PREFIX, only instances starting with it
#   scenario-ci.sh check-clean  fail if any state or labelled resource of an
#                               instance starting with RUN_TAG remains
#
# Environment: CLOUD (gcp, azure or aws), SCENARIO,
# INSTANCE, TTL_MINUTES, RUN_TAG and REAP_PREFIX as the action needs them, and the
# PLATFORM_* repository variables the bootstraps set.
#
# Each instance's state lives at scenarios/<cloud>/<scenario>/<instance> in
# the bootstrap's state store, and every action works on a fresh copy of the
# scenario directory with a generated backend: nothing carries over between
# invocations except that state, so a destroy finds what an earlier, separate
# apply left, exactly as a later dispatch does. The scenario's ci-vars.sh
# supplies the Terraform variables a CI run passes.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
here="$root/deploy/platform/terraform"
action=${1:?usage: scenario-ci.sh apply|test|destroy|reap|check-clean}
: "${CLOUD:?CLOUD is required}"
now=$(date +%s)
# A prefixed resource with no live state is only reaped once it is this old,
# so a run that is still applying, and has not written its state yet, keeps
# its resources.
orphan_age=$((2 * 3600))

summary() {
  echo "$*"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then printf '%s\n\n' "$*" >>"$GITHUB_STEP_SUMMARY"; fi
}

utc() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ; }

# NAME_PREFIX starts every name a scenario run gives its resources: each
# scenario's ci-vars.sh sets TF_VAR_name="${NAME_PREFIX}${INSTANCE}". On AWS
# it is artifactweave-awscn-, because the deployer may manage only
# artifactweave-* buckets; elsewhere awscn-, by which reap finds the
# unlabelled GCP load-balancer pieces.
case $CLOUD in
gcp)
  : "${PLATFORM_GCP_STATE_BUCKET:?}" "${PLATFORM_GCP_PROJECT:?}"
  NAME_PREFIX=awscn-
  ;;
azure)
  : "${PLATFORM_AZURE_STATE_ACCOUNT:?}" "${PLATFORM_AZURE_RESOURCE_GROUP:?}"
  NAME_PREFIX=awscn-
  ;;
aws)
  : "${PLATFORM_AWS_STATE_BUCKET:?}" "${PLATFORM_AWS_REGION:?}"
  NAME_PREFIX=artifactweave-awscn-
  ;;
*)
  echo "unknown cloud: $CLOUD" >&2
  exit 1
  ;;
esac

# Scenario and instance names reach bucket names, labels and state keys.
check_names() { # scenario instance
  if ! [[ $1 =~ ^[a-z][a-z0-9-]{0,30}$ ]] || ! [ -f "$here/$CLOUD/$1/ci-vars.sh" ]; then
    echo "$CLOUD/$1 is not a scenario that runs in CI (no ci-vars.sh)" >&2
    exit 1
  fi
  if ! [[ $2 =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]]; then
    echo "instance '$2' must be 1 to 32 of a-z, 0-9 and -, starting with a letter or digit" >&2
    exit 1
  fi
}

state_key() { echo "scenarios/$CLOUD/$1/$2"; }

# use_instance exports the scenario's CI variables for one instance and
# prepares a fresh, initialised copy of it in $dir. The copy keeps the
# scenario's place beside modules/, which its module sources reach as
# ../../modules.
use_instance() { # scenario instance expires-at
  local scenario=$1 instance=$2
  SCENARIO_LABELS=$(printf '{"artifactweave-scenario":"%s","instance":"%s","expires-at":"%s"}' "$scenario" "$instance" "$3")
  INSTANCE=$instance
  export SCENARIO_LABELS INSTANCE
  # shellcheck source=/dev/null
  . "$here/$CLOUD/$scenario/ci-vars.sh"
  local work="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/scenario-$CLOUD-$scenario-$instance"
  dir="$work/$CLOUD/$scenario"
  rm -rf "$work"
  mkdir -p "$dir"
  cp -R "$here/modules" "$work/modules"
  cp -R "$here/$CLOUD/$scenario/." "$dir/"
  rm -rf "$dir/.terraform" "$dir"/terraform.tfstate* "$dir/.local"
  local key
  key=$(state_key "$scenario" "$instance")
  case $CLOUD in
  gcp)
    printf 'terraform {\n  backend "gcs" {}\n}\n' >"$dir/ci_backend.tf"
    terraform -chdir="$dir" init -input=false -no-color \
      -backend-config="bucket=$PLATFORM_GCP_STATE_BUCKET" -backend-config="prefix=$key"
    ;;
  azure)
    printf 'terraform {\n  backend "azurerm" {}\n}\n' >"$dir/ci_backend.tf"
    terraform -chdir="$dir" init -input=false -no-color \
      -backend-config="storage_account_name=$PLATFORM_AZURE_STATE_ACCOUNT" \
      -backend-config="container_name=tfstate" -backend-config="key=$key" \
      -backend-config="use_azuread_auth=true"
    ;;
  aws)
    printf 'terraform {\n  backend "s3" {}\n}\n' >"$dir/ci_backend.tf"
    terraform -chdir="$dir" init -input=false -no-color \
      -backend-config="bucket=$PLATFORM_AWS_STATE_BUCKET" -backend-config="key=$key" \
      -backend-config="region=$PLATFORM_AWS_REGION" -backend-config="encrypt=true" \
      -backend-config="use_lockfile=true"
    ;;
  esac
}

delete_state() { # scenario instance
  local key
  key=$(state_key "$1" "$2")
  case $CLOUD in
  gcp) gcloud storage rm --quiet "gs://$PLATFORM_GCP_STATE_BUCKET/$key/default.tfstate" 2>/dev/null || true ;;
  azure)
    az storage blob delete --account-name "$PLATFORM_AZURE_STATE_ACCOUNT" --container-name tfstate \
      --name "$key" --auth-mode login --only-show-errors 2>/dev/null || true
    ;;
  aws)
    aws s3 rm --only-show-errors "s3://$PLATFORM_AWS_STATE_BUCKET/$key" 2>/dev/null || true
    aws s3 rm --only-show-errors "s3://$PLATFORM_AWS_STATE_BUCKET/$key.tflock" 2>/dev/null || true
    ;;
  esac
}

apply() {
  : "${SCENARIO:?SCENARIO is required}" "${INSTANCE:?INSTANCE is required}"
  check_names "$SCENARIO" "$INSTANCE"
  local ttl=${TTL_MINUTES:-240}
  [[ $ttl =~ ^-?[0-9]+$ ]] || {
    echo "TTL_MINUTES must be a whole number of minutes" >&2
    exit 1
  }
  local expires=$((now + ttl * 60))
  use_instance "$SCENARIO" "$INSTANCE" "$expires"
  terraform -chdir="$dir" apply -input=false -no-color -auto-approve
  summary "Applied \`$CLOUD/$SCENARIO\` instance \`$INSTANCE\`, expires-at $expires ($(utc "$expires"))."
}

test_scenario() {
  apply
  # platform-tests.sh is shared with local.sh verify, so a workstation runs
  # exactly this check against its own applied scenario.
  "$root/deploy/ci/platform-tests.sh" "$dir" "$CLOUD/$SCENARIO" \
    "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/scenario-run-$INSTANCE"
  local host
  host=$(terraform -chdir="$dir" output -raw storage_host)
  summary "test/platform passed against \`$CLOUD/$SCENARIO\` instance \`$INSTANCE\` (downloads redirected to \`$host\`)."
}

# state_resources prints how many managed resources an instance's state
# holds: 0 when the state is empty or there is none. It reads the state store
# directly, so it answers even when the scenario's configuration cannot
# initialise, and fails when a state exists but cannot be read.
state_resources() { # scenario instance
  local json
  if json=$(state_json "$1" "$2" 2>/dev/null) && [ -n "$json" ]; then
    jq '[.resources[]? | select(.mode == "managed")] | length' <<<"$json"
  elif states | grep -qx "$1 $2"; then
    echo "the state of $CLOUD/$1 instance $2 exists but cannot be read" >&2
    return 1
  else
    echo 0
  fi
}

# destroy_one runs in its own process (reap calls it as "$0" destroy-one), so
# set -e holds: a failed destroy must never go on to delete the state. An
# instance that never applied (its state empty or absent) needs no
# configuration: it succeeds and removes the empty state, so a run whose
# apply failed early still cleans up after itself.
destroy_one() { # scenario instance
  check_names "$1" "$2"
  local managed
  managed=$(state_resources "$1" "$2")
  if [ "$managed" = 0 ]; then
    summary "No resources in the state of \`$CLOUD/$1\` instance \`$2\`; nothing to destroy."
  else
    use_instance "$1" "$2" "$now"
    terraform -chdir="$dir" destroy -input=false -no-color -auto-approve
    summary "Destroyed \`$CLOUD/$1\` instance \`$2\`."
  fi
  delete_state "$1" "$2"
}

# states prints "<scenario> <instance>" for every scenario state of CLOUD.
states() {
  case $CLOUD in
  gcp)
    { gcloud storage ls "gs://$PLATFORM_GCP_STATE_BUCKET/scenarios/gcp/**" 2>/dev/null || true; } |
      sed -n "s#^gs://$PLATFORM_GCP_STATE_BUCKET/scenarios/gcp/\([^/]*\)/\([^/]*\)/default\.tfstate\$#\1 \2#p"
    ;;
  azure)
    az storage blob list --account-name "$PLATFORM_AZURE_STATE_ACCOUNT" --container-name tfstate \
      --prefix scenarios/azure/ --auth-mode login --only-show-errors --query '[].name' -o tsv |
      sed -n 's#^scenarios/azure/\([^/]*\)/\([^/]*\)$#\1 \2#p'
    ;;
  aws)
    # The S3 backend keeps a key.tflock beside each state while it is locked.
    aws s3api list-objects-v2 --bucket "$PLATFORM_AWS_STATE_BUCKET" --prefix scenarios/aws/ \
      --query 'Contents[].Key' --output text | tr '\t' '\n' |
      sed -n 's#^scenarios/aws/\([^/]*\)/\([^/.]*\)$#\1 \2#p'
    ;;
  esac
}

state_json() { # scenario instance
  local key
  key=$(state_key "$1" "$2")
  case $CLOUD in
  gcp) gcloud storage cat "gs://$PLATFORM_GCP_STATE_BUCKET/$key/default.tfstate" ;;
  azure)
    local tmp
    tmp=$(mktemp)
    az storage blob download --account-name "$PLATFORM_AZURE_STATE_ACCOUNT" --container-name tfstate \
      --name "$key" --file "$tmp" --auth-mode login --only-show-errors --no-progress >/dev/null
    cat "$tmp"
    rm -f "$tmp"
    ;;
  aws) aws s3 cp --only-show-errors "s3://$PLATFORM_AWS_STATE_BUCKET/$key" - ;;
  esac
}

# aws_tagged_buckets prints "<bucket>\t<expires-at>\t<instance>\t<region>"
# for every bucket a scenario run tagged, in any region: a scenario may span several
# (multi-region), and the tagging API only sees the region it is asked in.
# It lists the deployer's artifactweave- buckets with their regions and reads
# each one's tags there.
aws_tagged_buckets() {
  local name region tags
  while IFS=$'\t' read -r name region; do
    [ -n "$name" ] || continue
    if [ -z "$region" ] || [ "$region" = None ]; then
      region=$(aws s3api get-bucket-location --bucket "$name" --query LocationConstraint --output text)
      [ "$region" != None ] || region=us-east-1
    fi
    # A bucket with no tags answers NoSuchTagSet; it is not a scenario's.
    tags=$(aws s3api get-bucket-tagging --bucket "$name" --region "$region" --output json 2>/dev/null) || continue
    jq -r --arg n "$name" --arg r "$region" '(.TagSet | from_entries) as $t |
      select($t["artifactweave-scenario"] != null) |
      [$n, ($t["expires-at"] // ""), ($t.instance // ""), $r] | @tsv' <<<"$tags"
  done < <(aws s3api list-buckets --prefix artifactweave- --query 'Buckets[].[Name, BucketRegion]' --output text)
}

# aws_delete_bucket empties a bucket of every object version and delete
# marker, which s3 rb --force leaves behind in a versioned bucket (the
# geo-replicated scenario's, which replication needs versioned), then
# deletes it.
aws_delete_bucket() { # bucket region
  local batch
  while :; do
    batch=$(aws s3api list-object-versions --bucket "$1" --region "$2" --max-items 1000 --output json |
      jq -c '{Objects: ([.Versions[]?, .DeleteMarkers[]?] | map({Key, VersionId})), Quiet: true}')
    [ "$(jq '.Objects | length' <<<"$batch")" -gt 0 ] || break
    aws s3api delete-objects --bucket "$1" --region "$2" --delete "$batch" >/dev/null
  done
  aws s3 rb --force "s3://$1" --region "$2" >/dev/null
}

# in_scope is true when an instance belongs to this reap: every instance,
# unless REAP_PREFIX narrows it to one run's (a pull request proof reaps only
# its own, so parallel proofs never race to destroy the same state).
in_scope() { [ -z "${REAP_PREFIX:-}" ] || [[ $1 == "$REAP_PREFIX"* ]]; }

reap() {
  local failed=0 reaped=0 live=" " scenario instance json expires resources
  while read -r scenario instance; do
    [ -n "$scenario" ] || continue
    in_scope "$instance" || continue
    json=$(state_json "$scenario" "$instance")
    expires=$(jq -r '.outputs.expires_at.value // ""' <<<"$json")
    resources=$(jq '[.resources[]? | select(.mode == "managed")] | length' <<<"$json")
    if [ "$resources" = 0 ]; then
      delete_state "$scenario" "$instance"
      summary "Removed the empty state of \`$CLOUD/$scenario\` instance \`$instance\`."
    elif [ -z "$expires" ]; then
      live+="$instance "
      summary "Kept \`$CLOUD/$scenario\` instance \`$instance\`: its state carries no expires-at."
    elif [ "$expires" -le "$now" ]; then
      if "$0" destroy-one "$scenario" "$instance"; then
        reaped=$((reaped + 1))
      else
        failed=$((failed + 1))
        summary "**Failed** to reap \`$CLOUD/$scenario\` instance \`$instance\`; its state is kept for the next reap."
      fi
    else
      live+="$instance "
      summary "Kept \`$CLOUD/$scenario\` instance \`$instance\` until $(utc "$expires")."
    fi
  done < <(states)

  # Resources that outlived their state: labelled ones once expired,
  # prefixed (unlabelled) ones once old enough, never a live instance's.
  local name inst exp created
  case $CLOUD in
  gcp)
    while IFS=$'\t' read -r name exp inst; do
      [ -n "$name" ] || continue
      in_scope "$inst" || continue
      if [[ $live != *" $inst "* ]] && [ -n "$exp" ] && [ "$exp" -le "$now" ]; then
        if gcloud storage rm -r --quiet "gs://$name"; then
          summary "Deleted orphaned bucket \`$name\` (instance \`$inst\`, expired $(utc "$exp"))."
        else
          failed=$((failed + 1))
        fi
      fi
    done < <(gcloud storage buckets list --project "$PLATFORM_GCP_PROJECT" --format=json |
      jq -r '.[] | select(.labels["artifactweave-scenario"] != null) | [.name, (.labels["expires-at"] // ""), (.labels.instance // "")] | @tsv')
    # Load-balancer pieces take no labels; delete them in dependency order.
    local kind scope
    for kind in forwarding-rules target-https-proxies target-http-proxies url-maps backend-buckets ssl-certificates addresses; do
      scope=--global
      [ "$kind" = backend-buckets ] && scope=
      while IFS=$'\t' read -r name created; do
        [ -n "$name" ] || continue
        inst=${name#"$NAME_PREFIX"}
        in_scope "$inst" || continue
        local owned=
        for l in $live; do [[ $inst == "$l"* ]] && owned=1; done
        [ -z "$owned" ] || continue
        [ $((now - $(date -d "$created" +%s))) -ge "$orphan_age" ] || continue
        # shellcheck disable=SC2086 # scope is one flag or nothing
        if gcloud compute "$kind" delete $scope --project "$PLATFORM_GCP_PROJECT" --quiet "$name"; then
          summary "Deleted orphaned $kind \`$name\`."
        else
          failed=$((failed + 1))
        fi
      done < <(
        # shellcheck disable=SC2086
        gcloud compute "$kind" list $scope --project "$PLATFORM_GCP_PROJECT" \
          --filter="name~^$NAME_PREFIX" --format='value(name,creationTimestamp)' 2>/dev/null || true
      )
    done
    ;;
  azure)
    local id
    while IFS=$'\t' read -r id exp inst; do
      [ -n "$id" ] || continue
      in_scope "$inst" || continue
      if [[ $live != *" $inst "* ]] && [ -n "$exp" ] && [ "$exp" -le "$now" ]; then
        if az resource delete --ids "$id" --only-show-errors; then
          summary "Deleted orphaned resource \`${id##*/}\` (instance \`$inst\`, expired $(utc "$exp"))."
        else
          failed=$((failed + 1))
        fi
      fi
    done < <(az resource list --resource-group "$PLATFORM_AZURE_RESOURCE_GROUP" --only-show-errors \
      --query '[?tags."artifactweave-scenario" != null].[id, tags."expires-at", tags.instance]' -o tsv)
    ;;
  aws)
    local region
    while IFS=$'\t' read -r name exp inst region; do
      [ -n "$name" ] || continue
      in_scope "$inst" || continue
      if [[ $live != *" $inst "* ]] && [ -n "$exp" ] && [ "$exp" -le "$now" ]; then
        if aws_delete_bucket "$name" "$region"; then
          summary "Deleted orphaned bucket \`$name\` (instance \`$inst\`, expired $(utc "$exp"))."
        else
          failed=$((failed + 1))
        fi
      fi
    done < <(aws_tagged_buckets)
    ;;
  esac

  summary "Reap of \`$CLOUD\`${REAP_PREFIX:+ (instances starting \`$REAP_PREFIX\`)}: $reaped expired instance(s) destroyed, $failed failure(s); live:${live% }."
  [ "$failed" = 0 ]
}

# check_clean fails if anything of an instance starting with RUN_TAG remains:
# a state, a labelled bucket or resource.
check_clean() {
  : "${RUN_TAG:?RUN_TAG is required}"
  local left=0 scenario instance line
  while read -r scenario instance; do
    if [[ $instance == "$RUN_TAG"* ]]; then
      summary "**Left behind:** the state of \`$CLOUD/$scenario\` instance \`$instance\`."
      left=1
    fi
  done < <(states)
  case $CLOUD in
  gcp)
    while read -r line; do
      [ -n "$line" ] || continue
      summary "**Left behind:** bucket \`$line\`."
      left=1
    done < <(gcloud storage buckets list --project "$PLATFORM_GCP_PROJECT" --format=json |
      jq -r --arg tag "$RUN_TAG" '.[] | select((.labels.instance // "") | startswith($tag)) | .name')
    ;;
  azure)
    while read -r line; do
      [ -n "$line" ] || continue
      summary "**Left behind:** resource \`$line\`."
      left=1
    done < <(az resource list --resource-group "$PLATFORM_AZURE_RESOURCE_GROUP" --only-show-errors -o json |
      jq -r --arg tag "$RUN_TAG" '.[] | select((.tags.instance // "") | startswith($tag)) | .name')
    ;;
  aws)
    local exp inst region
    while IFS=$'\t' read -r line exp inst region; do
      [ -n "$line" ] || continue
      [[ $inst == "$RUN_TAG"* ]] || continue
      summary "**Left behind:** bucket \`$line\` ($region)."
      left=1
    done < <(aws_tagged_buckets)
    ;;
  esac
  if [ "$left" = 0 ]; then
    summary "Nothing of run \`$RUN_TAG\` remains in \`$CLOUD\`."
  fi
  [ "$left" = 0 ]
}

case $action in
apply) apply ;;
test) test_scenario ;;
destroy) "$0" destroy-one "${SCENARIO:?SCENARIO is required}" "${INSTANCE:?INSTANCE is required}" ;;
destroy-one) destroy_one "${2:?}" "${3:?}" ;;
reap) reap ;;
check-clean) check_clean ;;
*)
  echo "unknown action: $action" >&2
  exit 1
  ;;
esac
