@jetbrainsplugins
Feature: JetBrains IDE installs from a native remote Marketplace repository
  IntelliJ IDEA 2025.3 uses the documented Marketplace host setting and
  installs a compatible plugin without credentials, through an explicit
  anonymous read grant.

  Scenario: the pinned IDE resolves and installs a plugin through a remote cache
    Given the registry is running
    And a JetBrains Marketplace fixture serves a compatible plugin
    When I PUT "/artifactory/api/repositories/jetbrains-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"jetbrainsplugins","url":"{upstream}"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/jetbrainsplugins/jetbrains-remote/api/search/updates/compatible?build=IC-252.28238.7&pluginXmlId=com.weave.fixture"
    Then the response status is 401
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I GET "/artifactory/api/jetbrainsplugins/jetbrains-remote/api/search/updates/compatible?build=IC-252.28238.7&pluginXmlId=com.weave.fixture"
    Then the response status is 401
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"jetbrains-anonymous-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"jetbrains-remote":{"include_patterns":["**"]}}}}}
      """
    Then the response status is 201
    When I GET "/artifactory/api/jetbrainsplugins/jetbrains-remote/api/search/updates/compatible?build=IC-252.28238.7&pluginXmlId=com.weave.fixture"
    Then the response status is 200
    Given the JetBrains client can reach the registry
    When I install the JetBrains fixture plugin with a fresh IDE profile "cold"
    Then the command succeeds
    And the output contains "installed plugin: PluginNode{id=com.weave.fixture"
    And the generic upstream received 2 GET requests for "/api/search/updates/compatible"
    And the generic upstream received 1 GET requests for "/files/98765/12345/meta.json"
    And the generic upstream received 1 GET requests for "/pluginManager"
    And the generic upstream received 1 GET requests for "/files/98765/12345/weave-fixture.jar"
    Given the generic upstream stops
    When I install the JetBrains fixture plugin with a fresh IDE profile "warm"
    Then the command succeeds
    And the output contains "installed plugin: PluginNode{id=com.weave.fixture"

  Scenario: the full server labels a redirected plugin JAR for the IDE
    Given the registry is running
    And a JetBrains Marketplace fixture serves a compatible plugin
    When I PUT "/artifactory/api/repositories/jetbrains-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"jetbrainsplugins","url":"{upstream}"}
      """
    Then the response status is 200
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"jetbrains-anonymous-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"jetbrains-remote":{"include_patterns":["**"]}}}}}
      """
    Then the response status is 201
    When I GET "/artifactory/api/jetbrainsplugins/jetbrains-remote/pluginManager?id=com.weave.fixture&build=IC-252.28238.7"
    Then the response status is 200
    And the response header "Content-Disposition" contains "plugin-com-weave-fixture.jar"
    And the JetBrains archive bytes match the upstream fixture
    And the generic upstream received 1 GET requests for "/pluginManager"
    And the generic upstream received 1 GET requests for "/files/98765/12345/weave-fixture.jar"
