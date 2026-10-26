@gitlfs
Feature: Git LFS batch and basic transfers
  Git LFS 3.8.1 publishes and resolves the same content-addressed bytes
  through native local, federated, virtual and remote repositories.

  Scenario Outline: the pinned client publishes and freshly clones through a <class> repository
    Given the registry is running
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"gitlfs"}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I publish and freshly clone a Git LFS asset through "<repo>"
    Then the command succeeds
    And the output contains "git-lfs/3.8.1"

    Examples:
      | class     | repo      |
      | local     | lfs-local |
      | federated | lfs-fed   |

  Scenario: the pinned client publishes through a virtual deploy target
    Given the registry is running
    When I PUT "/artifactory/api/repositories/lfs-target" as admin with body:
      """
      {"rclass":"local","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/lfs-empty" as admin with body:
      """
      {"rclass":"local","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/lfs-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"gitlfs","repositories":["lfs-empty","lfs-target"],"defaultDeploymentRepo":"lfs-target"}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I publish and freshly clone a Git LFS asset through "lfs-virtual"
    Then the command succeeds
    And the output contains "git-lfs/3.8.1"

  Scenario: the pinned client resolves a remote object through the cache while offline
    Given the registry is running
    And a native Git LFS upstream serves an unchanged object
    When I PUT "/artifactory/api/repositories/lfs-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"gitlfs","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I fetch the upstream Git LFS asset through "lfs-remote" in a fresh client "cold"
    Then the command succeeds
    Given the native Git LFS upstream stops
    When I fetch the upstream Git LFS asset through "lfs-remote" in a fresh client "warm"
    Then the command succeeds
