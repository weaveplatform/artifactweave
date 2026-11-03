@recipes @bazelmodules
Feature: Bazel connection recipe obeys an explicit read grant
  Unlike the protocol fixture, this invokes the actual artifactweave binary.

  Scenario: a fresh Bazel build requires the repository-specific read grant
    Given the registry is running
    And a controlled Bazel recipe module is ready
    When I build the Bazel public recipe from fresh state
    Then the command fails
    And the server answered 401 to GET "/artifactory/api/bazelmodules/bazel-recipe/modules/hello/1.0.0/MODULE.bazel"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I GET "/artifactory/api/bazelmodules/bazel-recipe/modules/hello/1.0.0/MODULE.bazel"
    Then the response status is 401
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"bazel-recipe-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"bazel-recipe":{"include_patterns":["**"]}}}}}
      """
    Then the response status is 201
    When I build the Bazel public recipe from fresh state
    Then the command succeeds
    And the server answered 200 to GET "/artifactory/api/bazelmodules/bazel-recipe/modules/hello/1.0.0/MODULE.bazel"
    When I DELETE "/access/api/v2/permissions/bazel-recipe-read" as admin
    Then the response status is 204
    When I build the Bazel public recipe from fresh state
    Then the command fails
