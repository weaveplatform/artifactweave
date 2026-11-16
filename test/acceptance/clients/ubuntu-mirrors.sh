#!/bin/sh
# ubuntu-mirrors gives each Ubuntu archive host in the image's APT sources a
# second mirror, so one host being down does not fail a CI build (#1632), as
# fetch-pinned.sh does for the files the images download (#1321). The Ubuntu
# images run it before their first apt-get update.
#
# Each host's sources become a mirror+file: list of that host and then its
# second mirror. apt-transport-mirror(1) tries the mirrors of a list in random
# order unless each has a priority, hence priority:1 and priority:2. APT
# retries a file on the first mirror (Acquire::Retries, 3 by default since APT
# 2.3.2, so in both the 22.04 and the 26.10 images) and then fetches it from
# the second.
#
# A second mirror can serve only what Canonical signed. APT checks each
# suite's InRelease against the Ubuntu archive key in the base image
# (Signed-By in ubuntu.sources, trusted.gpg.d on 22.04), and every index and
# package against the SHA-256 in that signed file, whichever mirror serves it.
# Nothing here turns that check off.
#
# azure.archive.ubuntu.com carries every suite the images read, -security
# included, and GitHub's own Ubuntu runner images install from it, with
# archive.ubuntu.com as their failover (actions/runner-images,
# images/ubuntu/scripts/build/configure-apt-sources.sh). It is on Microsoft's
# network, apart from the Canonical hosts the runners could not reach on
# 2026-11-16. An arm64 build reads ports.ubuntu.com instead, and
# azure.ports.ubuntu.com resolves to the same Canonical hosts, so the ports
# mirror of UC Berkeley's Open Computing Facility, on Launchpad's list of
# Ubuntu archive mirrors, is its second.
#
# It fails when no source names one of these hosts, or one still does after
# the rewrite, so a base image that moves its sources fails the build rather
# than quietly losing the second mirror.
#
# $sources is split on purpose: it lists APT's own paths, which hold no spaces.
# shellcheck disable=SC2086
set -eu

lists=/etc/apt/mirrors
sources=
for file in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
	if [ -f "$file" ]; then
		sources="$sources $file"
	fi
done
if [ -z "$sources" ]; then
	echo "ubuntu-mirrors: the image has no APT sources" >&2
	exit 1
fi

# mirror HOST PATH SECOND points every source for http://HOST/PATH/ at a list
# of that URL and then SECOND. The URL ends a deb822 URIs: line, and is
# followed by the suite in a one-line sources.list; either form may drop the
# last slash.
mirror() {
	uri="http://$(printf %s "$1" | sed 's/\./\\./g')/$2/?([[:space:]]|\$)"
	if ! grep -Eq "^[^#]*$uri" $sources; then
		return 0
	fi
	mkdir -p "$lists"
	printf 'http://%s/%s/\tpriority:1\n%s\tpriority:2\n' "$1" "$2" "$3" >"$lists/$1.list"
	sed -i -E "/^[[:space:]]*#/!s#$uri#mirror+file:$lists/$1.list\\1#" $sources
	echo "ubuntu-mirrors: http://$1/$2/, then $3"
}
mirror archive.ubuntu.com ubuntu http://azure.archive.ubuntu.com/ubuntu/
mirror security.ubuntu.com ubuntu http://azure.archive.ubuntu.com/ubuntu/
mirror ports.ubuntu.com ubuntu-ports http://mirrors.ocf.berkeley.edu/ubuntu-ports/

if grep -Eq '^[^#]*https?://(archive|security|ports)\.ubuntu\.com' $sources ||
	! grep -q "^[^#]*mirror+file:$lists/" $sources; then
	echo "ubuntu-mirrors: these sources name no Ubuntu host it knows, or one it left:" >&2
	grep -H -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' $sources >&2 || true
	exit 1
fi
