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
#                               instance named RUN_TAG-<step> remains
#
# Environment: CLOUD (gcp, azure, aws or multicloud), SCENARIO,
# INSTANCE, TTL_MINUTES, RUN_TAG and REAP_PREFIX as the action needs them, and the
# PLATFORM_* repository variables the bootstraps set.
#
# Each instance's state lives at scenarios/<cloud>/<scenario>/<instance> in
# the bootstrap's state store (a multicloud instance's in GCP's), and every
# action works on a fresh copy of the scenario directory with a generated
# backend: nothing carries over between invocations except that state, so a
# destroy finds what an earlier, separate apply left, exactly as a later
# dispatch does. The scenario's ci-vars.sh supplies the Terraform variables a
# CI run passes, and scenario_env what it names beyond the PLATFORM_*
# variables (a CDN key pair, a zone's name).
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
# unlabelled GCP load-balancer pieces. A multicloud scenario has an S3 bucket
# too, so it takes AWS's.
#
# STORE is the cloud whose bootstrap holds the state. A multicloud
# scenario's lives in GCP's state bucket, and its resources are found on all
# three clouds (CLOUDS).
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
multicloud)
  : "${PLATFORM_GCP_STATE_BUCKET:?}" "${PLATFORM_GCP_PROJECT:?}" \
    "${PLATFORM_AZURE_RESOURCE_GROUP:?}" "${PLATFORM_AWS_REGION:?}"
  NAME_PREFIX=artifactweave-awscn-
  ;;
*)
  echo "unknown cloud: $CLOUD" >&2
  exit 1
  ;;
esac
STORE=$CLOUD
CLOUDS=$CLOUD
if [ "$CLOUD" = multicloud ]; then
  STORE=gcp
  CLOUDS="gcp azure aws"
fi

# multicloud lists the scenarios under multicloud/, each with a space either
# side. Their resources sit on every cloud, so a multicloud reap sweeps them
# and a single cloud's reap leaves them alone (ours).
multicloud=" "
for d in "$here"/multicloud/*/; do
  [ -d "$d" ] && multicloud+="$(basename "$d") "
done

ours() { # scenario label
  if [ "$CLOUD" = multicloud ]; then [[ $multicloud == *" $1 "* ]]; else [[ $multicloud != *" $1 "* ]]; fi
}

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
  local work="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/scenario-$CLOUD-$scenario-$instance"
  dir="$work/$CLOUD/$scenario"
  rm -rf "$work"
  mkdir -p "$dir"
  scenario_env "$scenario" "$work/secrets"
  # shellcheck source=/dev/null
  . "$here/$CLOUD/$scenario/ci-vars.sh"
  cp -R "$here/modules" "$work/modules"
  cp -R "$here/$CLOUD/$scenario/." "$dir/"
  rm -rf "$dir/.terraform" "$dir"/terraform.tfstate* "$dir/.local"
  local key
  key=$(state_key "$scenario" "$instance")
  case $STORE in
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

# scenario_env exports what a scenario's ci-vars.sh names beyond the PLATFORM_*
# variables, made for this instance alone:
#
#   CLOUDFRONT_PUBLIC_KEY, CLOUDFRONT_PRIVATE_KEY_FILE
#       a fresh key pair in <secrets> (cloudfront-key.sh), generated outside
#       Terraform so the private half never reaches a state or a log;
#       artifactweave reads it from the file. A destroy gets a pair of its
#       own, which it never uses.
#   PLATFORM_AWS_DNS_ZONE_NAME
#       the name of the bootstrap's zone, PLATFORM_AWS_DNS_ZONE_ID, for a
#       scenario that gives its instance a host there
scenario_env() { # scenario secrets-dir
  local vars=$here/$CLOUD/$1/ci-vars.sh
  if grep -q CLOUDFRONT_ "$vars"; then
    "$root/deploy/ci/cloudfront-key.sh" "$2"
    CLOUDFRONT_PUBLIC_KEY=$(cat "$2/cloudfront-public-key.pem")
    CLOUDFRONT_PRIVATE_KEY_FILE=$2/cloudfront-private-key.pem
    export CLOUDFRONT_PUBLIC_KEY CLOUDFRONT_PRIVATE_KEY_FILE
  fi
  if grep -q PLATFORM_AWS_DNS_ZONE_NAME "$vars"; then
    PLATFORM_AWS_DNS_ZONE_NAME=$(aws_zone_name)
    export PLATFORM_AWS_DNS_ZONE_NAME
  fi
}

# aws_zone_name prints the bootstrap zone's name without its final dot.
aws_zone_name() {
  : "${PLATFORM_AWS_DNS_ZONE_ID:?PLATFORM_AWS_DNS_ZONE_ID is required}"
  local name
  name=$(aws route53 get-hosted-zone --id "$PLATFORM_AWS_DNS_ZONE_ID" --query HostedZone.Name --output text)
  echo "${name%.}"
}

delete_state() { # scenario instance
  local key
  key=$(state_key "$1" "$2")
  case $STORE in
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
  case $STORE in
  gcp)
    { gcloud storage ls "gs://$PLATFORM_GCP_STATE_BUCKET/scenarios/$CLOUD/**" 2>/dev/null || true; } |
      sed -n "s#^gs://$PLATFORM_GCP_STATE_BUCKET/scenarios/$CLOUD/\([^/]*\)/\([^/]*\)/default\.tfstate\$#\1 \2#p"
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
  case $STORE in
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

# aws_tagged_buckets prints
# "<bucket>\t<expires-at>\t<instance>\t<region>\t<scenario>" for every bucket
# a scenario run tagged, in any region: a scenario may span several
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
      [$n, ($t["expires-at"] // ""), ($t.instance // ""), $r, $t["artifactweave-scenario"]] | @tsv' <<<"$tags"
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

# The CDN scenario (aws/cdn) adds CloudFront and certificate pieces. The
# distribution and the certificate take tags, as buckets do; the origin
# access control, cache policy, key group and public keys take none, and are
# known by their names, which start with NAME_PREFIX, the instance and its
# eight-hex suffix; the records are the instance's host, cdn-<instance>, in
# the bootstrap's zone. A disabled distribution costs nothing, but CloudFront
# allows few cache policies, key groups and public keys in an account, so a
# lost state must not leave them behind.

# aws_cdn_tagged prints "<kind>\t<id>\t<expires-at>\t<instance>" for every
# CloudFront distribution and us-east-1 ACM certificate a scenario run
# tagged. Both are listed directly rather than through the tagging API,
# which can report a resource for a while after its delete.
aws_cdn_tagged() {
  local id arn tags
  while IFS=$'\t' read -r id arn; do
    [ -n "$id" ] && [ "$id" != None ] || continue
    tags=$(aws cloudfront list-tags-for-resource --resource "$arn" --output json)
    jq -r --arg id "$id" '(.Tags.Items // [] | from_entries) as $t |
      select($t["artifactweave-scenario"] != null) |
      ["distribution", $id, ($t["expires-at"] // ""), ($t.instance // "")] | @tsv' <<<"$tags"
  done < <(aws cloudfront list-distributions --query 'DistributionList.Items[].[Id, ARN]' --output text)
  while read -r arn; do
    [ -n "$arn" ] && [ "$arn" != None ] || continue
    tags=$(aws acm list-tags-for-certificate --region us-east-1 --certificate-arn "$arn" --output json)
    jq -r --arg arn "$arn" '(.Tags // [] | from_entries) as $t |
      select($t["artifactweave-scenario"] != null) |
      ["certificate", $arn, ($t["expires-at"] // ""), ($t.instance // "")] | @tsv' <<<"$tags"
  done < <(aws acm list-certificates --region us-east-1 \
    --certificate-statuses PENDING_VALIDATION ISSUED INACTIVE EXPIRED VALIDATION_TIMED_OUT REVOKED FAILED \
    --includes keyTypes=RSA_1024,RSA_2048,RSA_3072,RSA_4096,EC_prime256v1,EC_secp384r1,EC_secp521r1 \
    --query 'CertificateSummaryList[].CertificateArn' --output text | tr '\t' '\n')
}

# aws_cdn_pieces prints "<kind>\t<id>\t<instance>\t<created>" for every
# CloudFront key group, public key, cache policy and origin access control
# whose name starts with NAME_PREFIX, in the order they can be deleted.
# created is when it last changed, in epoch seconds, or empty for an origin
# access control, which records no time.
aws_cdn_pieces() {
  local kind id name when
  while IFS=$'\t' read -r kind id name when; do
    [ -n "$id" ] || continue
    name=${name#"$NAME_PREFIX"}
    # <instance>-<8 hex>[-<key name>-<random>]
    printf '%s\t%s\t%s\t%s\n' "$kind" "$id" "$(sed -E 's/-[0-9a-f]{8}(-.*)?$//' <<<"$name")" \
      "${when:+$(date -d "$when" +%s)}"
  done < <(
    aws cloudfront list-key-groups --output json | jq -r --arg p "$NAME_PREFIX" '.KeyGroupList.Items[]? |
      select(.KeyGroup.KeyGroupConfig.Name | startswith($p)) |
      ["key-group", .KeyGroup.Id, .KeyGroup.KeyGroupConfig.Name, .KeyGroup.LastModifiedTime] | @tsv'
    aws cloudfront list-public-keys --output json | jq -r --arg p "$NAME_PREFIX" '.PublicKeyList.Items[]? |
      select(.Name | startswith($p)) | ["public-key", .Id, .Name, .CreatedTime] | @tsv'
    aws cloudfront list-cache-policies --type custom --output json | jq -r --arg p "$NAME_PREFIX" '.CachePolicyList.Items[]? |
      select(.CachePolicy.CachePolicyConfig.Name | startswith($p)) |
      ["cache-policy", .CachePolicy.Id, .CachePolicy.CachePolicyConfig.Name, .CachePolicy.LastModifiedTime] | @tsv'
    aws cloudfront list-origin-access-controls --output json | jq -r --arg p "$NAME_PREFIX" '.OriginAccessControlList.Items[]? |
      select(.Name | startswith($p)) | ["origin-access-control", .Id, .Name, ""] | @tsv'
  )
}

# aws_cdn_records prints "<instance>\t<record set as JSON>" for every record
# of an instance's host, cdn-<instance>, and its certificate's validation
# record, _<token>.cdn-<instance>, in the bootstrap's zone.
aws_cdn_records() {
  [ -n "${PLATFORM_AWS_DNS_ZONE_ID:-}" ] || return 0
  aws route53 list-resource-record-sets --hosted-zone-id "$PLATFORM_AWS_DNS_ZONE_ID" --output json |
    jq -r '.ResourceRecordSets[] | (.Name | sub("^_[^.]*\\."; "")) as $host |
      select($host | startswith("cdn-")) |
      [($host | ltrimstr("cdn-") | split(".")[0]), tojson] | @tsv'
}

# aws_delete_distribution disables a distribution, waits until every edge
# location has the change, which CloudFront requires before a delete, then
# deletes it.
aws_delete_distribution() { # id
  local config etag tmp
  config=$(aws cloudfront get-distribution-config --id "$1" --output json)
  if [ "$(jq -r .DistributionConfig.Enabled <<<"$config")" = true ]; then
    tmp=$(mktemp)
    jq '.DistributionConfig | .Enabled = false' <<<"$config" >"$tmp"
    aws cloudfront update-distribution --id "$1" --if-match "$(jq -r .ETag <<<"$config")" \
      --distribution-config "file://$tmp" >/dev/null
    rm -f "$tmp"
  fi
  aws cloudfront wait distribution-deployed --id "$1"
  etag=$(aws cloudfront get-distribution --id "$1" --query ETag --output text)
  aws cloudfront delete-distribution --id "$1" --if-match "$etag"
}

# aws_delete_piece deletes a key group, public key, cache policy or origin
# access control. CloudFront refuses while something still uses it.
aws_delete_piece() { # kind id
  local etag
  etag=$(aws cloudfront "get-$1" --id "$2" --query ETag --output text)
  aws cloudfront "delete-$1" --id "$2" --if-match "$etag"
}

aws_delete_record() { # record set as JSON
  aws route53 change-resource-record-sets --hosted-zone-id "$PLATFORM_AWS_DNS_ZONE_ID" \
    --change-batch "$(jq -c '{Changes: [{Action: "DELETE", ResourceRecordSet: .}]}' <<<"$1")" >/dev/null
}

# reap_aws_cdn deletes the CloudFront and certificate pieces of instances
# that have no state, in the order CloudFront allows: distributions, then
# certificates, then what they used, then the records. An instance's tagged
# pieces go once its expires-at has passed; its untagged ones once they are
# orphan_age old, so a run still applying keeps them. An instance with a
# tagged piece not yet expired, or an untagged one too young, keeps all of
# its pieces, the origin access control and records included, which record
# no time of their own. It runs inside reap's sweep of AWS, counting failures
# in reap's failed and reading its live and now.
reap_aws_cdn() {
  local keep=" " kind id exp inst created record ok
  while IFS=$'\t' read -r kind id exp inst; do
    [ -n "$id" ] || continue
    in_scope "$inst" || continue
    # A distribution kept, or one whose delete failed, keeps its certificate.
    if [[ $live$keep == *" $inst "* ]]; then continue; fi
    if [ -z "$exp" ] || [ "$exp" -gt "$now" ]; then
      keep+="$inst "
      continue
    fi
    ok=
    case $kind in
    distribution) aws_delete_distribution "$id" && ok=1 ;;
    certificate) aws acm delete-certificate --region us-east-1 --certificate-arn "$id" && ok=1 ;;
    esac
    if [ -n "$ok" ]; then
      summary "Deleted orphaned $kind \`${id##*/}\` (instance \`$inst\`, expired $(utc "$exp"))."
    else
      failed=$((failed + 1))
      keep+="$inst "
    fi
  done < <(aws_cdn_tagged | sort -k1,1r) # distributions before the certificates they use
  while IFS=$'\t' read -r kind id inst created; do
    [ -n "$id" ] || continue
    in_scope "$inst" || continue
    if [[ $live$keep == *" $inst "* ]]; then continue; fi
    if [ -n "$created" ] && [ $((now - created)) -lt "$orphan_age" ]; then
      keep+="$inst "
      continue
    fi
    if aws_delete_piece "$kind" "$id"; then
      summary "Deleted orphaned CloudFront $kind \`$id\` (instance \`$inst\`)."
    else
      failed=$((failed + 1))
    fi
  done < <(aws_cdn_pieces)
  while IFS=$'\t' read -r inst record; do
    [ -n "$inst" ] || continue
    in_scope "$inst" || continue
    if [[ $live$keep == *" $inst "* ]]; then continue; fi
    if aws_delete_record "$record"; then
      summary "Deleted orphaned record \`$(jq -r .Name <<<"$record")\` ($(jq -r .Type <<<"$record"), instance \`$inst\`)."
    else
      failed=$((failed + 1))
    fi
  done < <(aws_cdn_records)
}

# in_scope is true when an instance belongs to this reap: every instance,
# unless REAP_PREFIX narrows it to one run's (a pull request proof reaps only
# its own, so parallel proofs never race to destroy the same state).
in_scope() { [ -z "${REAP_PREFIX:-}" ] || [[ $1 == "$REAP_PREFIX"* ]]; }

# sweep deletes what outlived its state on one cloud: labelled resources once
# expired, prefixed (unlabelled) ones once old enough, never a live
# instance's, and only those of scenarios this reap owns (ours). It reads
# reap's live and counts into its failed.
sweep() { # cloud
  local name inst exp created scenario
  case $1 in
  gcp)
    while IFS=$'\t' read -r name exp inst scenario; do
      [ -n "$name" ] || continue
      in_scope "$inst" || continue
      ours "$scenario" || continue
      if [[ $live != *" $inst "* ]] && [ -n "$exp" ] && [ "$exp" -le "$now" ]; then
        if gcloud storage rm -r --quiet "gs://$name"; then
          summary "Deleted orphaned bucket \`$name\` (instance \`$inst\`, expired $(utc "$exp"))."
        else
          failed=$((failed + 1))
        fi
      fi
    done < <(gcloud storage buckets list --project "$PLATFORM_GCP_PROJECT" --format=json |
      jq -r '.[] | select(.labels["artifactweave-scenario"] != null) | [.name, (.labels["expires-at"] // ""), (.labels.instance // ""), .labels["artifactweave-scenario"]] | @tsv')
    # Load-balancer pieces take no labels; delete them in dependency order.
    # Only GCP's own scenarios make them: a multicloud scenario makes none,
    # and its names lack GCP's prefix.
    [ "$CLOUD" = gcp ] || return 0
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
    while IFS=$'\t' read -r id exp inst scenario; do
      [ -n "$id" ] || continue
      in_scope "$inst" || continue
      ours "$scenario" || continue
      if [[ $live != *" $inst "* ]] && [ -n "$exp" ] && [ "$exp" -le "$now" ]; then
        if az resource delete --ids "$id" --only-show-errors; then
          summary "Deleted orphaned resource \`${id##*/}\` (instance \`$inst\`, expired $(utc "$exp"))."
        else
          failed=$((failed + 1))
        fi
      fi
    done < <(az resource list --resource-group "$PLATFORM_AZURE_RESOURCE_GROUP" --only-show-errors \
      --query '[?tags."artifactweave-scenario" != null].[id, tags."expires-at", tags.instance, tags."artifactweave-scenario"]' -o tsv)
    ;;
  aws)
    local region
    while IFS=$'\t' read -r name exp inst region scenario; do
      [ -n "$name" ] || continue
      in_scope "$inst" || continue
      ours "$scenario" || continue
      if [[ $live != *" $inst "* ]] && [ -n "$exp" ] && [ "$exp" -le "$now" ]; then
        if aws_delete_bucket "$name" "$region"; then
          summary "Deleted orphaned bucket \`$name\` (instance \`$inst\`, expired $(utc "$exp"))."
        else
          failed=$((failed + 1))
        fi
      fi
    done < <(aws_tagged_buckets)
    # CloudFront and certificate pieces come from aws/cdn alone. A multicloud
    # reap leaves them be: it cannot see aws/cdn's states, and shares its
    # name prefix, so it would take a live instance's untagged pieces.
    [ "$CLOUD" = aws ] || return 0
    reap_aws_cdn
    ;;
  esac
}

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

  # Then what outlived its state, on every cloud the scenarios use.
  local c
  for c in $CLOUDS; do sweep "$c"; done

  summary "Reap of \`$CLOUD\`${REAP_PREFIX:+ (instances starting \`$REAP_PREFIX\`)}: $reaped expired instance(s) destroyed, $failed failure(s); live:${live% }."
  [ "$failed" = 0 ]
}

# check_clean fails if anything of an instance of the run remains: a state, a
# labelled bucket or resource. A proof names its instances RUN_TAG-<step>,
# and the hyphen keeps one proof's tag from matching another's that it
# begins (a mirror's ...-m and a multi-region's ...-mr in the same run).
check_clean() {
  : "${RUN_TAG:?RUN_TAG is required}"
  local left=0 scenario instance
  while read -r scenario instance; do
    if [[ $instance == "$RUN_TAG"-* ]]; then
      summary "**Left behind:** the state of \`$CLOUD/$scenario\` instance \`$instance\`."
      left=1
    fi
  done < <(states)
  local c
  for c in $CLOUDS; do leftovers "$c"; done
  if [ "$left" = 0 ]; then
    summary "Nothing of run \`$RUN_TAG\` remains in \`$CLOUD\`."
  fi
  [ "$left" = 0 ]
}

# leftovers reports the labelled buckets or resources of the run's instances
# on one cloud, setting check_clean's left.
leftovers() { # cloud
  local line
  case $1 in
  gcp)
    while read -r line; do
      [ -n "$line" ] || continue
      summary "**Left behind:** bucket \`$line\`."
      left=1
    done < <(gcloud storage buckets list --project "$PLATFORM_GCP_PROJECT" --format=json |
      jq -r --arg tag "$RUN_TAG-" '.[] | select((.labels.instance // "") | startswith($tag)) | .name')
    ;;
  azure)
    while read -r line; do
      [ -n "$line" ] || continue
      summary "**Left behind:** resource \`$line\`."
      left=1
    done < <(az resource list --resource-group "$PLATFORM_AZURE_RESOURCE_GROUP" --only-show-errors -o json |
      jq -r --arg tag "$RUN_TAG-" '.[] | select((.tags.instance // "") | startswith($tag)) | .name')
    ;;
  aws)
    local exp inst region
    while IFS=$'\t' read -r line exp inst region _; do
      [ -n "$line" ] || continue
      [[ $inst == "$RUN_TAG"-* ]] || continue
      summary "**Left behind:** bucket \`$line\` ($region)."
      left=1
    done < <(aws_tagged_buckets)
    # aws/cdn's CloudFront and certificate pieces, which no other scenario makes.
    [ "$CLOUD" = aws ] || return 0
    local kind id
    while IFS=$'\t' read -r kind id inst; do
      [[ $inst == "$RUN_TAG"-* ]] || continue
      summary "**Left behind:** $kind \`${id##*/}\`."
      left=1
    done < <(aws_cdn_tagged | cut -f1,2,4)
    while IFS=$'\t' read -r kind id inst; do
      [[ $inst == "$RUN_TAG"-* ]] || continue
      summary "**Left behind:** CloudFront $kind \`$id\`."
      left=1
    done < <(aws_cdn_pieces | cut -f1-3)
    while IFS=$'\t' read -r inst line; do
      [[ $inst == "$RUN_TAG"-* ]] || continue
      summary "**Left behind:** record \`$(jq -r .Name <<<"$line")\` ($(jq -r .Type <<<"$line"))."
      left=1
    done < <(aws_cdn_records)
    ;;
  esac
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
