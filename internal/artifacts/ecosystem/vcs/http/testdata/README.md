# Git archive fixtures

These are unchanged outputs from Git 2.55.0 `git archive`, with no archive prefix.
`git-archives.json` records each source commit and the SHA-256 of every archive.
The generator fixes the commit identity, timestamp, content and initial branch;
it disables user and system Git configuration. No proprietary publisher schema
or server capture is included here.

Regenerate into a temporary directory and compare the hashes before replacing:

```sh
python3 internal/artifacts/ecosystem/vcs/http/testdata/generate.py --output /tmp/vcs-git-fixtures
```

Full archives contain both `README.md` and `sub/README.md`, plus `sub/ONLY.md`
without a corresponding root `ONLY.md`. This detects accidental removal of a
path component during selected-file extraction. The single-file archives retain
Git's commit comment and, for nested paths, its ancestor directory entry.

Git writes the source commit into a global PAX comment for tar archives and the
ZIP comment for ZIP archives. Tests validate that metadata against the requested
commit while preserving the original archive bytes. See the official
[git-archive documentation](https://git-scm.com/docs/git-archive).
