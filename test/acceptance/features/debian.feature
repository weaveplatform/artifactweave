@debian
Feature: Debian repositories deliver signed apt metadata and real packages
  A fresh apt client verifies the InRelease signature and package checksums
  before it installs a dpkg-built package from a local or federated repository.

  Scenario Outline: apt installs a signed package from a <class> repository
    Given the registry is running
    And the Debian client can reach the registry
    When I create a Debian signing pair "deb-sign"
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"debian","primaryKeyPairRef":"deb-sign"}
      """
    Then the response status is 200
    Given a dpkg-built Debian package "alpha" at "1.0" under "debian"
    When I deploy the file "debian/alpha_1.0_amd64.deb" to "/artifactory/<repo>/pool/main/a/alpha/alpha_1.0_amd64.deb;deb.distribution=stable;deb.component=main;deb.architecture=amd64" as admin
    Then the response status is 201
    When I GET "/artifactory/<repo>/dists/stable/InRelease" as admin until the response status is 200
    Then the response status is 200
    Given the apt repository is "<repo>"
    When I run apt "update"
    Then the command succeeds
    When I run apt "install -y alpha"
    Then the command succeeds
    And the response body contains "Setting up alpha"

    Examples:
      | class     | repo      |
      | local     | deb-local |
      | federated | deb-fed   |

  @debian_remote
  Scenario: apt installs through a remote cache and reuses it offline
    Given the registry is running
    And the Debian client can reach the registry
    When I create a Debian signing pair "deb-sign"
    Then the response status is 201
    Given a Debian upstream serves "alpha" at "1.0"
    When I PUT "/artifactory/api/repositories/deb-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"debian","url":"{upstream}"}
      """
    Then the response status is 200
    When I GET "/artifactory/deb-remote/dists/stable/InRelease" as admin
    And the generic upstream received 1 GET requests for "/dists/stable/InRelease"
    Then the response status is 200
    Given the apt repository is "deb-remote"
    When I run apt "update"
    Then the command succeeds
    When I run apt "install -y alpha"
    Then the command succeeds
    And the response body contains "Setting up alpha"
    When I GET "/artifactory/deb-remote-cache/pool/main/a/alpha/alpha_1.0_amd64.deb" as admin
    Then the response status is 200
    Given the generic upstream stops
    When I clear the apt client cache
    And I run apt "update"
    Then the command succeeds
    When I run apt "install -y alpha"
    Then the command succeeds
    And the response body contains "Setting up alpha"

  Scenario: invalid remote metadata and package bytes never enter cache
    Given the registry is running
    When I create a Debian signing pair "deb-sign"
    Then the response status is 201
    Given a Debian upstream serves "alpha" at "1.0"
    When I PUT "/artifactory/api/repositories/deb-bad-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"debian","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Debian upstream publishes an invalid Packages index
    When I GET "/artifactory/deb-bad-remote/dists/stable/main/binary-amd64/Packages.gz" as admin
    Then the response status is 502
    When I GET "/artifactory/deb-bad-remote-cache/dists/stable/main/binary-amd64/Packages.gz" as admin
    Then the response status is 404
    Given the Debian upstream corrupts "alpha" at "1.0"
    When I GET "/artifactory/deb-bad-remote/pool/main/a/alpha/alpha_1.0_amd64.deb" as admin
    Then the response status is 502
    When I GET "/artifactory/deb-bad-remote-cache/pool/main/a/alpha/alpha_1.0_amd64.deb" as admin
    Then the response status is 404

  Scenario: apt downloads a complete verified Debian source package
    Given the registry is running
    And the Debian client can reach the registry
    When I create a Debian signing pair "deb-sign"
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/deb-source" as admin with body:
      """
      {"rclass":"local","packageType":"debian","primaryKeyPairRef":"deb-sign"}
      """
    Then the response status is 200
    Given a dpkg-built Debian source package under "source"
    When I deploy the file "source/alpha_1.0-1.dsc" to "/artifactory/deb-source/pool/main/a/alpha/alpha_1.0-1.dsc;dsc.distribution=stable;dsc.component=main" as admin
    Then the response status is 201
    When I GET "/artifactory/deb-source/dists/stable/main/source/Sources" as admin
    Then the response status is 404
    When I deploy the file "source/alpha_1.0.orig.tar.gz" to "/artifactory/deb-source/pool/main/a/alpha/alpha_1.0.orig.tar.gz" as admin
    Then the response status is 201
    When I deploy the file "source/alpha_1.0-1.debian.tar.xz" to "/artifactory/deb-source/pool/main/a/alpha/alpha_1.0-1.debian.tar.xz" as admin
    Then the response status is 201
    When I GET "/artifactory/deb-source/dists/stable/main/source/Sources" as admin until the response status is 200
    Then the response status is 200
    When I POST "/artifactory/api/deb/reindex/deb-source?writeProps=0" as admin
    Then the response status is 200
    And the response body contains "metadata recalculation accepted"
    When I GET "/artifactory/deb-source/dists/stable/main/source/Sources" as admin
    Then the response status is 200
    Given the apt source repository is "deb-source"
    When I run apt "update"
    Then the command succeeds
    When I run apt "source --download-only alpha"
    Then the command succeeds
    And the file "alpha_1.0-1.dsc" exists
    And the file "alpha_1.0.orig.tar.gz" exists
    And the file "alpha_1.0-1.debian.tar.xz" exists
