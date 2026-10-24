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

  Scenario: RPM upload requires the package's declared path and identity
    Given the registry is running
    And a genuine RPM package is available
    When I PUT "/artifactory/api/repositories/rpm-path" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-path/pool/wrong-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 400
    When I GET "/artifactory/rpm-path/repodata/repomd.xml" as admin
    Then the response status is 404

  Scenario: dnf installs an RPM fetched through a same-origin upstream redirect
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    And an RPM upstream serves the genuine package
    And the RPM upstream redirects its package
    When I PUT "/artifactory/api/repositories/rpm-redirect" as admin with body:
      """
      {"rclass":"remote","packageType":"rpm","url":"{upstream}"}
      """
    Then the response status is 200
    Given the dnf repository is "rpm-redirect"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: dnf installs from an ordered virtual RPM repository
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I PUT "/artifactory/api/repositories/rpm-empty" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rpm-member" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-member/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I GET "/artifactory/rpm-member/repodata/repomd.xml" as admin until the response status is 200
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rpm-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"rpm","repositories":["rpm-empty","rpm-member"]}
      """
    Then the response status is 200
    When I POST "/artifactory/api/yum/rpm-virtual?path=repodata" as admin
    Then the response status is 200
    Given the dnf repository is "rpm-virtual"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: dnf installs through a virtual RPM repository backed by a remote member
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    And an RPM upstream serves the genuine package
    When I PUT "/artifactory/api/repositories/rpm-remote-member" as admin with body:
      """
      {"rclass":"remote","packageType":"rpm","url":"{upstream}"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rpm-virtual-remote" as admin with body:
      """
      {"rclass":"virtual","packageType":"rpm","repositories":["rpm-remote-member"]}
      """
    Then the response status is 200
    Given the dnf repository is "rpm-virtual-remote"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: virtual RPM metadata excludes packages a reader cannot access
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I PUT "/artifactory/api/repositories/rpm-private-member" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-private-member/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I GET "/artifactory/rpm-private-member/repodata/repomd.xml" as admin until the response status is 200
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rpm-private-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"rpm","repositories":["rpm-private-member"]}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/rpm-index-only" as admin with body:
      """
      {"name":"rpm-index-only","email":"rpm-index-only@example.com","password":"Index-pass1"}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/users/rpm-index-only/groups" as admin with JSON:
      """
      {"remove":["readers"]}
      """
    Then the response status is 200
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"rpm-index-only","resources":{"artifact":{"actions":{"users":{"rpm-index-only":["READ"]}},"targets":{"ANY":{"include_patterns":["repodata/*"]}}}}}
      """
    Then the response status is 201
    When I GET "/artifactory/rpm-private-virtual/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as "rpm-index-only" with password "Index-pass1"
    Then the response status is 403
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-private-member/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as "rpm-index-only" with password "Index-pass1"
    Then the response status is 403
    Given the dnf repository is "rpm-private-virtual" as "rpm-index-only" with password "Index-pass1"
    When I run dnf "repoquery artifactweave-alpha"
    Then the command succeeds
    And the output does not contain "artifactweave-alpha"

  Scenario: dnf verifies a virtual RPM repository's GPG-signed metadata
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I create a GPG RPM metadata signing pair
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/rpm-signed-member" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-signed-member/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I GET "/artifactory/rpm-signed-member/repodata/repomd.xml" as admin until the response status is 200
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rpm-signed-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"rpm","repositories":["rpm-signed-member"],"keyPair":"rpm-sign"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/security/keypair/public/repositories/rpm-signed-virtual" as admin
    Then the response status is 200
    Given the dnf repository is "rpm-signed-virtual"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds

  Scenario: delete and restore reconcile RPM metadata through Yum reindex
    Given the registry is running
    And the Fedora client can reach the registry
    And a genuine RPM package is available
    When I PUT "/artifactory/api/repositories/rpm-lifecycle" as admin with body:
      """
      {"rclass":"local","packageType":"rpm","calculateYumMetadata":true}
      """
    Then the response status is 200
    When I deploy the file "artifactweave-alpha-1.0-1.fc42.noarch.rpm" to "/artifactory/rpm-lifecycle/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 201
    When I POST "/artifactory/api/yum/rpm-lifecycle" as admin
    Then the response status is 200
    Given the dnf repository is "rpm-lifecycle"
    When I run dnf "install artifactweave-alpha"
    Then the command succeeds
    When I DELETE "/artifactory/rpm-lifecycle/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm" as admin
    Then the response status is 204
    When I POST "/artifactory/api/yum/rpm-lifecycle" as admin
    Then the response status is 200
    When I clear the RPM installation root
    And I run dnf "repoquery artifactweave-alpha"
    Then the command succeeds
    And the output does not contain "artifactweave-alpha"
    When I POST "/artifactory/api/trash/restore/rpm-lifecycle/pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm?to=rpm-lifecycle" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/yum/rpm-lifecycle" as admin
    Then the response status is 200
    When I clear the RPM installation root
    And I run dnf "install artifactweave-alpha"
    Then the command succeeds
