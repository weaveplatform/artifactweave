@bazelmodules
Feature: Bazel Modules publish only complete, valid registry versions
  The native Bazel 9.3.1 client build is exercised separately by
  TestAccBazelClient on local, remote, virtual and one-node federated classes.

  Scenario: a module is hidden until its source archive is published
    Given the registry is running
    When I PUT "/artifactory/api/repositories/bazel-local" as admin with body:
      """
      {"rclass":"local","packageType":"bazelmodules"}
      """
    Then the response status is 200
    Given a file "bazel/MODULE.bazel" with content:
      """
      module(name = "hello", version = "1.0.0")
      """
    When I deploy the file "bazel/MODULE.bazel" to "/artifactory/bazel-local/modules/hello/1.0.0/MODULE.bazel" as admin
    Then the response status is 201
    When I GET "/artifactory/api/bazelmodules/bazel-local/modules/hello/metadata.json" as admin
    Then the response status is 404
    When I GET "/artifactory/api/bazelmodules/bazel-local/modules/hello/1.0.0/source.json" as admin
    Then the response status is 404

  Scenario: generated metadata cannot be uploaded or forged
    Given the registry is running
    When I PUT "/artifactory/api/repositories/bazel-guarded" as admin with body:
      """
      {"rclass":"local","packageType":"bazelmodules"}
      """
    Then the response status is 200
    Given a file "bazel/source.json" with content:
      """
      {"url":"https://attacker.example/hello.tar.gz"}
      """
    When I deploy the file "bazel/source.json" to "/artifactory/bazel-guarded/modules/hello/1.0.0/source.json" as admin
    Then the response status is 409
    Given a file "bazel/wrong.MODULE.bazel" with content:
      """
      module(name = "wrong", version = "1.0.0")
      """
    When I deploy the file "bazel/wrong.MODULE.bazel" to "/artifactory/bazel-guarded/modules/hello/1.0.0/MODULE.bazel" as admin
    Then the response status is 409
