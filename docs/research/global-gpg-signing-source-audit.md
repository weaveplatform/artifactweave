# Instance GPG signing audit (#1654)

Audited 2026-11-19; phase ownership reconciled with the plan on the same date. **[D]** documentation; **[S]** verified OSS source; **[I]** inference or unsettled current wire behavior. Read-only research; no vendor instance, private material, or public issue write. #1654 remains separate from P10 sign-off.

## Coverage and contradictions

- **[D]** [Security Keys Management](https://docs.jfrog.com/administration/docs/security-keys-management) identifies **GPG for Debian, RPM/YUM and Opkg**, and **RSA for Alpine** local, federated and virtual indexes. All four sign metadata. Its current text does **not** state the claimed global fallback rule. The repository spec's “Debian and Alpine” description contradicts this format distinction.
- **[D]** [Alpine](https://docs.jfrog.com/artifactory/docs/alpine-linux-repositories) requires an assigned RSA pair or `--allow-untrusted`; the virtual repository signs with its own RSA pair and ignores local-member pairs. No evidence supports converting an uploaded OpenPGP key to Alpine PEM/RSA material or using the global GPG key there.
- **[D]** [Debian](https://docs.jfrog.com/artifactory/docs/debian-repositories#sign-debian-metadata) signs `Release.gpg` and `InRelease`; [RPM](https://docs.jfrog.com/artifactory/docs/rpm-repositories#sign-rpm-metadata) signs metadata; [Opkg](https://docs.jfrog.com/artifactory/docs/opkg-repositories) signs indexes using GPG. These pages alone do not settle fallback timing or all repository classes.
- **[D]** The [Debian KB](https://jfrog.com/help/r/artifactory-how-to-use-gpg-keys-with-artifactory-debian-repositories), dated 2025-10-21, says a virtual repository with no assigned key produces unsigned metadata. This qualifies the issue's unconditional signing-time fallback assumption; it does not describe whether an instance default existed.

## API contract

- **[D]** [PUT private](https://docs.jfrog.com/administration/reference/setgpgprivatekey): admin; Pro; since 3.3; default key for Debian/RPM/Opkg. Body is private-key content described as PEM/PGP. Success is **200 text/plain** confirmation; listed errors are **400** invalid format, **401** bad credentials, **403** non-admin, **500**. The page ambiguously mentions JSON/header submission and `--data-binary`, without a concrete header or JSON envelope. Exact accepted request MIME/envelope and success text remain **[I]**.
- **[D]** [GET public](https://docs.jfrog.com/administration/reference/getgpgpublickey): **200 text/plain**, **404 when no public key is configured**, 401/403/500 listed. Authenticated users **or anonymous when global Anonymous Access is enabled** may read it. Thus blanket anonymous=401 acceptance conflicts with the documented enabled-anonymous case. Exact challenge/error envelope is **[I]**.
- **[D]** [PUT public](https://docs.jfrog.com/administration/reference/setGpgPublicKey) separately stores the default public key. [PUT passphrase](https://docs.jfrog.com/administration/reference/setgpgpassphrase) separately accepts required `X-GPG-PASSPHRASE`, admin-only, 200/400/401/403/500. Private-only upload deriving a readable public key, encrypted-key acceptance before passphrase installation, expiry/algorithm validation, atomic failure behavior, and mismatched public/private handling are **[I]**.

## Exact OSS evidence

Reused [official 7.161.26 archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz), SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`; no download. Anchors below are relative to `.tools/p10-oss7/jfrog-artifactory-7.161.26/`.

- **[S]** `base/config/src/main/java/org/artifactory/descriptor/repo/KeyPairDescriptor.java:47–57`: separate RSA/GPG types; `default-gpg-key`; GPG repository types exactly Debian/YUM/Opkg. `default-lifecycle-key` is separate.
- **[S]** `web/rest-ui/src/main/java/org/artifactory/ui/rest/service/admin/configuration/repositories/util/builder/RepoConfigDescriptorBuilder.java:391–417`: valid repository `keypair`, then `primaryKeyPair`, wins; if no primary, assigns existing `default-gpg-key` for those three formats. Calls cover local **222–233**, federated **245–258**, virtual **277–286**; remote **261–274** has no helper. This proves configuration-time default assignment, **not** runtime fallback for every existing/ref-less repository or every REST creation path.
- **[S]** `base/config/src/main/java/org/artifactory/version/converter/v319/GpgToDefaultGpgKeyPairConverter.java:39–53,69–108,111–146` migrates legacy key files into the named default and assigns references to local/virtual GPG repositories. This is upgrade logic, not contemporary signer ordering.
- **[S]** `web/rest-ui/src/main/java/org/artifactory/ui/rest/service/admin/security/signingkeys/InstallSigningKeyService.java:141–162` creates/updates that named pair, changing only the public **or** private field; no derivation is visible. `GetSigningKeyService.java:63–75` independently checks public/private presence.
- **[S]** `backend/core/src/main/java/org/artifactory/repo/confighandler/KeyPairsConfigChangeEventHandler.java:44–50,58–79` reloads referencing local/federated/virtual repositories after pair updates. It does not prove eager re-signing. Actual GPG REST resource, `KeyPairService` implementation and commercial signers are absent; `base/common/src/main/java/org/artifactory/addon/common/keys/KeyPairService.java:19–31,94–102` exposes interfaces only.

## Replacement and remaining comparison

**[D]** The Debian KB requires reindexing after assigning/rotating the active key; it says deployment triggers reindexing and recommends explicit recalculation for immediate verification. **[I]** Global replacement's exact invalidation, existing-signature lifetime, RPM/Opkg reindex triggers, dangling/wrong-type/invalid assigned-pair behavior, and signing-time default lookup remain unobserved.

**[D]** [GPG Signing](https://docs.jfrog.com/artifactory/docs/gpg-signing) concerns Distribution Release Bundles, not this repository fallback. [Current releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases) reach 7.173.21 (2026-11-17); detailed recent fixes require authentication. Bounded first-party release/KB/public-Jira searches found no evidence settling the remaining wire questions. Keep any comparison payload local pending exact authorization; no #216 post was made.

## Phase ownership and implementation readiness

[P23 task 2](../implementation/plan.md#p23-evidence-and-provenance) owns the remaining package-signing administration APIs. #1654 can be pulled forward independently: the sealed named-key store and Debian/RPM/Opkg local, federated and virtual metadata signers already exist. It does not require the Evidence/DSSE service, P15 Distribution, or the P20 inventory to be implemented first. P28 explicitly permits independent signing work to land earlier. This is phase allocation and research evidence, not an implementation-completion claim.

The related cross-kind public-material privacy fix [#1727](https://github.com/weaveplatform/artifactweave/issues/1727) was delivered in merged [PR #1729](https://github.com/weaveplatform/artifactweave/pull/1729); retain its validation/export protections. The Opkg-only virtual public-key route correction stays within #1654. Any new cross-kind defect must receive its own deduplicated ticket under the existing review convention.
