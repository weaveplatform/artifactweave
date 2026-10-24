#!/bin/sh
set -eu

# Run in the pinned Fedora 42 acceptance image with this directory mounted at
# /out. Each pair has the same RPM identity and different installed bytes, so
# the format contract exercises replacement without weakening RPM validation.
dnf -y install rpm-build >/dev/null
work=/tmp/artifactweave-rpm-contract
mkdir -p "$work/SPECS" "$work/BUILD" "$work/RPMS" "$work/SOURCES"
export SOURCE_DATE_EPOCH=1700000000

for name in alpha beta; do
  for version in 1.0 2.0; do
    for variant in 0 1; do
      spec="$work/SPECS/$name-$version-$variant.spec"
      cat > "$spec" <<EOF
Name:           $name
Version:        $version
Release:        1%{?dist}
Summary:        Controlled RPM format contract package
License:        MIT
BuildArch:      noarch

%description
Controlled RPM package for repository lifecycle tests.

%install
mkdir -p %{buildroot}/usr/share/artifactweave
printf '$name $version variant $variant\\n' > %{buildroot}/usr/share/artifactweave/$name.txt

%files
/usr/share/artifactweave/$name.txt
EOF
      rpmbuild -bb \
        --define "_topdir $work" \
        --define 'use_source_date_epoch_as_buildtime 1' \
        "$spec" >/dev/null
      cp "$work/RPMS/noarch/$name-$version-1.fc42.noarch.rpm" \
        "/out/${name}_${version}_${variant}.rpm"
    done
  done
done
