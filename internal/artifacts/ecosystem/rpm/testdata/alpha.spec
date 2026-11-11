Name:           artifactweave-alpha
Version:        1.0
Release:        1%{?dist}
Summary:        Controlled RPM acceptance package
License:        MIT
BuildArch:      noarch

%description
A controlled RPM package for artifactweave protocol acceptance.

%install
mkdir -p %{buildroot}/usr/share/artifactweave
printf 'alpha 1.0\n' > %{buildroot}/usr/share/artifactweave/alpha.txt

%files
/usr/share/artifactweave/alpha.txt
