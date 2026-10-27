@apm
Feature: Native Agent Package Manager uses local APM v1 registry
  A pinned APM client publishes and installs Agent Packages from the registry
  with exact and ranged SemVer selection.

  Scenario: publish two versions and install an exact and a ranged version
    Given the registry is running
    And the native APM CLI is available
    And the APM registry is served through TLS
    When I PUT "/artifactory/api/repositories/apm-local" as admin with body:
      """
      {"rclass":"local","packageType":"agentpackages"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "apm-token"
    Given a file "producer/apm.yml" with content:
      """
      name: my-pkg
      version: 1.0.0
      description: A native APM example
      includes: auto
      dependencies:
        apm: []
        mcp: []
      scripts: {}
      """
    And a file "producer/.apm/skills/help/SKILL.md" with content:
      """
      ---
      name: help
      description: Help with the test package
      ---
      Help the user.
      """
    And the APM CLI runs with "APM_REGISTRY_TOKEN_APM_LOCAL" set to "{apm-token}"
    When I run apm "experimental enable registries" in "producer"
    Then the command succeeds
    When I run apm "config set registry.apm-local.url {apmurl}/api/agentpackages/apm-local" in "producer"
    Then the command succeeds
    When I run apm "config set registry.apm-local.default true" in "producer"
    Then the command succeeds
    When I run apm "publish --package acme/my-pkg --registry apm-local" in "producer"
    Then the command succeeds
    When I GET "/artifactory/api/agentpackages/apm-local/v1/packages/acme/my-pkg/versions" as admin
    Then the response status is 200
    And the JSON field "versions.0.version" equals "1.0.0"
    Given a file "consumer-exact/apm.yml" with content:
      """
      name: consumer-exact
      version: 0.1.0
      includes: auto
      dependencies:
        apm: []
        mcp: []
      scripts: {}
      """
    When I run apm "install acme/my-pkg#1.0.0 --target claude" in "consumer-exact"
    Then the command succeeds
    And the file "consumer-exact/apm.lock.yaml" contains "1.0.0"
    Given a file "producer/apm.yml" with content:
      """
      name: my-pkg
      version: 1.2.0
      description: A newer native APM example
      includes: auto
      dependencies:
        apm: []
        mcp: []
      scripts: {}
      """
    When I run apm "publish --package acme/my-pkg --registry apm-local" in "producer"
    Then the command succeeds
    Given a file "consumer-range/apm.yml" with content:
      """
      name: consumer-range
      version: 0.1.0
      includes: auto
      dependencies:
        apm: []
        mcp: []
      scripts: {}
      """
    When I run apm "view acme/my-pkg versions --registry apm-local" in "consumer-range"
    Then the command succeeds
    And the output contains "1.2.0"
    And the output contains "1.0.0"
    When I run apm "install acme/my-pkg#^1.0.0 --target claude" in "consumer-range"
    Then the command succeeds
    And the file "consumer-range/apm.lock.yaml" contains "1.2.0"
    When I GET "/artifactory/api/agentpackages/apm-local/v1/packages/acme/my-pkg/versions" as admin
    Then the response status is 200
    And the JSON field "versions.0.version" equals "1.2.0"

  Scenario: publish and install through a one-node federated repository
    Given the registry is running
    And the native APM CLI is available
    And the APM registry is served through TLS
    When I PUT "/artifactory/api/repositories/apm-fed" as admin with body:
      """
      {"rclass":"federated","packageType":"agentpackages"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "apm-fed-token"
    Given a file "producer-fed/apm.yml" with content:
      """
      name: fed-pkg
      version: 1.0.0
      includes: auto
      dependencies:
        apm: []
        mcp: []
      scripts: {}
      """
    And a file "producer-fed/.apm/skills/help/SKILL.md" with content:
      """
      ---
      name: help
      description: Help with the federated test package
      ---
      Help the user.
      """
    And the APM CLI runs with "APM_REGISTRY_TOKEN_APM_FED" set to "{apm-fed-token}"
    When I run apm "experimental enable registries" in "producer-fed"
    Then the command succeeds
    When I run apm "config set registry.apm-fed.url {apmurl}/api/agentpackages/apm-fed" in "producer-fed"
    Then the command succeeds
    When I run apm "config set registry.apm-fed.default true" in "producer-fed"
    Then the command succeeds
    When I run apm "publish --package acme/fed-pkg --registry apm-fed" in "producer-fed"
    Then the command succeeds
    Given a file "consumer-fed/apm.yml" with content:
      """
      name: consumer-fed
      version: 0.1.0
      includes: auto
      dependencies:
        apm: []
        mcp: []
      scripts: {}
      """
    When I run apm "install acme/fed-pkg#1.0.0 --target claude" in "consumer-fed"
    Then the command succeeds
    And the file "consumer-fed/apm.lock.yaml" contains "1.0.0"
