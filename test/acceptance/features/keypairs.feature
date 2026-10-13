Feature: Key pairs and client certificates
  Signing key pairs are stored with their private halves sealed, assigned to
  repositories by name, and cannot be deleted while a repository uses them.
  Client certificates for remote repositories are stored with their keys.

  Background:
    Given the registry is running

  Scenario: Create, assign and delete a signing key pair
    When I create the RSA key pair "signing" with alias "release" as admin
    Then the response status is 201
    When I GET "/artifactory/api/security/keypair/signing" as admin
    Then the response status is 200
    And the JSON field "alias" equals "release"
    And the JSON field "publicKey" is not empty
    When I PUT "/artifactory/api/repositories/debian-signed" as admin with body:
      """
      {"rclass": "local", "packageType": "debian", "primaryKeyPairRef": "signing"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/security/keypair/public/repositories/debian-signed" as admin
    Then the response status is 200
    And the response body contains "BEGIN PUBLIC KEY"
    When I DELETE "/artifactory/api/security/keypair/signing" as admin
    Then the response status is 400
    When I DELETE "/artifactory/api/repositories/debian-signed" as admin
    And I DELETE "/artifactory/api/security/keypair/signing" as admin
    Then the response status is 200

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
