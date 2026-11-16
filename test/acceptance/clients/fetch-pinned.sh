#!/bin/sh
# fetch-pinned DIGEST FILE SOURCE... writes FILE from the first SOURCE whose
# bytes match DIGEST, a SHA-256, or a SHA-512 where that is the digest upstream
# publishes.
#
# Every SOURCE after the first is a second source for the same bytes, so one
# host being down does not fail a CI build (#1321, #1322). The digest is
# checked for whichever source answers, so a second source can never change
# what runs, and a source that answers with other bytes is passed over like
# one that does not answer. There is no loop over a host that is down: curl
# retries only a passing fault, and the stall limit moves a host that accepts
# the connection but sends nothing on to the next source. When every source
# is down the build fails, which the maintainer accepts.
#
# A SOURCE is a URL, or "URL via HOST": the same URL fetched through HOST,
# which curl connects to while it still sends, and checks the certificate
# for, the URL's own host name. That is for a download host whose name a
# traffic manager hands to more than one delivery network: naming each
# network makes it a second source, though the origin behind them is one
# (#1603).
#
# The client images COPY it from their build context, test/acceptance/clients,
# and the AI editor assets action runs it from the checkout, which is why it
# lives here and not under .github.
set -u

usage() {
	echo "usage: fetch-pinned DIGEST FILE SOURCE..." >&2
	exit 2
}

[ $# -ge 3 ] || usage
digest=$1
file=$2
shift 2
case $digest in
*[!0-9a-f]*) usage ;;
esac
case ${#digest} in
64) check=sha256sum ;;
128) check=sha512sum ;;
*) usage ;;
esac

for source; do
	url=${source%% via *}
	route=
	if [ "$url" != "$source" ]; then
		# Only the URL's own host goes through HOST, so a redirect to another
		# host keeps its usual route.
		host=${url#*://}
		host=${host%%[/:?#]*}
		route=$host::${source##* via }:
	fi
	if curl -fsSL --connect-timeout 15 --retry 3 --retry-delay 2 \
		--speed-limit 1024 --speed-time 30 ${route:+--connect-to "$route"} \
		-o "$file" "$url" &&
		echo "$digest  $file" | "$check" -c -; then
		echo "fetch-pinned: $file from $source"
		exit 0
	fi
	echo "fetch-pinned: $source gave no file with the pinned digest" >&2
done
rm -f "$file"
echo "fetch-pinned: no source gave $file with its pinned digest" >&2
exit 1
