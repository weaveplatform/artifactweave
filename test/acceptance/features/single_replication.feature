@replication
Feature: Ordinary single replication between independently owned instances
  Scheduled and manual replication must transfer into another application's
  repository or the owning remote cache. The fixture establishes independent
  storage before direction, provider, native-client and recovery qualification.

  Scenario Outline: Replication peers use independent SQL-backed storage
    Given two fixture-owned replication instances are running on "<backend>"
    Then the replication instances retain independent repository bytes

    Examples:
      | backend  |
      | sqlite   |
      | postgres |
