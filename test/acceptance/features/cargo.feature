@cargo
Feature: Native Cargo sparse registries
  Cargo 1.90.0 publishes a controlled crate, reads its sparse index and
  resolves a fresh consumer through each approved hosted class.

  Scenario Outline: Cargo publishes and builds through a <class> repository
    Given the registry is running
    And the Cargo client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"cargo"}
      """
    Then the response status is 200
    Given a Cargo project and consumer for registry "<repo>"
    When I run cargo "publish --manifest-path /work/cargo-project/Cargo.toml --registry weave --allow-dirty --no-verify"
    Then the command succeeds
    When I GET "/artifactory/api/cargo/<repo>/index/he/ll/hello_cargo" as admin
    Then the response status is 200
    And the response body contains "1.0.0"
    When I clear Cargo's registry cache
    And I run cargo "build --manifest-path /work/cargo-consumer/Cargo.toml"
    Then the command succeeds
    When I run cargo "yank hello_cargo --version 1.0.0 --registry weave"
    Then the command succeeds
    When I GET "/artifactory/api/cargo/<repo>/index/he/ll/hello_cargo" as admin
    Then the response body contains "yanked"
    When I run cargo "yank hello_cargo --version 1.0.0 --registry weave --undo"
    Then the command succeeds

    Examples:
      | class     | repo        |
      | local     | cargo-local |
      | federated | cargo-fed   |

  Scenario: Cargo resolves a checksum-pinned crate through remote cache after upstream loss
    Given the registry is running
    And the Cargo client can reach the registry
    And a Cargo project and consumer for registry "cargo-remote"
    When I run cargo "package --manifest-path /work/cargo-project/Cargo.toml --allow-dirty --no-verify"
    Then the command succeeds
    Given a Cargo upstream serves the packaged crate
    When I PUT "/artifactory/api/repositories/cargo-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"cargo","url":"{upstream}/"}
      """
    Then the response status is 200
    When I run cargo "build --manifest-path /work/cargo-consumer/Cargo.toml"
    Then the command succeeds
    Given the generic upstream stops
    When I clear Cargo's registry cache
    And I run cargo "build --manifest-path /work/cargo-consumer/Cargo.toml"
    Then the command succeeds
