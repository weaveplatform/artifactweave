#!/usr/bin/env bash
# Retry only transient ruleset-download failures. The caller still owns every
# lint result; configuration, authentication and integrity failures stay fatal.
set -euo pipefail

retry_log=$(mktemp)
trap 'rm -f "$retry_log"' EXIT

for retry_attempt in 1 2 3; do
	if tflint --init "$@" 2>"$retry_log"; then
		cat "$retry_log" >&2
		exit 0
	else
		retry_status=$?
	fi
	cat "$retry_log" >&2
	if [ "$retry_attempt" -eq 3 ] || ! LC_ALL=C grep -Eiq \
		'(^|[[:space:]:])(429|500|502|503|504)([[:space:]]|$)|Bad Gateway|Service Unavailable|Gateway Timeout|connection reset by peer|connection refused|i/o timeout|TLS handshake timeout|temporary failure in name resolution|no such host|network is unreachable|unexpected EOF' \
		"$retry_log"; then
		exit "$retry_status"
	fi
	retry_delay=$((retry_attempt * 2))
	printf 'tflint ruleset download failed transiently (attempt %s of 3); retrying in %ss\n' "$retry_attempt" "$retry_delay" >&2
	sleep "$retry_delay"
done
