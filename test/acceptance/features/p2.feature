@p2
Feature: Eclipse P2 update sites
  Eclipse Platform 4.41's headless director resolves and installs a published
  GLSP 2.7.0 source bundle through the repository URL.

  Scenario: a remote composite proxies an absolute external child and installs offline
    Given the registry is running
    And the P2 client can reach the registry
    And a composite Eclipse P2 site points to the published child upstream
    When I PUT "/artifactory/api/repositories/p2-composite" as admin with body:
      """
      {"rclass":"remote","packageType":"p2","url":"{upstream}","p2ChildOrigins":["{p2childorigin}"]}
      """
    Then the response status is 200
    Given the P2 repository is "p2-composite"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the file "p2-install/plugins/org.eclipse.glsp.layout.source_2.7.0.jar" exists
    And the installed P2 bundle matches the published SHA256
    And the Eclipse P2 child was fetched only by the registry
    Given both Eclipse P2 upstreams stop
    And I clear the Eclipse P2 client state
    And the P2 repository is "p2-composite-cache"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the installed P2 bundle matches the published SHA256

  Scenario: a virtual P2 repository keeps external remote and local Generic members ordered
    Given the registry is running
    And the P2 client can reach the registry
    And a composite Eclipse P2 site points to the published child upstream
    When I PUT "/artifactory/api/repositories/p2-external-member" as admin with body:
      """
      {"rclass":"remote","packageType":"p2","url":"{upstream}","p2ChildOrigins":["{p2childorigin}"]}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/p2-local-member" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given the published Eclipse P2 update site is under "p2-site"
    When I deploy the files under "p2-site" to "/artifactory/p2-local-member" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/p2-ordered" as admin with body:
      """
      {"rclass":"virtual","packageType":"p2","repositories":["p2-external-member","p2-local-member"]}
      """
    Then the response status is 200
    Given the P2 repository is "p2-ordered"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the installed P2 bundle matches the published SHA256
    And the Eclipse P2 child was fetched only by the registry

  Scenario: a composite child with a wrong published checksum cannot install
    Given the registry is running
    And the P2 client can reach the registry
    And a composite Eclipse P2 site points to the published child upstream
    And the Eclipse P2 child serves a valid JAR with the wrong published checksum
    When I PUT "/artifactory/api/repositories/p2-wrong-digest" as admin with body:
      """
      {"rclass":"remote","packageType":"p2","url":"{upstream}","p2ChildOrigins":["{p2childorigin}"]}
      """
    Then the response status is 200
    Given the P2 repository is "p2-wrong-digest"
    When I run the Eclipse P2 director "install"
    Then the command fails
    And the file "p2-install/plugins/org.eclipse.glsp.layout.source_2.7.0.jar" does not exist

  Scenario: a remote P2 repository lists and installs a published Eclipse bundle
    Given the registry is running
    And the P2 client can reach the registry
    And the published Eclipse P2 update site is available upstream
    When I PUT "/artifactory/api/repositories/p2-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"p2","url":"{upstream}"}
      """
    Then the response status is 200
    Given the P2 repository is "p2-remote"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the file "p2-install/plugins/org.eclipse.glsp.layout.source_2.7.0.jar" exists
    And the installed P2 bundle matches the published SHA256
    Given the generic upstream stops
    And the P2 repository is "p2-remote-cache"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"

  Scenario: a virtual P2 repository serves a local Generic update site
    Given the registry is running
    And the P2 client can reach the registry
    When I PUT "/artifactory/api/repositories/p2-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given the published Eclipse P2 update site is under "p2-site"
    When I deploy the files under "p2-site" to "/artifactory/p2-generic" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/p2-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"p2","repositories":["p2-generic"]}
      """
    Then the response status is 200
    Given the P2 repository is "p2-virtual"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the file "p2-install/plugins/org.eclipse.glsp.layout.source_2.7.0.jar" exists
    And the installed P2 bundle matches the published SHA256

  Scenario: a remote P2 repository resolves an update-site archive suffix
    Given the registry is running
    And the P2 client can reach the registry
    And the published Eclipse P2 update site is available as an archive upstream
    When I PUT "/artifactory/api/repositories/p2-archive" as admin with body:
      """
      {"rclass":"remote","packageType":"p2","url":"{upstream}/site.zip!/"}
      """
    Then the response status is 200
    Given the P2 repository is "p2-archive"
    When I run the Eclipse P2 director "list"
    Then the command succeeds
    And the output contains "org.eclipse.glsp.layout.source"
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the file "p2-install/plugins/org.eclipse.glsp.layout.source_2.7.0.jar" exists
    And the installed P2 bundle matches the published SHA256
