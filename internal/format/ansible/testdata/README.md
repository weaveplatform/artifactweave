# Ansible archive fixture

`weave-sample-1.2.3.tar.gz` is the controlled collection built by ansible-galaxy
and retained from the initial Ansible implementation work. It contains a generated
collection scaffold, no downloaded third-party collection code. Its manifests and
file checksums are verified by `TestUnitReadClientBuiltArchive`.

The current mandatory native proof does not rely on this historical archive:
`test/acceptance/ansible_steps_test.go` writes controlled `galaxy.yml` sources and
builds both `weave.sample` and `weave.dependency` using the pinned ansible-core
2.21.5 client. The sample declares `weave.dependency: >=1.0.0,<2.0.0`; fresh
installation must resolve that dependency through the selected repository class.
