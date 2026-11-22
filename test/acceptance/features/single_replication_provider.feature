@replication @clients @terraform @replication_provider
Feature: Pinned provider configuration drives real single replication
  The native JFrog Artifactory provider creates and updates both resources on
  one owned application. Execution uses nonadministrator peer credentials and
  publishes bytes into a second owned application or the owning remote cache.

  Scenario Outline: Provider-managed single replication transfers and destroys
    Given two fixture-owned replication instances are running on "<backend>"
    Then the pinned provider applies, updates, executes and destroys both single replication resources

    Examples:
      | backend  |
      | sqlite   |
      | postgres |
