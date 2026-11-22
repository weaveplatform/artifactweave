@replication @replication_recovery
Feature: Single replication process recovery and current authority
  A real process interruption retains unfinished SQL-backed transfer progress.
  Actual peer credential failures and pending configuration revocation must
  agree with public status and the independently stored destination bytes.

  Scenario Outline: Interrupted, partially failed and revoked single replication
    Given two fixture-owned replication instances are running on "<backend>"
    Then ordinary "<direction>" replication recovers after process interruption and refuses revoked pending work

    Examples:
      | backend  | direction |
      | sqlite   | push      |
      | sqlite   | pull      |
      | postgres | push      |
      | postgres | pull      |
