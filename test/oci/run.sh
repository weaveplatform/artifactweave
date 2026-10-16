#!/usr/bin/env bash
# Runs the OCI distribution-spec conformance suite against artifactweave
# (decision 0015, research 23 section 9).
#
#   run.sh   build the suite at its pinned commit, start one -cover
#            artifactweave on SQLite and the fs blob store, create two local
#            docker repositories and a user with Read, Deploy and Delete on
#            both, run the suite, and exit with its status
#
# The categories come from OCI_TEST_PULL, OCI_TEST_PUSH,
# OCI_TEST_CONTENT_DISCOVERY and OCI_TEST_CONTENT_MANAGEMENT, which the
# Makefile sets. The server's coverage goes to GOCOVERDIR and the suite's
# junit.xml and report.html to OCI_REPORT_DIR. A failed specification fails
# the run; a skipped one never does.
set -euo pipefail

# The commit tag v1.1.1 points to. The suite is fetched by commit, so a moved
# tag cannot change what runs, and built with its own go.mod: it is a Ginkgo
# module that this module never imports.
SUITE_COMMIT=a139cc423184af6078077b9b7ee336eddbd03f8f

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=$(mktemp -d)
server=
# shellcheck disable=SC2329 # run by the EXIT trap; the closing exit hides that
cleanup() {
	if [ -n "$server" ]; then
		kill -TERM "$server" 2>/dev/null || true
		wait "$server" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

# Built once per commit into .tools, like the deploy-check tools.
suite_dir=${OCI_SUITE_CACHE:-$root/.tools/oci-conformance-$SUITE_COMMIT}
suite=$suite_dir/conformance.test
if [ ! -x "$suite" ]; then
	src=$work/distribution-spec
	mkdir -p "$src" "$suite_dir"
	git -C "$src" init -q
	git -C "$src" fetch -q --depth 1 https://github.com/opencontainers/distribution-spec.git "$SUITE_COMMIT"
	git -C "$src" checkout -q FETCH_HEAD
	# The suite's own modules, retried like .github/actions/go-mod-download,
	# which fills only this module's cache.
	for attempt in 1 2 3; do
		(cd "$src/conformance" && go mod download) && break
		[ "$attempt" -lt 3 ] || exit 1
		sleep $((attempt * 15))
	done
	(cd "$src/conformance" && go test -c -o "$suite.part" .)
	mv "$suite.part" "$suite"
fi

# A -cover binary so the run's coverage joins the merged report, with the
# acceptance build's flags: covdata merge refuses to mix counter modes, and
# the unit tests' -race forces atomic.
module=$(cd "$root" && go list -m)
(cd "$root" && go build -cover -covermode=atomic -coverpkg="$module/..." -o "$work/artifactweave" ./cmd/artifactweave)
port=${OCI_CONFORMANCE_PORT:-18082}
url="http://127.0.0.1:$port"
password=conformance-admin
mkdir -p "${GOCOVERDIR:=$work/cover}"
export GOCOVERDIR
ARTIFACTWEAVE_MASTER_KEY=conformance-master-key-0123456789abcdef0123456789abcdef \
	ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=$password \
	"$work/artifactweave" serve --listen "127.0.0.1:$port" --data-dir "$work/data" \
	--base-url "$url" >"$work/server.log" 2>&1 &
server=$!
ready=
for _ in $(seq 100); do
	if curl -fs "$url/artifactory/api/v1/system/readiness" >/dev/null; then
		ready=1
		break
	fi
	sleep 0.1
done
if [ -z "$ready" ]; then
	echo "artifactweave did not become ready on $url" >&2
	cat "$work/server.log" >&2
	exit 1
fi

admin() {
	curl -fsS -u "admin:$password" -H 'Content-Type: application/json' "$@" >/dev/null
}
# Two repositories, so OCI_CROSSMOUNT_NAMESPACE mounts across repositories.
for repo in oci-conformance oci-mount; do
	admin -X PUT -d '{"rclass":"local","packageType":"docker"}' "$url/artifactory/api/repositories/$repo"
done
user=conformance
user_password=Conformance-pass1
admin -X POST -d "{\"username\":\"$user\",\"email\":\"$user@example.com\",\"password\":\"$user_password\"}" \
	"$url/access/api/v2/users"
# Read, Deploy and Delete: Deploy for pushes, Delete for the manifest deletes
# and for pushing over an existing tag (DV-035).
admin -X PUT -d "{\"name\":\"oci-conformance\",\"repo\":{\"repositories\":[\"oci-conformance\",\"oci-mount\"],
	\"include-patterns\":[\"**\"],\"exclude-patterns\":[],\"actions\":{\"users\":{\"$user\":[\"read\",\"write\",\"delete\"]}}}}" \
	"$url/artifactory/api/v2/security/permissions/oci-conformance"

report=${OCI_REPORT_DIR:-$work/report}
mkdir -p "$report"
status=0
(
	cd "$report"
	# Blob deletes answer 405 (decision 0031), so manifests go first; a mount
	# without `from` opens a session rather than guessing a source.
	OCI_ROOT_URL=$url \
		OCI_NAMESPACE=oci-conformance/conformance \
		OCI_CROSSMOUNT_NAMESPACE=oci-mount/conformance \
		OCI_USERNAME=$user \
		OCI_PASSWORD=$user_password \
		OCI_AUTOMATIC_CROSSMOUNT=0 \
		OCI_DELETE_MANIFEST_BEFORE_BLOBS=1 \
		OCI_HIDE_SKIPPED_WORKFLOWS=0 \
		OCI_TEST_PULL=${OCI_TEST_PULL:-0} \
		OCI_TEST_PUSH=${OCI_TEST_PUSH:-0} \
		OCI_TEST_CONTENT_DISCOVERY=${OCI_TEST_CONTENT_DISCOVERY:-0} \
		OCI_TEST_CONTENT_MANAGEMENT=${OCI_TEST_CONTENT_MANAGEMENT:-0} \
		OCI_REPORT_DIR=$report \
		"$suite"
) || status=$?
if [ "$status" -ne 0 ]; then
	echo "--- artifactweave log (last 200 lines)" >&2
	tail -n 200 "$work/server.log" >&2
fi
exit "$status"
