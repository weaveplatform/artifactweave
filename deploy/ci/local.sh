#!/usr/bin/env bash
# Runs artifactweave on a workstation against an applied scenario, from the
# scenario's own artifactweave_config output, so the local run uses exactly
# the configuration the reference documents (decision 0030). It works for
# every scenario, mirrors included, because it never writes a configuration
# by hand.
#
#   local.sh render       .local/artifactweave.yaml from artifactweave_config,
#                         .local/outputs.json, and .local/secrets.env (master
#                         key and admin password, generated once and kept:
#                         the database is sealed with that key)
#   local.sh credentials  what each backend in the configuration needs to act
#                         as the scenario's app identity from here (below)
#   local.sh up           build .local/artifactweave and start it in the
#                         background on 127.0.0.1:$PORT, then wait until ready
#   local.sh smoke        a 40 MiB round trip, and a downloadRedirect 302 that
#                         must land on the scenario's storage host
#   local.sh verify       test/platform against the applied scenario, exactly
#                         as CI runs it (platform-tests.sh)
#   local.sh logs | down | clean
#
# Run it from the scenario directory; scenario.mk does, setting CLOUD and
# PORT. Everything it writes is under .local/, which ignores itself.
set -euo pipefail

action=${1:?usage: local.sh render|credentials|up|smoke|verify|logs|down|clean}
: "${CLOUD:?CLOUD is required}"
root=$(git rev-parse --show-toplevel)
terraform=${TERRAFORM:-terraform}
port=${PORT:?PORT is required}
base="http://127.0.0.1:$port"
pidfile=.local/artifactweave.pid
logfile=.local/artifactweave.log

prepare() {
  mkdir -p .local
  # .local/ holds secrets and credentials; ignore it wherever the scenario
  # lives, even in a copy without the repository's ignore rules.
  [ -f .local/.gitignore ] || echo '*' >.local/.gitignore
}

# values_of prints every value of a key in the rendered configuration, one per
# line. Terraform's yamlencode writes one "key": "value" pair per line, which
# is all this needs.
values_of() { # key
  sed -nE 's/^[[:space:]]*"?'"$1"'"?:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' .local/artifactweave.yaml | sort -u
}

render() {
  prepare
  "$terraform" output -raw artifactweave_config >.local/artifactweave.yaml
  "$terraform" output -json >.local/outputs.json
  if [ ! -f .local/secrets.env ]; then
    (
      umask 077
      echo "ARTIFACTWEAVE_MASTER_KEY=v1:$(openssl rand -base64 32)"
      echo "ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -base64 18 | tr -d '/+=')"
    ) >.local/secrets.env
    chmod 600 .local/secrets.env
  fi
  echo "rendered .local/artifactweave.yaml from the scenario's artifactweave_config"
}

# credentials makes this machine act as the identity the scenario grants,
# per backend in the configuration. A production deployment gets the same
# identity from its runtime (instance profile, workload identity, managed
# identity) instead.
credentials() {
  [ -f .local/artifactweave.yaml ] || render
  local backends
  backends=$(values_of backend)
  if grep -qx gcs <<<"$backends"; then
    # Impersonate the app service account from your own Application Default
    # Credentials: the file `gcloud auth application-default login
    # --impersonate-service-account` would write, without replacing your ADC.
    # The lab forbids service account keys.
    local ids adc
    ids=$(values_of google_access_id)
    if [ "$(grep -c . <<<"$ids")" != 1 ]; then
      echo "the configuration names $(grep -c . <<<"$ids") google_access_id values; expected one" >&2
      exit 1
    fi
    adc=${CLOUDSDK_CONFIG:-$HOME/.config/gcloud}/application_default_credentials.json
    [ -f "$adc" ] || {
      echo "run: gcloud auth application-default login" >&2
      exit 1
    }
    (
      umask 077
      python3 -I - "$adc" "$ids" >.local/gcp-credentials.json <<'PY'
import json, sys
source, account = sys.argv[1], sys.argv[2]
json.dump({
    "type": "impersonated_service_account",
    "service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/"
    f"projects/-/serviceAccounts/{account}:generateAccessToken",
    "delegates": [],
    "source_credentials": json.load(open(source)),
}, sys.stdout)
PY
    )
    echo "wrote .local/gcp-credentials.json (impersonating $ids)"
  fi
  if grep -qx s3 <<<"$backends"; then
    # Short-lived credentials from your own `aws login`; rerun when they expire.
    (umask 077 && aws configure export-credentials --format env-no-export >.local/aws.env) || {
      echo "run: aws login" >&2
      exit 1
    }
    echo "wrote .local/aws.env from your aws login session"
  fi
  if grep -qx azure <<<"$backends"; then
    # The configuration's credential is default: the Azure SDK chain reaches
    # your az login, which the scenario grants as its operator.
    echo "azure: nothing to write; artifactweave uses your az login"
  fi
}

# load_env exports the environment artifactweave and test/platform need. It
# sources files rather than a process substitution, which macOS's bash 3.2
# silently reads as empty.
load_env() {
  set -a
  # shellcheck source=/dev/null
  . .local/secrets.env
  # shellcheck source=/dev/null
  [ ! -f .local/aws.env ] || . .local/aws.env
  set +a
  if [ -f .local/gcp-credentials.json ]; then
    export GOOGLE_APPLICATION_CREDENTIALS="$PWD/.local/gcp-credentials.json"
  fi
}

running() { [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; }

up() {
  if running; then
    echo "artifactweave is already running on $base (pid $(cat "$pidfile"))"
    return
  fi
  credentials
  local here=$PWD
  (cd "$root" && GOWORK=off go build -trimpath -o "$here/.local/artifactweave" ./cmd/artifactweave)
  mkdir -p .local/data
  (
    load_env
    nohup .local/artifactweave serve --config .local/artifactweave.yaml \
      --listen "127.0.0.1:$port" --data-dir .local/data --base-url "$base" \
      --log-format json >"$logfile" 2>&1 &
    echo $! >"$pidfile"
  )
  for _ in $(seq 1 60); do
    if curl -fsS "$base/artifactory/api/v1/system/readiness" >/dev/null 2>&1; then
      echo "artifactweave is running on $base (admin password in $PWD/.local/secrets.env, log in $logfile)"
      return
    fi
    running || break
    sleep 1
  done
  echo "artifactweave did not become ready; the end of $logfile:" >&2
  tail -n 40 "$logfile" >&2
  exit 1
}

smoke() (
  running || {
    echo "artifactweave is not running; run make up" >&2
    exit 1
  }
  local password work
  password=$(sed -n 's/^ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=//p' .local/secrets.env)
  local auth=(-u "admin:$password")
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  for repo in smoke smoke-redirect; do
    local redirect=false
    [ "$repo" = smoke ] || redirect=true
    # Answers 400 when the repository exists from an earlier run.
    curl -fs "${auth[@]}" -X PUT -H 'Content-Type: application/json' \
      -d "{\"rclass\":\"local\",\"packageType\":\"generic\",\"downloadRedirect\":$redirect}" \
      "$base/artifactory/api/repositories/$repo" >/dev/null || true
  done
  # 40 MiB is several parts on every backend, so the upload exercises the
  # multipart, compose or block-list path.
  head -c $((40 << 20)) /dev/urandom >"$work/big.bin"
  local sha back
  sha=$(shasum -a 256 "$work/big.bin" | cut -d' ' -f1)
  curl -fsS "${auth[@]}" -X PUT -H "X-Checksum-Sha256: $sha" \
    -T "$work/big.bin" "$base/artifactory/smoke/big.bin" >/dev/null
  curl -fsS "${auth[@]}" -o "$work/back.bin" "$base/artifactory/smoke/big.bin"
  back=$(shasum -a 256 "$work/back.bin" | cut -d' ' -f1)
  [ "$sha" = "$back" ] || {
    echo "checksum mismatch: $sha against $back" >&2
    exit 1
  }
  echo "deployed and downloaded 40 MiB: sha256 $sha"

  curl -fsS "${auth[@]}" -X PUT -H "X-Checksum-Sha256: $sha" \
    -T "$work/big.bin" "$base/artifactory/smoke-redirect/big.bin" >/dev/null
  local code location host
  read -r code location < <(curl -sS "${auth[@]}" -o /dev/null -w '%{http_code} %{redirect_url}\n' \
    "$base/artifactory/smoke-redirect/big.bin")
  host=${location#*://}
  host=${host%%/*}
  if [ "$code" != 302 ] || ! allowed_hosts | grep -qx "$host"; then
    echo "expected a 302 to the scenario's storage host, got $code to '${host:-nothing}'" >&2
    exit 1
  fi
  curl -fsS -o "$work/redirected.bin" "$location"
  back=$(shasum -a 256 "$work/redirected.bin" | cut -d' ' -f1)
  [ "$sha" = "$back" ] || {
    echo "checksum mismatch through the redirect: $sha against $back" >&2
    exit 1
  }
  echo "downloadRedirect answered 302 to $host and the signed URL served the same bytes"
)

# allowed_hosts prints the storage hosts a redirect may land on: the
# scenario's storage_host, and every mirror member's.
allowed_hosts() {
  python3 -I - .local/outputs.json <<'PY'
import json, sys
outputs = {k: v["value"] for k, v in json.load(open(sys.argv[1])).items()}
print(outputs["storage_host"])
for member in outputs.get("mirror_members", []) or []:
    print(member["signed_url_prefix"].split("/", 1)[0])
PY
}

verify() {
  credentials
  local out=.local/platform-tests
  (
    load_env
    "$root/deploy/ci/platform-tests.sh" "$PWD" "$CLOUD/$(basename "$PWD")" "$PWD/$out"
  )
}

down() {
  if running; then
    local pid
    pid=$(cat "$pidfile")
    kill -TERM "$pid"
    # SIGTERM lets the server drain; give it the drain timeout.
    for _ in $(seq 1 15); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid"; fi
    echo "stopped artifactweave (pid $pid); .local/data is kept"
  fi
  rm -f "$pidfile"
}

case $action in
render) render ;;
credentials) credentials ;;
up) up ;;
smoke) smoke ;;
verify) verify ;;
logs)
  [ -f "$logfile" ] || {
    echo "no log yet; run make up" >&2
    exit 1
  }
  tail -n 100 -f "$logfile"
  ;;
down) down ;;
clean)
  down
  rm -rf .local
  ;;
*)
  echo "unknown action: $action" >&2
  exit 1
  ;;
esac
