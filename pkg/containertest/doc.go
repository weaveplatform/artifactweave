// Package containertest gates the tests that start containers (decision
// 0015) and starts the blob-store emulators, the LDAP directory and the SMTP
// server they use.
//
// # Role
//
// Test support, outside the modules, for every package whose tests need
// Docker. They need a Docker daemon that runs Linux containers: GitHub's
// Windows runners have a healthy Docker that runs only Windows containers,
// so testcontainers' own health check passes and every Linux image then
// fails to start. Those tests skip there and run on Linux and on macOS with
// Docker Desktop.
//
// # Inputs
//
//   - The Docker engine that testcontainers finds, asked once per test
//     binary, within AnswerTimeout, whether it runs Linux containers.
//   - The -short flag: a short run skips every container test.
//
// # Outputs
//
//   - RequireLinux, which skips a test, and CheckLinux, which returns an
//     error, when no usable engine answers.
//   - MinIO, FakeGCS and Azurite, and RunMinIO, RunFakeGCS and RunAzurite
//     for callers without a testing.T: one emulator container per caller,
//     from an image pinned by digest, removed when the caller ends. MinIO
//     is handed out only once it serves S3 requests, not merely HTTP. Each
//     image is pulled before its module starts, one pull per image at a
//     time in the process. A pull that fails moves on to the image's second
//     source, a registry serving the same digest (for Azurite, the
//     project's copy on GHCR, which .github/workflows/emulator-copy.yml
//     makes), and when every source fails the error names each registry's
//     answer rather than the "No such image" testcontainers' own pull ends
//     in.
//   - OpenLDAP and RunOpenLDAP: one OpenLDAP container (osixia/openldap,
//     pinned by digest, with mirror.gcr.io's copy as its second source)
//     serving LDAP and LDAPS, as an LDAPEndpoint: its ports, base DN, the
//     administrator and a read-only reader with their passwords, and the CA
//     pool its fresh, per-container certificate chains to. Anonymous binds
//     read nothing.
//   - Postgres and RunPostgres: a PostgreSQL 17 server of the caller's own
//     (postgres:17-alpine pinned by digest, mirror.gcr.io's copy as its
//     second source), for a caller that must remove its container, such as
//     the multi-instance harness in test/peers; and Removed, which proves the
//     engine no longer has a container.
//   - Mailpit and RunMailpit: a real SMTP server with AUTH and STARTTLS over
//     a certificate from a CA made per run (MailpitEndpoint.CA), with
//     Messages and Clear over its message API, so mail tests check what an
//     independent server received. Its second source is mirror.gcr.io.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - Decision 0027 and research 18: docs/research/18-cloud-blob-storage.md,
//     the emulators the cloud blob backends are tested against.
//   - https://github.com/weaveplatform/artifactweave/issues/458: an engine
//     that accepts connections and never answers.
//   - https://github.com/weaveplatform/artifactweave/issues/1307: MinIO
//     answering CreateBucket with 503 XMinioServerNotInitialized after the
//     module's liveness wait passed.
//   - https://github.com/weaveplatform/artifactweave/issues/1448 and
//     https://github.com/testcontainers/testcontainers-go/issues/3945: a
//     pull whose failure the engine reports inside its 200 answer, which
//     testcontainers-go does not decode.
//   - https://github.com/weaveplatform/artifactweave/issues/1463: Azurite's
//     copy, its second source.
//   - https://github.com/weaveplatform/artifactweave/issues/1833: the LDAP
//     directory.
//   - https://github.com/weaveplatform/artifactweave/issues/1831: Mailpit,
//     for mail test and quota warning delivery.
//   - https://github.com/weaveplatform/artifactweave/issues/1924: the
//     PostgreSQL server the multi-instance harness owns.
//
// # Status
//
// Implemented P2 (the Linux engine gate), P2b (the emulators) and P7 (the
// answer timeout). MinIO's readiness wait added by issue 1307; the checked
// image pull and the second sources by issue 1448; Azurite's copy by issue
// 1463; OpenLDAP by issue 1833 (P12); Mailpit by issue 1831; PostgreSQL by
// issue 1924 (P13).
package containertest
