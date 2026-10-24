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
