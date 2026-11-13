@jf @agentplugins_clients
Feature: JFrog CLI installs Agent Plugins through the stored marketplace files
  7.x keeps the marketplace files at the repository root and rebuilds them
  after a publish has answered. The pinned CLI's install without --version
  finds the harness's file with an AQL search and downloads it (VS Code
  reads marketplace.json), so the scenario waits until the storage API
  shows each file, as JFrog CLI's own tests wait for the index, and then
  installs without --version.

  Scenario: install without --version resolves the version from the stored marketplace files
    Given the registry is running
    And the JFrog CLI is available
    And the JFrog CLI runs with "JFROG_AGENT_PLUGINS_DISABLE_QUIET_FAILURE" set to "true"
    When I PUT "/artifactory/api/repositories/plugins-latest" as admin with body:
      """
      {"rclass":"local","packageType":"agentplugins"}
      """
    Then the response status is 200
    Given a file "home/.keep" with content:
      """
      isolated CLI home
      """
    And the JFrog CLI runs with "HOME" set to "{dir}/home"
    And a file "standard/plugin.json" with content:
      """
      {"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-latest","version":"1.0.0","description":"Latest example"}
      """
    And a file "standard/skills/greet/SKILL.md" with content:
      """
      ---
      name: greet
      description: Greets the user.
      ---
      Say hello.
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "agent plugins publish standard --repo plugins-latest --version 1.0.0"
    Then the command succeeds
    When I GET "/artifactory/api/storage/plugins-latest/cursor-marketplace.json" as admin until the response status is 200
    Then the JSON field "createdBy" equals "_system_"
    When I GET "/artifactory/api/storage/plugins-latest/marketplace.json" as admin until the response status is 200
    Then the JSON field "createdBy" equals "_system_"
    When I run jf "agent plugins install hello-latest --harness vscode --repo plugins-latest"
    Then the command succeeds
    And the file "home/.copilot/installed-plugins/plugins-latest/hello-latest/plugin.json" contains "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
    And the file "home/.copilot/installed-plugins/plugins-latest/hello-latest/skills/greet/SKILL.md" exists
    When I run jf "agent plugins install hello-latest --harness cursor --repo plugins-latest"
    Then the command succeeds
    And the file "home/.cursor/plugins/local/hello-latest/plugin.json" contains "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
    When I run jf "agent plugins install hello-missing --harness cursor --repo plugins-latest"
    Then the command fails
    And the output contains "plugin 'hello-missing' is not listed in cursor-marketplace.json"
