@replication @clients @maven
Feature: Native package consumption after single replication
  A required pinned native JVM client resolves an indexed Maven coordinate
  from independent destination storage with a fresh client cache and a scoped
  nonadministrator account. The source is stopped before consumption.

  Scenario Outline: Native Maven consumption requires replicated package data
    Given two fixture-owned replication instances are running on "<backend>"
    Then a fresh native Maven consumer resolves the "<direction>" replicated package while its source is stopped

    Examples:
      | backend  | direction |
      | sqlite   | push      |
      | sqlite   | pull      |
      | postgres | push      |
      | postgres | pull      |
