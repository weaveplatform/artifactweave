#!/usr/bin/env bash
# The Cloud CDN signed URL key a gcp/cdn scenario signs with, kept outside
# Terraform: google_compute_backend_bucket_signed_url_key would store it in
# the state as plain text (decision 0030, Keys).
#
#   cloud-cdn-key.sh make <dir>
#       write <dir>/cloud-cdn-key (mode 600), 16 random bytes in base64url
#       as gcloud reads them, unless it exists already
#   cloud-cdn-key.sh add <key file> <project> <backend bucket> <key name>
#       add the key to the backend bucket, unless it holds a key of that
#       name already
#   cloud-cdn-key.sh has <project> <backend bucket> <key name>
#       succeed when the backend bucket holds a key of that name
#
# scenario-ci.sh makes a key for each instance and adds it after the first
# apply; lab-vars.sh makes one in the scenario's .secrets/cloud-cdn/, which
# make signed-url-key adds. Nothing is printed: the key never reaches a log.
set -euo pipefail

usage() {
  echo "usage: cloud-cdn-key.sh make <dir> | add <key file> <project> <backend bucket> <key name> | has <project> <backend bucket> <key name>" >&2
  exit 2
}

has() { # project backend-bucket key-name
  gcloud compute backend-buckets describe "$2" --project "$1" --format=json |
    jq -e --arg k "$3" '(.cdnPolicy.signedUrlKeyNames // []) | index($k) != null' >/dev/null
}

case ${1:-} in
make)
  [ $# = 2 ] || usage
  key=$2/cloud-cdn-key
  [ -s "$key" ] && exit 0
  mkdir -p "$2"
  chmod 700 "$2"
  (
    umask 077
    # Google's own recipe: padded base64 in the URL-safe alphabet.
    head -c 16 /dev/urandom | base64 | tr +/ -_ >"$key"
  )
  ;;
add)
  [ $# = 5 ] || usage
  [ -s "$2" ] || {
    echo "cloud-cdn-key.sh: no key at $2" >&2
    exit 1
  }
  has "$3" "$4" "$5" && exit 0
  gcloud compute backend-buckets add-signed-url-key "$4" --project "$3" \
    --key-name "$5" --key-file "$2" --quiet
  ;;
has)
  [ $# = 4 ] || usage
  has "$2" "$3" "$4"
  ;;
*) usage ;;
esac
