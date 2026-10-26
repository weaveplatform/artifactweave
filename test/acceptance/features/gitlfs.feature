@gitlfs
Feature: Git LFS batch and basic transfers
  Git LFS 3.8.1 publishes and resolves the same content-addressed bytes
  through native local, federated, virtual and remote repositories.

  Scenario Outline: pinned client enforces two-user Git LFS locking through a <class> repository
    Given the registry is running
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/lfs-alice" as admin with body:
      """
      {"name":"lfs-alice","email":"lfs-alice@example.invalid","password":"LFS-Alice-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/users/lfs-bob" as admin with body:
      """
      {"name":"lfs-bob","email":"lfs-bob@example.invalid","password":"LFS-Bob-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/lfs-lock-clients" as admin with body:
      """
      {"name":"lfs-lock-clients","repositories":["<repo>"],"includesPattern":"**",
       "principals":{"users":{"lfs-alice":["r","w"],"lfs-bob":["r","w","d"]}}}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I exercise two-user Git LFS locking through "<repo>"
    Then the command succeeds
    And the output contains "git-lfs/3.8.1"
    And the output contains "two-user Git LFS locking passed"

    Examples:
      | class     | repo      |
      | local     | lfs-locks |
      | federated | lfs-fed-locks |

  Scenario: pinned client enforces two-user Git LFS locking through a virtual deployment target
    Given the registry is running
    When I PUT "/artifactory/api/repositories/lfs-lock-target" as admin with body:
      """
      {"rclass":"local","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/lfs-lock-empty" as admin with body:
      """
      {"rclass":"local","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/lfs-lock-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"gitlfs","repositories":["lfs-lock-empty","lfs-lock-target"],"defaultDeploymentRepo":"lfs-lock-target"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/lfs-alice" as admin with body:
      """
      {"name":"lfs-alice","email":"lfs-alice@example.invalid","password":"LFS-Alice-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/users/lfs-bob" as admin with body:
      """
      {"name":"lfs-bob","email":"lfs-bob@example.invalid","password":"LFS-Bob-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/lfs-lock-clients" as admin with body:
      """
      {"name":"lfs-lock-clients","repositories":["lfs-lock-empty","lfs-lock-target"],"includesPattern":"**",
       "principals":{"users":{"lfs-alice":["r","w"],"lfs-bob":["r","w","d"]}}}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I exercise two-user Git LFS locking through "lfs-lock-virtual"
    Then the command succeeds
    And the output contains "two-user Git LFS locking passed"

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

  Scenario: a member-granted client publishes and reads through a virtual repository
    Given the registry is running
    When I PUT "/artifactory/api/repositories/lfs-member" as admin with body:
      """
      {"rclass":"local","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/lfs-other" as admin with body:
      """
      {"rclass":"local","packageType":"gitlfs"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/lfs-view" as admin with body:
      """
      {"rclass":"virtual","packageType":"gitlfs","repositories":["lfs-other","lfs-member"],"defaultDeploymentRepo":"lfs-member"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/lfs-writer" as admin with body:
      """
      {"name":"lfs-writer","email":"lfs-writer@example.invalid","password":"Lfs-writer-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/users/lfs-other-user" as admin with body:
      """
      {"name":"lfs-other-user","email":"lfs-other-user@example.invalid","password":"Lfs-other-123"}
      """
    Then the response status is 201
    # Virtual keys cannot be permission targets; grants apply to members.
    When I PUT "/artifactory/api/security/permissions/lfs-member-access" as admin with body:
      """
      {"name":"lfs-member-access","repositories":["lfs-member"],"includesPattern":"objects/**","principals":{"users":{"lfs-writer":["r","w"]}}}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/permissions/lfs-other-access" as admin with body:
      """
      {"name":"lfs-other-access","repositories":["lfs-other"],"includesPattern":"objects/**","principals":{"users":{"lfs-other-user":["r","w"]}}}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I publish and freshly clone a Git LFS asset through "lfs-view" as "lfs-writer" with password "Lfs-writer-123"
    Then the command succeeds
    And the output contains "git-lfs/3.8.1"
    When I attempt a Git LFS publish through "lfs-view" as "lfs-other-user" with password "Lfs-other-123"
    Then the command fails
    And the output contains "403"

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

  Scenario: remote Git LFS lock listing is fresh and fails when the upstream stops
    Given the registry is running
    And a native Git LFS upstream serves an unchanged object
    When I PUT "/artifactory/api/repositories/lfs-remote-locks" as admin with body:
      """
      {"rclass":"remote","packageType":"gitlfs","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Git LFS client can reach the registry
    When I list Git LFS locks through "lfs-remote-locks" with the pinned client
    Then the command succeeds
    And the output contains "upstream-lock-1"
    Given the native Git LFS upstream stops
    When I list Git LFS locks through "lfs-remote-locks" with the pinned client
    Then the command fails
