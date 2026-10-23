@jf @agentplugins
Feature: JFrog CLI publishes Agent Plugins into permission-filtered marketplaces
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
