// Package peers runs multi-instance scenarios: several artifactweave
// instances that reach each other, and later pair, federate and converge.
//
// # Role
//
// Test support, outside the modules. The acceptance-peers job runs its
// scenarios on Linux with go test directly (plan P13, QUA-01); the package
// acceptance lane leaves it out.
//
// # Inputs
//
//   - Start's Options: how many instances, SQLite or PostgreSQL, and how
//     often each reads its peers' status.
//   - The test binary itself: every instance is a process of it running
//     InstanceTest, which the binary declares by calling RunInstance.
//   - Docker, for a PostgreSQL cluster's own container.
//   - ARTIFACTWEAVE_TEST_PROCESS_LOG_DIR, where the server logs are kept
//     when it is set.
//
// # Outputs
//
//   - A Cluster: Instances, each on its own loopback port, with its own
//     data directory, database, instance identity and administrator
//     password; an ingress per instance, a forwarder whose address is its
//     base URL, the one address every other instance and the scenario
//     reach it at; an egress per instance, the forward proxy its server
//     reaches the others through; Cut and Restore, Partition and Heal,
//     which refuse destinations at the egress; DoFrom; Stop, which stops
//     one instance; Close and Leftovers; on each Instance, Identity, Peer
//     and SetPeerKey over its database.
//   - Each instance's coverage in the binary's -test.gocoverdir.
//   - Each instance's server log in a failing scenario's output.
//   - The scenarios: the harness's own (harness_test.go), pairing
//     through service_trust (pairing_test.go), federated members
//     (federated_test.go) and peer liveness (liveness_test.go); the P13
//     peer and federation tickets add theirs.
//
// # References
//
//   - Plan:
//     docs/implementation/plan.md#p13-peer-trust-and-federated-repositories,
//     QUA-01.
//   - docs/testing/ci.md, "Multi-instance scenarios (acceptance-peers)":
//     the conventions on ports, data directories, base URLs and partitions.
//   - P11's two-process pattern: test/server/federation/push_process_test.go.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Repository
//     root", line 63, and "Tests and enforcement", line 852.
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1924
//   - https://github.com/weaveplatform/artifactweave/issues/1946: one
//     ingress address per instance, partitions by source.
//
// # Status
//
// Implemented P13 (QUA-01, issue 1924): the harness and the acceptance-peers
// job; issue 1946 gave each instance one ingress address and moved PEER-02's
// pairing scenarios here. The other peer and federation scenarios come with
// their tickets.
package peers
