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

  Scenario: A dynamic mapping names the user from a claim and narrows the token to the repository a claim names
    When I POST "/access/api/v1/oidc/ci/identity_mappings" as admin with body:
      """
      {"name":"dynamic","priority":1,"claims":{"repository":"dyn-org/*"},
       "token_spec":{"username_pattern":"jf{{actor}}jf|$1","scope":"applied-permissions/groups:ci-readers",
         "resource_pattern":"{{jfrog_scope}}","audience":"*@*","expires_in":300}}
      """
    Then the response status is 200
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "narrowed" with claims:
      """
      {"sub":"repo:dyn-org/app","repository":"dyn-org/app","actor":"jfci-builderjf","jfrog_scope":"ci-local"}
      """
    When I run jf "eot ci {narrowed} --url {platform}/ --oidc-provider-type GenericOidc --oidc-audience jfrog"
    Then the command succeeds
    And the output contains "ci-builder"
    And I keep the JSON field "AccessToken" of the output as "narrowed-token"
    When I run jf "rt dl ci-local/a.txt dyn/ --flat --fail-no-op --url {url} --access-token {narrowed-token}"
    Then the command succeeds
    And the files "dyn/a.txt" and "a.txt" are identical
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "elsewhere" with claims:
      """
      {"sub":"repo:dyn-org/app","repository":"dyn-org/app","actor":"jfci-builderjf","jfrog_scope":"other-local"}
      """
    When I run jf "eot ci {elsewhere} --url {platform}/ --oidc-provider-type GenericOidc --oidc-audience jfrog"
    Then the command succeeds
    And I keep the JSON field "AccessToken" of the output as "elsewhere-token"
    When I run jf "rt dl ci-local/a.txt other/ --flat --fail-no-op --url {url} --access-token {elsewhere-token}"
    Then the command fails
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "unmatched" with claims:
      """
      {"sub":"repo:dyn-org/app","repository":"dyn-org/app","actor":"ci-builder","jfrog_scope":"ci-local"}
      """
    When I run jf "eot ci {unmatched} --url {platform}/ --oidc-provider-type GenericOidc --oidc-audience jfrog"
    Then the command fails
    And the server answered 400 to POST "/access/api/v1/oidc/token"

  Scenario: A transient user from an exchange mints its own token with jf atc, which downloads as the exchanged token does
    When I POST "/access/api/v1/oidc/ci/identity_mappings" as admin with body:
      """
      {"name":"named","priority":1,"claims":{"repository":"named-org/*"},
       "token_spec":{"username":"ci-named","scope":"applied-permissions/groups:ci-readers","audience":"*@*",
         "expires_in":300}}
      """
    Then the response status is 200
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "named" with claims:
      """
      {"sub":"repo:named-org/app","repository":"named-org/app"}
      """
    When I run jf "eot ci {named} --url {platform}/ --oidc-provider-type GenericOidc --oidc-audience jfrog"
    Then the command succeeds
    And the output contains "ci-named"
    And I keep the JSON field "AccessToken" of the output as "named-token"
    When I run jf "atc --url {platform}/ --access-token {named-token}"
    Then the command succeeds
    And I keep the JSON field "access_token" of the output as "own-token"
    When I run jf "rt dl ci-local/a.txt own/ --flat --fail-no-op --url {url} --access-token {own-token}"
    Then the command succeeds
    And the files "own/a.txt" and "a.txt" are identical

  Scenario: A project roles mapping without a username, as the jfrog/platform provider's example sends it, exchanges for a synthetic subject
    When I POST "/access/api/v1/oidc/ci/identity_mappings" as admin with body:
      """
      {"name":"project-roles","priority":1,"claims":{"repository":"roles-org/*"},
       "token_spec":{"scope":"applied-permissions/roles:my-project:\"Project Admin\",\"Developer\"",
         "audience":"jfrt@* jfac@*","expires_in":7200}}
      """
    Then the response status is 200
    And the JSON field "project_key" equals "my-project"
    Given the OIDC provider issues an ID token for audience "jfrog" kept as "roles" with claims:
      """
      {"sub":"repo:roles-org/app","repository":"roles-org/app"}
      """
    When I run jf "eot ci {roles} --url {platform}/ --oidc-provider-type GenericOidc --oidc-audience jfrog"
    Then the command succeeds
    And the output contains "oidc-ci-project-roles"
    When I PUT "/access/api/v1/oidc/ci/identity_mappings/project-roles" as admin with body:
      """
      {"name":"project-roles","claims":{"repository":"roles-org/*"},
       "token_spec":{"scope":"applied-permissions/roles:other-project:Developer"}}
      """
    Then the response status is 400
