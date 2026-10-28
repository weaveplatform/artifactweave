@ansible
Feature: Native Ansible Galaxy collections
  ansible-core 2.21.5 builds and publishes controlled collections, and a fresh
  client resolves their dependency through each approved repository class.

  Scenario Outline: Galaxy publishes and installs through a <class> repository
    Given the registry is running
    And the Ansible client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"ansible"}
      """
    Then the response status is 200
    When I build the controlled Ansible collections
    And I publish the controlled Ansible collections to "<repo>"
    And I install the controlled Ansible collection from "<repo>"
    Then the command succeeds
    And the controlled Ansible dependency is installed

    Examples:
      | class     | repo          |
      | local     | ansible-local |
      | federated | ansible-fed   |

  Scenario: Galaxy installs through a virtual with ordered local members
    Given the registry is running
    And the Ansible client can reach the registry
    When I PUT "/artifactory/api/repositories/ansible-member" as admin with body:
      """
      {"rclass":"local","packageType":"ansible"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/ansible-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"ansible","repositories":["ansible-member"]}
      """
    Then the response status is 200
    When I build the controlled Ansible collections
    And I publish the controlled Ansible collections to "ansible-member"
    And I install the controlled Ansible collection from "ansible-virtual"
    Then the command succeeds
    And the controlled Ansible dependency is installed
    And the server answered 200 to GET "/artifactory/api/ansible/ansible-virtual/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.0.0.tar.gz"

  Scenario: Galaxy installs redirected remote collections and resolves offline from an empty client
    Given the registry is running
    And the Ansible client can reach the registry
    When I build the controlled Ansible collections
    Given the controlled Ansible Galaxy upstream is available
    And the Ansible upstream redirects its archives
    When I PUT "/artifactory/api/repositories/ansible-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"ansible","url":"{upstream}"}
      """
    Then the response status is 200
    When I install the controlled Ansible collection from "ansible-remote"
    Then the command succeeds
    And the controlled Ansible dependency is installed
    When I POST "/artifactory/api/repositories/ansible-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"ansible","url":"{upstream}","offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I install the controlled Ansible collection from "ansible-remote"
    Then the command succeeds
    And the controlled Ansible dependency is installed

  Scenario: Galaxy refuses an archive which fails the advertised checksum
    Given the registry is running
    And the Ansible client can reach the registry
    When I build the controlled Ansible collections
    Given the controlled Ansible Galaxy upstream is available
    And the Ansible upstream corrupts its archive
    When I PUT "/artifactory/api/repositories/ansible-corrupt" as admin with body:
      """
      {"rclass":"remote","packageType":"ansible","url":"{upstream}"}
      """
    Then the response status is 200
    When I install the controlled Ansible collection from "ansible-corrupt"
    Then the command fails
    When I GET "/artifactory/ansible-corrupt-cache/collections/weave/sample/1.0.0/weave-sample-1.0.0.tar.gz" as admin
    Then the response status is 404

  Scenario: Galaxy rejects invalid credentials for metadata reads and publication
    Given the registry is running
    And the Ansible client can reach the registry
    When I PUT "/artifactory/api/repositories/ansible-auth" as admin with body:
      """
      {"rclass":"local","packageType":"ansible"}
      """
    Then the response status is 200
    When I build the controlled Ansible collections
    And I publish the controlled Ansible collections to "ansible-auth"
    And I try Ansible install with invalid credentials against "ansible-auth"
    Then the command fails
    When I try Ansible publish with invalid credentials against "ansible-auth"
    Then the command fails
