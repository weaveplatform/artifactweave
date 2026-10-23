@jf @agentplugins_clients
Feature: JFrog CLI installs and updates Agent Plugins from a local repository
  The pinned JFrog CLI resolves the repository's marketplace, downloads the
  ZIP and installs Cursor plugin bytes in an isolated home directory.

  Scenario: install v1, publish v2 and update Cursor
    Given the registry is running
    And the JFrog CLI is available
    And the JFrog CLI runs with "JFROG_AGENT_PLUGINS_DISABLE_QUIET_FAILURE" set to "true"
    When I PUT "/artifactory/api/repositories/plugins-clients" as admin with body:
      """
      {"rclass":"local","packageType":"agentplugins"}
      """
    Then the response status is 200
    Given a file "home/.keep" with content:
      """
      isolated CLI home
      """
    And the JFrog CLI runs with "HOME" set to "{dir}/home"
    And a file "plugin/.cursor-plugin/plugin.json" with content:
      """
      {"name":"hello-plugin","version":"1.0.0","description":"Cursor example"}
      """
    And a file "plugin/README.md" with content:
      """
      version one
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "agent plugins publish plugin --repo plugins-clients --version 1.0.0"
    Then the command succeeds
    When I run jf "agent plugins install hello-plugin --harness cursor --repo plugins-clients --version 1.0.0"
    Then the command succeeds
    When I run jf "agent plugins list --harness cursor --format json"
    Then the command succeeds
    And the output contains "hello-plugin"
    And the output contains "1.0.0"
    When I run jf "rt dl plugins-clients/hello-plugin/1.0.0/hello-plugin-1.0.0.zip downloaded-v1/ --flat"
    Then the command succeeds
    When I run jf "rt dl plugins-clients/hello-plugin/1.0.0/hello-plugin-1.0.0.zip downloaded-v1-again/ --flat"
    Then the command succeeds
    And the files "downloaded-v1/hello-plugin-1.0.0.zip" and "downloaded-v1-again/hello-plugin-1.0.0.zip" are identical
    Given a file "plugin/.cursor-plugin/plugin.json" with content:
      """
      {"name":"hello-plugin","version":"2.0.0","description":"Cursor example"}
      """
    And a file "plugin/README.md" with content:
      """
      version two
      """
    When I run jf "agent plugins publish plugin --repo plugins-clients --version 2.0.0"
    Then the command succeeds
    When I run jf "agent plugins update --slug hello-plugin --harness cursor --repo plugins-clients"
    Then the command succeeds
    When I run jf "agent plugins list --harness cursor --format json"
    Then the command succeeds
    And the output contains "2.0.0"
    When I GET "/artifactory/plugins-clients/cursor-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.version" equals "2.0.0"

  Scenario: Claude marketplace and ZIP enforce credentials and token expiry
    Given the registry is running
    And the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/plugins-claude" as admin with body:
      """
      {"rclass":"local","packageType":"agentplugins"}
      """
    Then the response status is 200
    Given a file "claude/.claude-plugin/plugin.json" with content:
      """
      {"name":"hello-claude","version":"1.0.0","description":"Claude example"}
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "agent plugins publish claude --repo plugins-claude --version 1.0.0"
    Then the command succeeds
    When I GET "/artifactory/plugins-claude/claude-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.source.source" equals "url"
    When I GET "/artifactory/plugins-claude/hello-claude/1.0.0/hello-claude-1.0.0.zip" as admin
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/plugin-denied" as admin with body:
      """
      {"name":"plugin-denied","email":"denied@example.invalid","password":"Denied-pass-123"}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/groups/readers/members" as admin with JSON:
      """
      {"remove":["plugin-denied"]}
      """
    When I GET "/artifactory/plugins-claude/claude-marketplace.json" as "plugin-denied" with password "Denied-pass-123"
    Then the response status is 403
    When I GET "/artifactory/plugins-claude/hello-claude/1.0.0/hello-claude-1.0.0.zip" as "plugin-denied" with password "Denied-pass-123"
    Then the response status is 403
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":2}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "plugin-token"
    When I GET "/artifactory/plugins-claude/claude-marketplace.json" with the kept token "plugin-token"
    Then the response status is 200
    Given the access token kept as "plugin-token" has expired
    When I GET "/artifactory/plugins-claude/claude-marketplace.json" with the kept token "plugin-token"
    Then the response status is 401
    When I GET "/artifactory/plugins-claude/hello-claude/1.0.0/hello-claude-1.0.0.zip" with the kept token "plugin-token"
    Then the response status is 401

  Scenario: path identity remains authoritative when an uploaded archive disagrees
    Given the registry is running
    And the JFrog CLI is available
    And the JFrog CLI runs with "JFROG_AGENT_PLUGINS_DISABLE_QUIET_FAILURE" set to "true"
    When I PUT "/artifactory/api/repositories/plugins-mismatch" as admin with body:
      """
      {"rclass":"local","packageType":"agentplugins"}
      """
    Then the response status is 200
    Given a file "home/.keep" with content:
      """
      isolated CLI home
      """
    And the JFrog CLI runs with "HOME" set to "{dir}/home"
    And a file "mismatch/.cursor-plugin/plugin.json" with content:
      """
      {"name":"manifest-lie","version":"99.0.0","description":"Path wins"}
      """
    And a zip file "mismatch.zip" of the files under "mismatch"
    When I deploy the file "mismatch.zip" to "/artifactory/plugins-mismatch/hello-plugin/1.0.0/hello-plugin-1.0.0.zip" as admin
    Then the response status is 201
    When I GET "/artifactory/plugins-mismatch/cursor-marketplace.json" as admin
    Then the response status is 200
    And the JSON field "plugins.0.name" equals "hello-plugin"
    And the JSON field "plugins.0.version" equals "1.0.0"
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "agent plugins install hello-plugin --harness cursor --repo plugins-mismatch --version 1.0.0"
    Then the command succeeds
