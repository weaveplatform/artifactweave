// Package quota applies the descriptor's global storage quota, quotaConfig:
// a percentage of the hot blob store's volume above which positive storage
// growth is refused, and a lower percentage at which administrators are
// warned.
//
// # Role
//
// configuration module, core layer. The descriptor (core/descriptor) stores
// the policy; this package reads it back, admits writes against a measured
// volume and delivers the warnings. It reaches access only through
// access/api's Identity and the platform through its ports (decision 0044).
//
// # Inputs
//
//   - Admission.Admit: a root metadata transaction's net logical content
//     delta, from access's StorageAccounting, which the application
//     composition installs around every root transaction.
//   - configapi.StorageCapacity: the volume measurement, which the
//     composition binds to the hot blob store's local root (artifacts'
//     usage.Capacity, the volume GET /api/storageinfo reports). Object
//     storage has none.
//   - The policy document (ConfigKind), the warning state
//     (settings.SettingStorageQuotaWarning) and the work queue, all joining
//     the caller's transaction.
//   - Delivery: access's current administrators and the configured mail
//     server, read under the shared guard before any network I/O.
//
// # Outputs
//
//   - Admit: nil, kernel.ErrDiskQuota (413, 7.161.26's text) at or above
//     the limit, or kernel.ErrStorageCapacity (503) when an enabled policy
//     cannot measure its volume.
//   - WarningKind work items, one per occurrence, and their mail.
//
// # Invariants
//
//   - Admission runs inside the transaction it decides, after the
//     accounting guard, so admissions are serialized; the measurement
//     already includes the bytes of uploads still racing to commit, so no
//     interleaving of concurrent writers is admitted past the limit, and a
//     refused write rolls back with its metadata, accounting and warnings.
//   - Only positive net growth is refused: reads, deletions and shrinking
//     or net-zero writes reclaim or keep space and always pass. Project
//     quotas are applied separately in the same settlement; neither grants
//     an exception to the other.
//   - Each threshold raises one occurrence when it is crossed and is rearmed
//     only by a measurement below it, or by disabling the policy. The armed
//     state is durable, so a restart neither forgets nor repeats a warning.
//     A refused write cannot record the limit occurrence it observed, so the
//     Monitor raises it from its own transaction.
//   - Delivery suppresses an occurrence that is no longer armed, a disabled
//     policy and a warning with no deliverable enabled administrator; a
//     missing or disabled mail server retries under the queue's policy. The
//     mail password is read only for the send and never logged.
//
// # References
//
//   - artifactory.xsd QuotaConfigType, RepositoryServiceImpl.assertStorageQuota,
//     StorageServiceImpl.getStorageQuotaInfo and StorageQuotaInfo in the
//     7.161.26 sources.
//   - https://docs.jfrog.com/administration/docs/managing-disk-space-usage.md
//     and https://docs.jfrog.com/installation/docs/advanced-configurations-in-artifactory-yaml.md
//   - Research 06: docs/research/06-system-configuration-and-descriptor.md,
//     "Global storage quota".
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1830
//
// # Status
//
// P12 CFG-06.
//
// # Testing
//
//   - quota_test.go: fields, types and ranges; exact threshold edges;
//     refusal, capacity failure, growth only, transitions, rearm across a
//     restart, the monitor's limit occurrence, disable and store failures.
//   - delivery_test.go: recipients, suppression, retries, the pre-DATA
//     recheck and the message.
//   - test/acceptance/features/storage_quota.feature: real uploads against
//     the measured volume, loopback SMTP with a lost acknowledgement,
//     process restarts, project quotas alongside.
//   - internal/app/storetest/storage_quota.go: the composition's actual
//     writes on in-memory, SQLite and PostgreSQL stores, racing writers and
//     project quotas together.
package quota
