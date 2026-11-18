@recipes @maven @jvmrecipes
Feature: JVM connection recipes resolve from fresh homes with scoped credentials
  The pinned Ivy and Gradle clients consume public native repository endpoints.
  Each resolve uses new client state, including the invalid-token attempt.

  @maven_layout
  Scenario: Gradle uses an Ivy artifact pattern to resolve a Maven publication through a different virtual layout
    Given the registry is running
    And the JVM recipe client image is available
    And a Gradle-layout virtual over a Maven-layout local is ready
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "recipe-token"
    When I publish the layout recipe with Maven and token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I resolve the gradle recipe with token "invalid-recipe-token"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I resolve the gradle recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM recipe retrieved the controlled artifact
    And the server answered 200 to GET "/artifactory/gradle-layout-virtual/org.acme/widget/1.0.0/widget-1.0.0.jar"
    When I GET "/artifactory/mvn-layout-local/org/acme/widget/1.0.0/widget-1.0.0.jar" as admin
    Then the response status is 200

  Scenario Outline: Ivy resolves the exact bytes from a <class> repository
    Given the registry is running
    And the JVM recipe client image is available
    And a controlled ivy recipe repository of class "<class>" is ready
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "recipe-token"
    When I resolve the ivy recipe with token "invalid-recipe-token"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I resolve the ivy recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM recipe retrieved the controlled artifact
    And the JVM recipe used both virtual member origins
    And the server answered 200 to GET "/artifactory/ivy-recipe-<class>/org.acme/widget/1.0.0/jars/widget-1.0.0.jar"

    Examples:
      | class     |
      | local     |
      | remote    |
      | virtual   |
      | federated |

  @gradle_native
  Scenario Outline: Gradle resolves the exact bytes from a <class> repository
    Given the registry is running
    And the JVM recipe client image is available
    And a controlled gradle recipe repository of class "<class>" is ready
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "recipe-token"
    When I resolve the gradle recipe with token "invalid-recipe-token"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I resolve the gradle recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM recipe retrieved the controlled artifact
    And the JVM recipe used both virtual member origins
    And the server answered 200 to GET "/artifactory/gradle-recipe-<class>/org/acme/widget/1.0.0/widget-1.0.0.jar"

    Examples:
      | class     |
      | local     |
      | remote    |
      | virtual   |
      | federated |

  @gradle_native
  Scenario Outline: Gradle publishes a module into a <class> repository
    Given the registry is running
    And the JVM recipe client image is available
    And a controlled gradle recipe repository of class "<class>" is ready
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "recipe-token"
    When I publish the Gradle recipe with token "invalid-recipe-token"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I publish the Gradle recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM publication landed in the deployment member
    When I resolve the gradle recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM recipe retrieved the controlled artifact

    Examples:
      | class     |
      | local     |
      | federated |
      | virtual   |

  @maven_virtual
  Scenario: Ivy publishes into a mixed virtual deployment target and resolves with a fresh cache
    Given the registry is running
    And the JVM recipe client image is available
    And a controlled ivy recipe repository of class "virtual" is ready
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "recipe-token"
    When I publish the Ivy recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM publication landed in the deployment member
    When I resolve the ivy recipe with token "{recipe-token}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the JVM recipe retrieved the controlled artifact
