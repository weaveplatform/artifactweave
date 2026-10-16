#!/usr/bin/env bash
# Moves the JFrog Terraform providers the app-layer references in
# deploy/artifactweave/terraform use (jfrog/artifactory, jfrog/platform) to
# their latest releases, everywhere they are pinned:
#
#   - each reference's versions.tf;
#   - test/terraform/run.sh, the version and the SHA-256 of each release zip
#     its filesystem mirror installs, taken from the release's SHA256SUMS
#     once its signature checks against the key in
#     jfrog-terraform-providers.asc;
#   - each reference's .terraform.lock.hcl, for linux and darwin on amd64
#     and arm64, from a filesystem mirror of the checked zips;
#   - the versions the references' READMEs name, and their terraform-docs
#     sections.
#
# It then runs run.sh validate over every reference, so the pins, the
# checksums and the locks are known to agree before anything is committed.
#
#   jfrog-providers.sh check      each provider's pinned and latest version
#                                 and the bump; changes nothing
#   jfrog-providers.sh update     move the pins in place
#   jfrog-providers.sh dry-run    update a copy of the tree and print the diff
#
# With GITHUB_OUTPUT set, update writes changed (true|false), major
# (true|false) and versions ("artifactory 12.11.15 and platform 2.2.12") for
# deps-refresh.yml. ARTIFACTORY_TARGET and PLATFORM_TARGET name a version
# instead of the latest release; JFROG_PROVIDERS_ROOT names the tree to work
# on. It never moves a pin backwards.
set -euo pipefail

# The key both providers' SHA256SUMS are signed with, as the Terraform
# registry publishes it for the jfrog namespace
# (registry.terraform.io/v1/providers/jfrog/<name>/<version>/download/...,
# signing_keys). A signature by any other key fails the update: a rotated key
# is a change a person reviews, not one this script follows.
JFROG_SIGNING_FINGERPRINT=7F2AD74397A7E25E2D0B082E2FA4D2A520237FA7
PROVIDERS="artifactory platform"
PLATFORMS="linux_amd64 linux_arm64 darwin_amd64 darwin_arm64"

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=${JFROG_PROVIDERS_ROOT:-$(cd "$here/../.." && pwd)}

die() {
	echo "jfrog-providers: $*" >&2
	exit 1
}

# pinned prints the version run.sh pins for a provider.
pinned() {
	local var
	var=$(echo "$1" | tr '[:lower:]' '[:upper:]')_VERSION
	sed -n "s/^$var=\([0-9][0-9.]*\)\$/\1/p" "$root/test/terraform/run.sh"
}

# latest prints a provider's latest release, or its *_TARGET override.
latest() {
	local var target auth=()
	var=$(echo "$1" | tr '[:lower:]' '[:upper:]')_TARGET
	target=${!var:-}
	if [ -n "$target" ]; then
		echo "$target"
		return
	fi
	[ -n "${GH_TOKEN:-}" ] && auth=(-H "Authorization: Bearer $GH_TOKEN")
	# ${auth[@]+...}: bash 3.2 (macOS) calls an empty array unbound under -u.
	curl -fsSL ${auth[@]+"${auth[@]}"} "https://api.github.com/repos/jfrog/terraform-provider-$1/releases/latest" |
		tr ',' '\n' | sed -n 's/^ *"tag_name": *"v\{0,1\}\([0-9][0-9.]*\)".*/\1/p'
}

# bump names the change from one x.y.z to another: none, patch, minor, major
# or downgrade.
bump() {
	local a1 a2 a3 b1 b2 b3
	IFS=. read -r a1 a2 a3 <<<"$1"
	IFS=. read -r b1 b2 b3 <<<"$2"
	if [ "$b1" -gt "$a1" ]; then echo major
	elif [ "$b1" -lt "$a1" ]; then echo downgrade
	elif [ "$b2" -gt "$a2" ]; then echo minor
	elif [ "$b2" -lt "$a2" ]; then echo downgrade
	elif [ "$b3" -gt "$a3" ]; then echo patch
	elif [ "$b3" -lt "$a3" ]; then echo downgrade
	else echo none
	fi
}

# verify_sums checks a SHA256SUMS file's detached signature against the
# committed key, and that the key which signed it is the pinned one.
verify_sums() {
	local sums=$1 sig=$2 home status
	command -v gpg >/dev/null || die "gpg is needed to check the release signatures"
	home=$(mktemp -d)
	status=$(
		export GNUPGHOME=$home
		gpg --batch --quiet --no-autostart --import "$here/jfrog-terraform-providers.asc" 2>/dev/null
		gpg --batch --no-autostart --status-fd 1 --verify "$sig" "$sums" 2>/dev/null || true
	)
	rm -rf "$home"
	echo "$status" | grep -q "^\[GNUPG:\] VALIDSIG .* $JFROG_SIGNING_FINGERPRINT\$"
}

# fetch_sums downloads and checks one release's SHA256SUMS into a directory.
fetch_sums() {
	local name=$1 version=$2 dir=$3 base sums
	base="https://github.com/jfrog/terraform-provider-$name/releases/download/v$version"
	sums="terraform-provider-${name}_${version}_SHA256SUMS"
	curl -fsSL -o "$dir/$sums" "$base/$sums"
	curl -fsSL -o "$dir/$sums.sig" "$base/$sums.sig"
	verify_sums "$dir/$sums" "$dir/$sums.sig" ||
		die "the signature on $sums does not check against key $JFROG_SIGNING_FINGERPRINT"
}

# sum_for prints the SHA-256 a SHA256SUMS file gives one platform's zip.
sum_for() {
	local sums=$1 name=$2 version=$3 platform=$4 sum
	sum=$(awk -v z="terraform-provider-${name}_${version}_${platform}.zip" '$2 == z { print $1 }' "$sums")
	[[ $sum =~ ^[0-9a-f]{64}$ ]] || die "no SHA-256 for $name $version $platform in $(basename "$sums")"
	echo "$sum"
}

# rewrite_versions_tf moves one provider's version constraint in a
# versions.tf, inside the block whose source is jfrog/<name> only.
rewrite_versions_tf() {
	local file=$1 name=$2 version=$3 tmp
	tmp=$(mktemp)
	# Two passes over the file: the first finds which blocks name the
	# provider, the second edits their version lines in place, whichever
	# order source and version come in.
	awk -v src="jfrog/$name" -v ver="$version" '
		FNR == 1 { b = 0; inb = 0 }
		/^[[:space:]]*[a-z_]+[[:space:]]*=[[:space:]]*\{/ { b++; inb = 1 }
		NR == FNR {
			if (inb && $0 ~ ("source[[:space:]]*=[[:space:]]*\"" src "\"")) mine[b] = 1
			if (inb && /^[[:space:]]*}/) inb = 0
			next
		}
		inb && mine[b] && /^[[:space:]]*version[[:space:]]*=/ { sub(/"[^"]*"/, "\"" ver "\"") }
		inb && /^[[:space:]]*}/ { inb = 0 }
		{ print }
	' "$file" "$file" >"$tmp"
	mv "$tmp" "$file"
}

# rewrite_run_sh moves one provider's version and its four zip checksums in
# run.sh.
rewrite_run_sh() {
	local file=$1 name=$2 version=$3 sums=$4 var p sum
	var=$(echo "$name" | tr '[:lower:]' '[:upper:]')_VERSION
	sed -i.bak "s/^$var=.*/$var=$version/" "$file"
	for p in $PLATFORMS; do
		sum=$(sum_for "$sums" "$name" "$version" "$p")
		sed -i.bak -E "s/^([[:space:]]*${name}_$p\) echo )[0-9a-f]{64}( ;;)$/\1$sum\2/" "$file"
	done
	rm -f "$file.bak"
}

# rewrite_prose replaces one version with another wherever the references'
# READMEs name it, as a whole version only: 2.2.11 never matches 12.2.11 or
# 2.2.110.
rewrite_prose() {
	local old=$1 new=$2
	# shellcheck disable=SC2016 # perl reads OLD and NEW from its environment
	find "$root/deploy/artifactweave" -name '*.md' -not -path '*/.terraform/*' -print0 |
		OLD=$old NEW=$new xargs -0 perl -pi -e 's/(?<![0-9.])\Q$ENV{OLD}\E(?![0-9])/$ENV{NEW}/g'
}

# terraform_on_path makes the terraform deploy/check.sh pins callable.
terraform_on_path() {
	local want
	want=$(sed -n 's/^TERRAFORM_VERSION=//p' "$root/deploy/check.sh")
	[ "$(terraform version 2>/dev/null | head -1)" = "Terraform v$want" ] && return
	"$root/deploy/check.sh" tools
	PATH=$root/.tools/terraform-$want:$PATH
	[ "$(terraform version 2>/dev/null | head -1)" = "Terraform v$want" ] || die "no terraform $want"
}

# references lists the app-layer references, the roots on the jfrog
# providers.
references() {
	find "$root/deploy/artifactweave/terraform" -name versions.tf -exec dirname {} \; | sort
}

report() {
	local name from to
	for name in $PROVIDERS; do
		from=$(pinned "$name")
		to=$(latest "$name")
		[ -n "$from" ] || die "run.sh pins no $name version"
		[ -n "$to" ] || die "no latest $name release"
		printf '%s %s %s %s\n' "$name" "$from" "$to" "$(bump "$from" "$to")"
	done
}

update() {
	local plan name from to kind work mirror ref p zip url sums changed=false major=false versions=""
	plan=$(report)
	echo "$plan" | while read -r name from to kind; do
		echo "jfrog/$name: pinned $from, latest $to ($kind)"
	done
	echo "$plan" | grep -Eq ' (patch|minor|major)$' || {
		echo "already at the latest releases"
		output changed false major false versions ""
		return
	}
	work=$(mktemp -d)
	mirror=$work/mirror/registry.terraform.io/jfrog
	while read -r name from to kind; do
		case $kind in
		patch | minor | major) ;;
		*) continue ;;
		esac
		changed=true
		[ "$kind" = major ] && major=true
		mkdir -p "$work/$name" "$mirror/$name"
		fetch_sums "$name" "$to" "$work/$name"
		sums="$work/$name/terraform-provider-${name}_${to}_SHA256SUMS"
		for p in $PLATFORMS; do
			zip="terraform-provider-${name}_${to}_$p.zip"
			url="https://github.com/jfrog/terraform-provider-$name/releases/download/v$to/$zip"
			curl -fsSL -o "$mirror/$name/$zip" "$url"
			echo "$(sum_for "$sums" "$name" "$to" "$p")  $mirror/$name/$zip" |
				(sha256sum -c - 2>/dev/null || shasum -a 256 -c -) >/dev/null ||
				die "$zip does not match its signed SHA256SUMS"
		done
		rewrite_run_sh "$root/test/terraform/run.sh" "$name" "$to" "$sums"
		for ref in $(references); do
			grep -q "source *= *\"jfrog/$name\"" "$ref/versions.tf" && rewrite_versions_tf "$ref/versions.tf" "$name" "$to"
		done
		rewrite_prose "$from" "$to"
	done <<<"$plan"
	for name in $PROVIDERS; do
		to=$(echo "$plan" | awk -v n="$name" '$1 == n { print $3 }')
		from=$(echo "$plan" | awk -v n="$name" '$1 == n { print $2 }')
		[ "$(bump "$from" "$to")" = downgrade ] && to=$from
		versions="${versions:+$versions and }$name $to"
	done
	terraform_on_path
	for ref in $(references); do
		# providers lock refuses to move a version the lock records, so each
		# lock is written afresh, for every provider the reference requires,
		# from the mirror of checked zips.
		rm -f "$ref/.terraform.lock.hcl"
		(cd "$ref" && TF_DATA_DIR=$work/data/$(basename "$ref") terraform providers lock \
			-fs-mirror="$work/mirror" -platform=linux_amd64 -platform=linux_arm64 \
			-platform=darwin_amd64 -platform=darwin_arm64 >/dev/null)
	done
	"$root/deploy/check.sh" docs
	# The pins, the checksums and the locks agree, or this fails.
	# shellcheck disable=SC2046 # one argument per reference
	(cd "$root" && ./test/terraform/run.sh validate $(references | sed "s|^$root/||"))
	rm -rf "$work"
	echo "moved to $versions"
	output changed "$changed" major "$major" versions "$versions"
}

# output writes key/value pairs to GITHUB_OUTPUT when it is set.
output() {
	[ -n "${GITHUB_OUTPUT:-}" ] || return 0
	while [ $# -ge 2 ]; do
		echo "$1=$2" >>"$GITHUB_OUTPUT"
		shift 2
	done
}

# dry_run updates a copy of what update touches and reads, then prints the
# difference from the tree.
dry_run() {
	local copy
	copy=$(mktemp -d)
	mkdir -p "$copy/test"
	cp -R "$root/deploy" "$copy/deploy"
	cp -R "$root/test/terraform" "$copy/test/terraform"
	cp "$root/.terraform-docs.yml" "$copy/"
	[ -d "$root/.tools" ] && ln -s "$root/.tools" "$copy/.tools"
	JFROG_PROVIDERS_ROOT=$copy GITHUB_OUTPUT='' "$here/jfrog-providers.sh" update
	echo "== the update would change:"
	(cd "$root" && diff -ru -x .terraform -x '.terraform.lock.hcl.bak' deploy/artifactweave "$copy/deploy/artifactweave" | sed "s|$copy/||") || true
	(cd "$root" && diff -u test/terraform/run.sh "$copy/test/terraform/run.sh" | sed "s|$copy/||") || true
	rm -rf "$copy"
}

main() {
	case ${1:-} in
	check) report | while read -r name from to kind; do echo "jfrog/$name: pinned $from, latest $to ($kind)"; done ;;
	update) update ;;
	dry-run) dry_run ;;
	*)
		echo "usage: $0 check|update|dry-run" >&2
		exit 2
		;;
	esac
}

# The table test sources the functions without running main.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	main "$@"
fi
