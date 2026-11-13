@jf @agentplugins
Feature: JFrog CLI publishes Agent Plugins into the stored marketplace files
  The pinned CLI packages harness manifests and uploads a canonical ZIP. The
  repository indexes the path-derived identity for the unified and harness
  marketplaces; installing into client homes is covered by #691.

  Scenario: publish a multi-harness plugin and read both indexes
    Given the registry is running
    And the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/plugins-local" as admin with body:
      """
      {"rclass":"local","packageType":"agentplugins"}
      """
    Then the response status is 200
    Given a file "plugin/.claude-plugin/plugin.json" with content:
      """
      {"name":"hello-plugin","version":"1.0.0","description":"Claude example"}
      """
    And a file "plugin/.cursor-plugin/plugin.json" with content:
      """
      {"name":"hello-plugin","version":"1.0.0","description":"Cursor example"}
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "agent plugins publish plugin --repo plugins-local --version 1.0.0"
    Then the command succeeds
    When I GET "/artifactory/plugins-local/hello-plugin/1.0.0/hello-plugin-1.0.0.zip" as admin
    Then the response status is 200
    When I GET "/artifactory/plugins-local/marketplace.json" as admin
    Then the response status is 200
    And the response body contains "hello-plugin"
    And the JSON field "plugins.0.source.source" equals "zip"
    When I GET "/artifactory/plugins-local/claude-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.source.source" equals "url"
    When I GET "/artifactory/plugins-local/cursor-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.source.source" equals "zip"

  Scenario: publish a plugin with both layouts and read every index
    Self-managed 7.173.8 lists a bundle with a harness manifest and a
    conforming root plugin.json in every file either layout matches. The
    Cursor file keeps the Cursor manifest, and the VS Code file takes the
    root plugin.json.
    Given the registry is running
    And the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/plugins-both" as admin with body:
      """
      {"rclass":"local","packageType":"agentplugins"}
      """
    Then the response status is 200
    Given a file "both/.cursor-plugin/plugin.json" with content:
      """
      {"name":"both-plugin","version":"1.0.0","description":"Cursor example"}
      """
    And a file "both/plugin.json" with content:
      """
      {"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"both-plugin","version":"1.0.0","description":"Standard example"}
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "agent plugins publish both --repo plugins-both --version 1.0.0"
    Then the command succeeds
    When I GET "/artifactory/plugins-both/cursor-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.name" equals "both-plugin"
    And the JSON field "plugins.0.description" equals "Cursor example"
    When I GET "/artifactory/plugins-both/vscode-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.name" equals "both-plugin"
    And the JSON field "plugins.0.description" equals "Standard example"
    And the JSON field "plugins.0.source.source" equals "zip"
    When I GET "/artifactory/plugins-both/marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.description" equals "Cursor example"
    When I GET "/artifactory/plugins-both/codex-marketplace.json" as admin
    Then the response status is 200
    And the response body does not contain "both-plugin"
    When I GET "/artifactory/api/storage/plugins-both/both-plugin/1.0.0/both-plugin-1.0.0.zip?properties=agentplugins.type" as admin
    Then the response status is 200
    And the response body contains "cursor"
    And the response body contains "generic"
    And the response body contains "vscode"
