Feature: Key pairs and client certificates
  Signing key pairs are stored with their private halves sealed and assigned
  to repositories by name. Deleting a pair a repository uses also removes it
  from that repository. Client certificates for remote repositories are
  stored with their keys.

  Background:
    Given the registry is running

  @keypair_signing
  Scenario: Create, assign and delete a signing key pair
    When I create the RSA key pair "signing" with alias "release" as admin
    Then the response status is 201
    When I GET "/artifactory/api/security/keypair/signing" as admin
    Then the response status is 200
    And the JSON field "alias" equals "release"
    And the JSON field "publicKey" is not empty
    When I PUT "/artifactory/api/repositories/debian-rsa-rejected" as admin with body:
      """
      {"rclass": "local", "packageType": "debian", "primaryKeyPairRef": "signing"}
      """
    Then the response status is 400
    And the response body contains "Debian metadata signing requires GPG"
    When I PUT "/artifactory/api/repositories/alpine-signed" as admin with body:
      """
      {"rclass": "local", "packageType": "alpine", "primaryKeyPairRef": "signing"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/security/keypair/public/repositories/alpine-signed" as admin
    Then the response status is 200
    And the response body contains "BEGIN PUBLIC KEY"
    And the response header "Content-Disposition" contains "attachment; filename=release.rsa.pub"
    When I DELETE "/artifactory/api/security/keypair/signing" as admin
    Then the response status is 200
    And the response body is "OK"
    When I GET "/artifactory/api/repositories/alpine-signed" as admin
    Then the response status is 200
    And the response body does not contain "primaryKeyPairRef"
    When I GET "/artifactory/api/security/keypair/public/repositories/alpine-signed" as admin
    Then the response status is 404

  Scenario: A create replaces a pair of the same name, and any user reads its public half
    When I create the RSA key pair "rotating" with alias "first" as admin
    Then the response status is 201
    When I create the RSA key pair "rotating" with alias "second" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/security/users/viewer" as admin with body:
      """
      {"name": "viewer", "email": "viewer@example.com", "password": "Viewer-pass1"}
      """
    Then the response status is 201
    When I GET "/artifactory/api/security/keypair/rotating" as "viewer" with password "Viewer-pass1"
    Then the response status is 200
    And the JSON field "alias" equals "second"
    And the JSON field "publicKey" is not empty
    And the response body does not contain "PRIVATE KEY"
    When I GET "/artifactory/api/security/keypair" as "viewer" with password "Viewer-pass1"
    Then the response status is 403

  Scenario: A repository cannot reference a missing key pair
    When I PUT "/artifactory/api/repositories/debian-unsigned" as admin with body:
      """
      {"rclass": "local", "packageType": "debian", "primaryKeyPairRef": "absent"}
      """
    Then the response status is 400
    And the response body contains "Key pair 'absent' does not exist"

  Scenario: Add, list and delete a client certificate
    When I add a generated client certificate "upstream" as admin
    Then the response status is 200
    When I GET "/artifactory/api/system/security/certificates" as admin
    Then the response status is 200
    And the response body contains "upstream"
    And the response body contains "fingerPrint"
    When I DELETE "/artifactory/api/system/security/certificates/upstream" as admin
    Then the response status is 200
