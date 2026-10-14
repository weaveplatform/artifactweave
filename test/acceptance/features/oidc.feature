@jf @oidc
Feature: OIDC token exchange
  A CI system's ID token is exchanged for an access token with jf eot, as
  setup-jfrog-cli does, and the token reads what its identity mapping
  grants (research 21). The provider is a fake CI issuer the test runs.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And an OIDC provider is running
    And I PUT "/artifactory/api/repositories/ci-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And I PUT "/artifactory/api/security/groups/ci-readers" as admin with body:
      """
      {"name":"ci-readers"}
      """
    And I PUT "/artifactory/api/v2/security/permissions/ci-read" as admin with body:
      """
      {"name":"ci-read","repo":{"repositories":["ci-local"],"include-patterns":["**"],
        "actions":{"groups":{"ci-readers":["read"]}}}}
      """
    And a file "a.txt" with content:
      """
      built in CI
      """
    When I run jf "rt u a.txt ci-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    When I POST "/access/api/v1/oidc" as admin with body:
      """
      {"name":"ci","issuer_url":"{issuer}","provider_type":"Generic OpenID Connect",
       "audience":"jfrog","use_default_proxy":false}
      """
    Then the response status is 200
    When I POST "/access/api/v1/oidc/ci/identity_mappings" as admin with body:
      """
      {"name":"octo","priority":1,"claims":{"repository":"octo-org/*"},
       "token_spec":{"scope":"applied-permissions/groups:ci-readers","audience":"*@*","expires_in":300}}
      """
    Then the response status is 200

  Scenario: jf eot exchanges a CI token for an access token that downloads through its mapping's group
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "id-token" with claims:
      """
      {"sub":"repo:octo-org/app:ref:refs/heads/main","repository":"octo-org/app"}
      """
    When I run jf "eot ci {id-token} --url {platform}/ --oidc-provider-type GenericOidc --oidc-audience jfrog"
    Then the command succeeds
    And the output contains "oidc-ci-octo"
    And I keep the JSON field "AccessToken" of the output as "ci-token"
    When I run jf "rt dl ci-local/a.txt ci/ --flat --fail-no-op --url {url} --access-token {ci-token}"
    Then the command succeeds
    And the files "ci/a.txt" and "a.txt" are identical

  Scenario: A token from another organisation matches no mapping, and a token for another audience does not verify
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "stranger" with claims:
      """
      {"sub":"repo:evil-org/app:ref:refs/heads/main","repository":"evil-org/app"}
      """
    When I run jf "eot ci {stranger} --url {platform}/ --oidc-provider-type GenericOidc"
    Then the command fails
    And the server answered 400 to POST "/access/api/v1/oidc/token"
    Given the OIDC provider issues an ID token for audience "elsewhere" kept as "misdirected" with claims:
      """
      {"sub":"repo:octo-org/app:ref:refs/heads/main","repository":"octo-org/app"}
      """
    When I run jf "eot ci {misdirected} --url {platform}/ --oidc-provider-type GenericOidc"
    Then the command fails
    And the server answered 401 to POST "/access/api/v1/oidc/token"
