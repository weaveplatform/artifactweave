#!/usr/bin/env bash
# Makes the CloudFront key pair a CDN scenario signs URLs with, outside
# Terraform, so only the public half ever reaches a state (decision 0030,
# Keys):
#
#   cloudfront-key.sh <dir>
#
# writes <dir>/cloudfront-private-key.pem (mode 600) and
# <dir>/cloudfront-public-key.pem, an RSA 2048 pair, unless both exist
# already. scenario-ci.sh calls it with a fresh directory for each instance;
# lab-vars.sh with the scenario's .secrets/cloudfront/, so an operator's
# instance keeps its key across applies. Nothing is printed: the private key
# never reaches a log.
set -euo pipefail

dir=${1:?usage: cloudfront-key.sh <dir>}
private=$dir/cloudfront-private-key.pem
public=$dir/cloudfront-public-key.pem
[ -s "$private" ] && [ -s "$public" ] && exit 0

mkdir -p "$dir"
chmod 700 "$dir"
(
  umask 077
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$private" 2>/dev/null
)
openssl pkey -in "$private" -pubout -out "$public" 2>/dev/null
