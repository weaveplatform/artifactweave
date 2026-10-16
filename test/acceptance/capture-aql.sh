#!/usr/bin/env bash
# Refreshes pkg/aql/aqltest/testdata with the AQL the pinned JFrog CLI sends.
# The acceptance scenario "the query shapes the fixtures capture" runs one jf
# command per fixture, in the order below, and the server logs each query at
# debug level; this copies them over the fixtures. Run it whenever the pinned
# jf changes, then check the expected rows in pkg/aql/aqltest still hold.
set -euo pipefail
: "${ARTIFACTWEAVE_JF_BIN:?set ARTIFACTWEAVE_JF_BIN to the pinned jf binary}"
cd "$(dirname "$0")/../.."
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
ARTIFACTWEAVE_AQL_CAPTURE="$out" ARTIFACTWEAVE_ACCEPT_LOCAL=1 GOWORK=off \
	go test -count=1 ./test/acceptance/ -run 'TestFeatures/the_query_shapes_the_fixtures_capture'
names=(
	search_recursive search_props_sort search_props_properties
	search_root_offset_limit search_root_properties download_exclusions
	download_remote set_props_with_props copy_move_file search_build
	download_build delete_any
)
captured=("$out"/the_query_shapes_the_fixtures_capture-*.aql)
if [ "${#captured[@]}" -ne "${#names[@]}" ]; then
	echo "captured ${#captured[@]} queries, expected ${#names[@]}: the CLI's queries changed shape; update the names here and the scenario" >&2
	exit 1
fi
for i in "${!names[@]}"; do
	cp "${captured[$i]}" "pkg/aql/aqltest/testdata/${names[$i]}.aql"
done
echo "refreshed ${#names[@]} fixtures in pkg/aql/aqltest/testdata"
