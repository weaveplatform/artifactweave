@vagrant
Feature: Vagrant installs versioned boxes from repository metadata
  A native Vagrant client uses an access token to select a box version and
  provider, verifies the advertised checksum, and updates through the same
  repository. Federation here proves one-node behavior, without booting a VM.

  Scenario Outline: native box installation and update through a <class> repository
    Given the registry is running
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"vagrant"}
      """
    Then the response status is 200
    Given a Vagrant box "first.box" for provider "virtualbox" containing "original VM fixture"
    When I deploy the file "first.box" to "/artifactory/<repo>/arbitrary/first.box;box_name=acme%2Fdemo;box_provider=virtualbox;box_version=1.0.0" as admin
    Then the response status is 201
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "vagrant-token"
    Given the Vagrant client can reach the registry
    And a Vagrant client for repository "<repo>" using token "{vagrant-token}"
    When I run vagrant "--version"
    Then the command succeeds
    And the output contains "Vagrant 2.4.9"
    When I run vagrant "box add {vagrant-url}/acme/demo --provider virtualbox --box-version 1.0.0"
    Then the command succeeds
    And the installed Vagrant boxes contain "original VM fixture"
    When I run vagrant "box list"
    Then the command succeeds
    And the output contains "acme/demo"
    And the output contains "1.0.0"
    Given a Vagrant box "second.box" for provider "virtualbox" containing "updated VM fixture"
    When I deploy the file "second.box" to "/artifactory/<repo>/different/second.box;box_name=acme%2Fdemo;box_provider=virtualbox;box_version=1.1.0" as admin
    Then the response status is 201
    When I run vagrant "box outdated --global"
    Then the command succeeds
    And the output contains "1.1.0"
    When I run vagrant "box update --box acme/demo --provider virtualbox"
    Then the command succeeds
    And the installed Vagrant boxes contain "updated VM fixture"
    When I run vagrant "box list"
    Then the command succeeds
    And the output contains "1.1.0"
    Given a Vagrant box "docker.box" for provider "docker" containing "Docker provider fixture"
    When I deploy the file "docker.box" to "/artifactory/<repo>/providers/docker.box;box_name=acme%2Fdemo;box_provider=docker;box_version=1.1.0" as admin
    Then the response status is 201
    When I run vagrant "box add {vagrant-url}/acme/demo --provider docker --box-version 1.1.0"
    Then the command succeeds
    And the installed Vagrant boxes contain "Docker provider fixture"

    Examples:
      | class     | repo          |
      | local     | vagrant-local |
      | federated | vagrant-fed   |

  Scenario: an invalid token cannot install a private Vagrant box
    Given the registry is running
    When I PUT "/artifactory/api/repositories/vagrant-private" as admin with body:
      """
      {"rclass":"local","packageType":"vagrant"}
      """
    Then the response status is 200
    Given a Vagrant box "private.box" for provider "virtualbox" containing "private bytes"
    When I deploy the file "private.box" to "/artifactory/vagrant-private/private.box;box_name=private;box_provider=virtualbox;box_version=1.0.0" as admin
    Then the response status is 201
    Given the Vagrant client can reach the registry
    And a Vagrant client for repository "vagrant-private" using token "invalid-vagrant-token"
    When I run vagrant "box add {vagrant-url}/private --provider virtualbox --box-version 1.0.0"
    Then the command fails
    And the output contains "401"
