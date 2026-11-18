# P10 Terraform module virtual repositories

Issue [#1550](https://github.com/weaveplatform/artifactweave/issues/1550), researched
2026-11-18 against the current JFrog 7.x documentation, release notes, public Jira
references retained in [research 31](31-terraform.md), and the verified official
Artifactory OSS 7.161.26 source distribution. No trial or live capture was used.

## Primary evidence

- JFrog's [Terraform repositories](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories)
  documents virtual aggregation of module/provider local and remote repositories,
  the `host/repo__namespace/name/system` module address, and source hostname
  override enabled by default since 7.39.4. Its disabling property is
  `artifactory.terraform.override.modules.source.hostname=false`.
- RTFACT-30392's decoded token, recorded in research 31 section 2.4, carries
  `internal_repo`; expiry is 90 seconds. RTDEV-64996, fixed in 7.125.7, requires the
  complete module path when a member key equals the namespace.
- RTFACT-31270 covers rewriting registry sources with `//modules/...` selectors.
  RTFACT-30802 establishes the remote's `.terraform/**` metadata policy path.
- The OSS `PackageRepoServiceImpl.getSubRepositories` and shared virtual resolver
  establish effective member order and flattening, consumed through ENG-08.
  JFrog's [virtual repositories](https://docs.jfrog.com/artifactory/docs/virtual-repositories)
  exempts Terraform from the originated-request upstream restriction (R7).
- HashiCorp's [module sources](https://developer.hashicorp.com/terraform/language/modules/sources)
  and [JSON syntax](https://developer.hashicorp.com/terraform/language/syntax/json)
  distinguish registry addresses from local paths, Git and URL getters. The
  official [HCL parser](https://github.com/hashicorp/hcl/releases/tag/v2.25.0)
  permits editing only static module-source expressions while retaining unrelated
  HCL bytes. Go-getter's [decompressors](https://github.com/hashicorp/go-getter/blob/main/decompress.go)
  distinguish TAR containers from single compressed files; archive hints must
  describe the delivered representation.

## Decisions and unsettled points

These are **[I]** entries for [#216](https://github.com/weaveplatform/artifactweave/issues/216).
The public evidence establishes source override, but does not settle its complete
syntax, exact archive representation or duplicate ownership.

1. A version union keeps the first eligible whole contribution in the engine's
   effective order. Equal SemVer precedence uses a lexical tie-break for stable
   output; source bytes/claims always belong to the selected member.
2. Static module `source` values in `.tf` and `.tf.json` registry addresses,
   including bare `namespace/name/system` and explicit registry hosts, become
   `public-host/requested-repo__namespace/name/system`. Preserve `//` selectors.
   Relative, Git, HTTP and other getters, dynamic expressions and non-module
   `source` attributes remain unchanged. Already routed addresses are idempotent.
3. Rewrite only delivered remote archives, through either a direct remote or a
   virtual. The cache retains exact validated upstream bytes and provenance.
   Rewritten ZIP stays ZIP; rewritten TAR/compressed TAR becomes deterministic
   tar.gz; single compressed files use gzip when changed. No edits means exact
   original bytes and archive format, and no temporary output archive. The output
   digest, format and request-specific public target are signed; delivery recomputes
   them. Derived checksums, length, ETag, ranges, HEAD and conditional responses
   describe those output bytes, never raw upstream bytes.
4. Preserve safe symlink/hardlink entries. Complete original archive/link/selector
   validation remains mandatory. Configuration parsing is bounded to 16 MiB per configuration file,
   retaining the existing archive entry, expanded-size and graph limits. Malformed
   configurations remain unchanged for Terraform to diagnose.
5. Current effective membership, signer Read, member/virtual patterns and source
   authority are rechecked before signing and delivery. Unreadable or refused
   contributions do not disclose a member; true storage faults remain faults.
   Provider and network-mirror virtual operations retain 501 pending #1564.

## Same-kind finding fixed here

Direct remote module downloads previously returned raw registry dependency
addresses, despite the documented default override since 7.39.4. The same delivery
transform now covers direct remotes. A controlled upstream regression verifies the
rewritten dependency, unchanged source cache, correct derived validators/ranges and
refusal of a changed archive hint. This is a Terraform-only defect folded into
#1550, not deferred elsewhere.

A suspected configuration-link escape proved not to be a product defect. The pinned
Terraform 1.17.1 binary embeds go-getter v1.8.6 (`go version -m`). Its TAR extractor
copies non-directory entry bodies into ordinary files, so symlink/hardlink entries
become empty files; its ZIP extractor copies the link-target text into an ordinary
file. A controlled native TAR `main.tf.json -> config.json` experiment fails on
empty JSON before any dependency lookup. See official
[TAR](https://github.com/hashicorp/go-getter/blob/v1.8.6/decompress_tar.go) and
[ZIP](https://github.com/hashicorp/go-getter/blob/v1.8.6/decompress_zip.go) readers.
The server retains safe links and complete validation; it does not claim that
Terraform follows them or reinterpret differently named backing files. No issue
was filed for this non-defect.

Cold cache fill still requires Deploy/Cache permission as intended by #1587.
[#1670](https://github.com/weaveplatform/artifactweave/issues/1670) separately tracks
trusted upstream policy paths during cache publication; this work does not change
that shared admission contract.
