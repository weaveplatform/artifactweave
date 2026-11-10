#!/bin/sh
set -eu

# Run inside pinned Alpine 3.20 with this directory mounted at /out. The
# gzip(tar) layout matches OpenWrt 24.10's scripts/ipkg-build: debian-binary,
# data.tar.gz, then control.tar.gz. Every variant is a real installable IPK.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for name in alpha beta; do
  for version in 1.0 2.0; do
    for variant in 0 1; do
      rm -rf "$work/package" "$work/archive"
      mkdir -p "$work/package/CONTROL" "$work/package/usr/share/artifactweave" "$work/archive"
      cat > "$work/package/CONTROL/control" <<EOF
Package: $name
Version: $version
Architecture: all
Maintainer: Artifactweave tests
Description: Controlled Opkg contract package
Section: test
Priority: optional
EOF
      printf '%s %s variant %s\n' "$name" "$version" "$variant" > "$work/package/usr/share/artifactweave/$name.txt"
      printf '2.0\n' > "$work/archive/debian-binary"
      tar -czf "$work/archive/data.tar.gz" -C "$work/package" ./usr
      tar -czf "$work/archive/control.tar.gz" -C "$work/package/CONTROL" ./control
      tar -czf "/out/${name}_${version}_${variant}.ipk" -C "$work/archive" \
        ./debian-binary ./data.tar.gz ./control.tar.gz
    done
  done
done
