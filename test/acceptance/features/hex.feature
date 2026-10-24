@hex
Feature: Signed Hex registry with Mix
  A Hex client verifies registry metadata and fetches an unchanged upstream
  decimal tarball through the native endpoint.

  Scenario: Local publish and dependency resolution
    Given the registry is running
    And the Hex client can reach the registry
    When I create the RSA key pair "hex-key" with alias "hex" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/hex-local" as admin with body:
      """
      {"rclass":"local","packageType":"hex","primaryKeyPairRef":"hex-key"}
      """
    Then the response status is 200
    Given a Hex project for repository "hex-local"
    When I deploy the file "decimal-2.0.0.tar" to "/artifactory/hex-local/tarballs/decimal-2.0.0.tar" as admin
    Then the response status is 201
    When I run mix "hex.repo add hex-local {authurl}/api/hex/hex-local --public-key=/work/hex-local.pem"
    Then the command succeeds
    When I run mix "deps.get"
    Then the command succeeds
    When I run mix "deps.compile"
    Then the command succeeds
    And the file "deps/decimal/mix.exs" exists

  Scenario: Virtual repository resolves an ordered local member
    Given the registry is running
    And the Hex client can reach the registry
    When I create the RSA key pair "hex-key" with alias "hex" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/hex-local" as admin with body:
      """
      {"rclass":"local","packageType":"hex","primaryKeyPairRef":"hex-key"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/hex-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"hex","repositories":["hex-local"],"keyPair":"hex-key"}
      """
    Then the response status is 200
    Given a Hex project for repository "hex-virtual"
    When I deploy the file "decimal-2.0.0.tar" to "/artifactory/hex-local/tarballs/decimal-2.0.0.tar" as admin
    Then the response status is 201
    When I run mix "hex.repo add hex-virtual {authurl}/api/hex/hex-virtual --public-key=/work/hex-virtual.pem"
    Then the command succeeds
    When I run mix "deps.get"
    Then the command succeeds
    And the file "deps/decimal/mix.exs" exists

  @hex_remote
  Scenario: Remote cache survives upstream outage with a fresh Hex client
    Given the registry is running
    And the Hex client can reach the registry
    When I create the RSA key pair "hex-key" with alias "hex" as admin
    Then the response status is 201
    Given a signed Hex upstream serves decimal 2.0.0
    When I PUT "/artifactory/api/repositories/hex-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"hex","url":"{upstream}/hex/","primaryKeyPairRef":"hex-key","hexPublicKey":{hexUpstreamKeyJSON}}
      """
    Then the response status is 200
    Given a Hex project for repository "hex-remote"
    When I run mix "hex.repo add hex-remote {authurl}/api/hex/hex-remote --public-key=/work/hex-remote.pem"
    Then the command succeeds
    When I run mix "deps.get"
    Then the command succeeds
    And the file "deps/decimal/mix.exs" exists

    And the generic upstream received 1 GET requests for "/hex/redirect/decimal-2.0.0.tar"
    When I POST "/artifactory/api/repositories/hex-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    And the Hex client cache is cleared
    When I run mix "hex.repo add hex-remote {authurl}/api/hex/hex-remote --public-key=/work/hex-remote.pem"
    Then the command succeeds
    When I run mix "deps.get"
    Then the command succeeds
    And the file "deps/decimal/mix.exs" exists

  @hex_remote
  Scenario: Virtual repository falls through to a signed remote member
    Given the registry is running
    And the Hex client can reach the registry
    When I create the RSA key pair "hex-key" with alias "hex" as admin
    Then the response status is 201
    Given a signed Hex upstream serves decimal 2.0.0
    When I PUT "/artifactory/api/repositories/hex-local" as admin with body:
      """
      {"rclass":"local","packageType":"hex","primaryKeyPairRef":"hex-key"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/hex-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"hex","url":"{upstream}/hex/","primaryKeyPairRef":"hex-key","hexPublicKey":{hexUpstreamKeyJSON}}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/hex-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"hex","repositories":["hex-local","hex-remote"],"keyPair":"hex-key"}
      """
    Then the response status is 200
    Given a Hex project for repository "hex-virtual"
    When I run mix "hex.repo add hex-virtual {authurl}/api/hex/hex-virtual --public-key=/work/hex-virtual.pem"
    Then the command succeeds
    When I run mix "deps.get"
    Then the command succeeds
    And the file "deps/decimal/mix.exs" exists

  @hex_deps
  Scenario: Remote Ecto resolves required dependencies through the same proxy
    Given the registry is running
    And the Hex client can reach the registry
    When I create the RSA key pair "hex-key" with alias "hex" as admin
    Then the response status is 201
    Given a signed Hex upstream serves Ecto and its required dependencies
    When I PUT "/artifactory/api/repositories/hex-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"hex","url":"{upstream}/hex/","primaryKeyPairRef":"hex-key","hexPublicKey":{hexUpstreamKeyJSON}}
      """
    Then the response status is 200
    Given a Hex Ecto project for repository "hex-remote"
    When I run mix "hex.repo add hex-remote {authurl}/api/hex/hex-remote --public-key=/work/hex-remote.pem"
    Then the command succeeds
    When I run mix "deps.get"
    Then the command succeeds
    When I run mix "deps.compile"
    Then the command succeeds
    And the file "deps/ecto/mix.exs" exists
    And the file "deps/telemetry/mix.exs" exists
    And the file "deps/decimal/mix.exs" exists
    And the generic upstream received 1 GET requests for "/hex/tarballs/ecto-3.12.5.tar"
    And the generic upstream received 1 GET requests for "/hex/tarballs/telemetry-1.3.0.tar"
