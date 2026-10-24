#!/bin/sh
# Run inside an Ubuntu 24.04 image with dpkg-dev installed. The resulting
# source fixture is produced by dpkg-source, not by the registry under test.
set -eu

out=$1
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
mkdir -p alpha-1.0/debian/source
printf 'controlled source fixture\n' > alpha-1.0/README
cat > alpha-1.0/debian/control <<'CONTROL'
Source: alpha
Section: misc
Priority: optional
Maintainer: Artifactweave Test <test@example.invalid>
Standards-Version: 4.7.0
Build-Depends: debhelper-compat (= 13)

Package: alpha
Architecture: any
Description: controlled source fixture
 A minimal source package for native apt checks.
CONTROL
cat > alpha-1.0/debian/changelog <<'CHANGELOG'
alpha (1.0-1) stable; urgency=medium

  * Controlled source fixture.

 -- Artifactweave Test <test@example.invalid>  Sat, 24 Oct 2026 12:00:00 +0000
CHANGELOG
printf '3.0 (quilt)\n' > alpha-1.0/debian/source/format
printf '#!/usr/bin/make -f\n%%:\n\tdh $@\n' > alpha-1.0/debian/rules
chmod +x alpha-1.0/debian/rules
tar -czf alpha_1.0.orig.tar.gz --transform='s,^alpha-1.0/,alpha-1.0/,' alpha-1.0/README
dpkg-source -b alpha-1.0
cp alpha_1.0-1.dsc alpha_1.0-1.debian.tar.xz alpha_1.0.orig.tar.gz "$out"/
