@rpm
Feature: Native RPM repository metadata and dnf install
  A real dnf client consumes the generated repodata and verifies the package checksum.

  Scenario Outline: dnf installs from a <class> RPM repository
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/<repo>/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I GET "/artifactory/<repo>/repodata/repomd.xml" as admin until the response status is 200
    Then the response status is 200
    Given the dnf repository is "<repo>"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds
    And the file "rpm-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0"

    Examples:
      | class     | repo       |
      | local     | rpm-local  |
      | federated | rpm-fed    |

  Scenario: dnf installs from a remote RPM repository and reuses the cache offline
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    And an RPM upstream serves the genuine package
    When I PUT "/artifactory/api/repositories/rpm-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"rpm","url":"{upstream}"}
      """
    Then the response status is 200
    When I GET "/artifactory/rpm-remote/repodata/repomd.xml" as admin
    Then the response status is 200
    Given the dnf repository is "rpm-remote"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds
    And the file "rpm-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0"
    When I GET "/artifactory/rpm-remote-cache/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 200
    Given the generic upstream stops
    When I clear the RPM installation root
    And I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: manual Yum metadata calculation builds a repository when automatic calculation is disabled
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I PUT "/artifactory/api/repositories/rpm-manual" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":false}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-manual/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I GET "/artifactory/rpm-manual/repodata/repomd.xml" as admin
    Then the response status is 404
    When I POST "/artifactory/api/yum/rpm-manual" as admin
    Then the response status is 200
    When I GET "/artifactory/rpm-manual/repodata/repomd.xml" as admin
    Then the response status is 200
    Given the dnf repository is "rpm-manual"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: dnf verifies GPG-signed RPM repository metadata
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I create a GPG RPM metadata signing pair
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/rpm-signed" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true,"primaryKeyPairRef":"rpm-sign"}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-signed/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I GET "/artifactory/rpm-signed/repodata/repomd.xml.asc" as admin until the response status is 200
    Then the response status is 200
    Given the dnf repository is "rpm-signed"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: invalid remote RPM metadata and content do not enter the cache
    Given the registry is running
    And a genuine RPM package is available
    And an RPM upstream serves the genuine package
    When I PUT "/artifactory/api/repositories/rpm-invalid" as admin with body:
      """
      {"rclass":"remote","packageType":"rpm","url":"{upstream}"}
      """
    Then the response status is 200
    Given the RPM upstream publishes an invalid manifest
    When I GET "/artifactory/rpm-invalid/repodata/repomd.xml" as admin
    Then the response status is 502
    When I GET "/artifactory/rpm-invalid-cache/repodata/repomd.xml" as admin
    Then the response status is 404
    Given the RPM upstream corrupts its package
    When I GET "/artifactory/rpm-invalid/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 502
    When I GET "/artifactory/rpm-invalid-cache/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 404
