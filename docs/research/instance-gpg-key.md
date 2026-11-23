# Instance-wide GPG signing key

Research for [#1654](https://github.com/weaveplatform/artifactweave/issues/1654): the
four `/api/gpg` operations and the signing fallback for Debian, RPM and Opkg metadata.
Items no source settles are marked **[I]** and listed on
[#216](https://github.com/weaveplatform/artifactweave/issues/216) for the P19 comparison.

## Sources

- The REST reference pages for
  [setGpgPrivateKey](https://docs.jfrog.com/administration/reference/setgpgprivatekey),
  [setGpgPublicKey](https://docs.jfrog.com/administration/reference/setgpgpublickey),
  [setGpgPassPhrase](https://docs.jfrog.com/administration/reference/setgpgpassphrase) and
  [getGpgPublicKey](https://docs.jfrog.com/administration/reference/getgpgpublickey).
  The private and public keys are the default keys "Artifactory uses to sign Debian, RPM,
  and ipk (Opkg) packages" and that it "provides to Debian, RPM, and Opkg clients to verify
  packages". The three writes are admin-only and answer `text/plain` 200 with "a
  confirmation message", and 400 for an invalid key or pass phrase. The read allows any
  authenticated user, or anonymous while anonymous access is on, and answers 404 when "No
  GPG public key has been configured".
- [Security Keys Management](https://docs.jfrog.com/administration/docs/security-keys-management)
  lists GPG metadata signing for Debian, Opkg and YUM/RPM, and puts Alpine under RSA keys.
- The [official 7.161.26 OSS source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz),
  SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`:
  - `KeyPairDescriptor`: `DEFAULT_GPG_KEY = "default-gpg-key"`, `GPG_TYPE = "GPG"`,
    `GPG_REPO_TYPES = List.of(RepoType.Debian, RepoType.YUM, RepoType.Opkg)`,
    `GPG_PUBLIC_KEY_EXTENSION = ".gpg.pub"`.
  - `InstallSigningKeyService.onInstallKey` (UI upload, the same operation the REST writes
    describe). It reads the `default-gpg-key` pair. If the pair is absent, it creates it with
    name and alias `default-gpg-key`, type GPG and only the uploaded half. If the pair is
    present, it sets that one half and keeps the rest (`updateKeyPair`). Its messages are
    "Public key is installed" and "Private key is installed".
  - `UpdateSigningKeyService`: the passphrase is set on the same pair, with the message
    "Successfully updated signing pass-phrase". On an absent pair it answers "Failed to
    update a non existed key".
  - `GetSigningKeyService`: the public key counts as installed when the pair's public key
    is not blank.
  - `RepoConfigDescriptorBuilder.setDefaultGpgKeyIfNeeded`: when the UI saves a local,
    federated or virtual repository of a `GPG_REPO_TYPES` type with no
    `primaryKeyPairRef`, it assigns `default-gpg-key`.
  - `GpgToDefaultGpgKeyPairConverter` (config 3.19): the old `artifactory.gpg.public` and
    `artifactory.gpg.private` files became the `default-gpg-key` pair, referenced by every
    local and virtual Debian, YUM and Opkg repository.
- The [Debian signing KB](https://jfrog.com/help/r/artifactory-how-to-use-gpg-keys-with-artifactory-debian-repositories):
  metadata is recalculated after a key is assigned or rotated.

## What artifactweave does

- **One named pair.** The instance key is the key pair called `default-gpg-key` (type
  GPG, alias `default-gpg-key`), as in 7.x. Because it is an ordinary pair, the key pair
  API lists, reads and deletes it. Each `/api/gpg` write sets one field in one statement:
  the private key, public key or passphrase. The other fields are kept, and the pair is
  created on the first write. The private key and passphrase are sealed at rest (decision
  0017), and no operation returns them.
- **Validation.** The private key must be one ASCII-armored OpenPGP key holding a private
  key. The public key passes the same complete-field public check as every key pair
  (#1727): public armor only, no private packets, no trailing blocks. Each invalid value
  answers 400 and stores nothing. A pair named `default-gpg-key` of another type is
  refused with 400 rather than mixed. **[I]**
- **Messages.** The writes answer "Private key is installed", "Public key is installed"
  and "Successfully updated signing pass-phrase", the 7.161.26 UI services' texts. **[I]**
  The REST pages give no text. A passphrase set before any key creates the pair holding
  only the passphrase, where the UI refuses. **[I]**
- **The public key read.** `getGpgPublicKey` answers the stored public key as
  `text/plain`, or 404 `No GPG public key has been configured` while none is set. **[I]**
  The message is the page's response description.
- **Signing fallback.** Debian, RPM and Opkg repositories sign with their own pair:
  `primaryKeyPairRef` for local and federated repositories, `keyPair` for virtual ones.
  When that field is empty, they sign with the instance key. This happens when the metadata
  is built, not when the repository is saved. **[I]** 7.161.26's UI writes the reference
  at save time, but no source shows the REST repository API doing so. Choosing at signing
  time leaves repository configuration answers unchanged and covers repositories created
  before the key. A repository's own reference is always used first, including its errors
  (a named pair that does not exist still fails as before).
- **When the instance key signs.** The instance key signs only once both halves are set
  and the private key unlocks with the stored passphrase and matches the public key. Until
  then, metadata stays unsigned, as it is with no key at all. **[I]** No source says how
  7.x signs with a partly configured default key.
- **Formats.** Alpine signs with RSA and never falls back to the GPG instance key. Remote
  repositories build no metadata.
- **Public key per repository.** `GET /api/security/keypair/public/repositories/{repo}`
  answers the instance key, as `default-gpg-key.gpg.pub`, for a Debian, RPM or Opkg
  repository that signs with it. A virtual Opkg repository's own key is now read from
  `keyPair`, which its feed signs with, not from `primaryKeyPairRef`.
- **Timing.** Metadata keeps its normal timing. A local repository picks up a new or
  rotated instance key at its next index calculation (deploy, delete or reindex). A
  virtual repository picks it up when its merged metadata is next built, which is after
  its `virtualRetrievalCachePeriodSecs` cache of the previous build expires.
