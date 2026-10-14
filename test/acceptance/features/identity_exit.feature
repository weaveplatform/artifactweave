@jf
Feature: Identity exit checks
  Users, groups, permission targets and tokens made with the JFrog CLI work
  together end to end, and a token scoped to one repository cannot read
  another (M1). The Terraform identity resources are applied by the
  terraform CI job (deploy/artifactweave/terraform/identity).

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/team-a-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And I PUT "/artifactory/api/repositories/team-b-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And a file "a.txt" with content:
      """
      from team a
      """
    And a file "b.txt" with content:
      """
      from team b
      """
    When I run jf "rt u a.txt team-a-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u b.txt team-b-local/ --url {url} --user admin --password {password}"
    Then the command succeeds

  Scenario: A user, group and permission target made with jf read through tokens, and a repository-scoped token reads only its repository
    When I run jf "rt group-create exit-team --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt user-create exit-dev Exit-pass-123 exit-dev@example.com --users-groups exit-team --url {url} --user admin --password {password}"
    Then the command succeeds
    # The CLI's permission target template: comma-separated strings.
    Given a file "pt.json" with content:
      """
      {"name": "exit-team-read", "repo": {"repositories": "team-a-local,team-b-local",
        "include-patterns": "**", "actions-groups": {"exit-team": "read"}}}
      """
    When I run jf "rt permission-target-create {dir}/pt.json --url {url} --user admin --password {password}"
    Then the command succeeds

    # jf atc mints a token for another user only with an admin access token.
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "admin-token"
    When I run jf "atc exit-dev --url {platform}/ --access-token {admin-token} --expiry 7200"
    Then the command succeeds
    And I keep the JSON field "access_token" of the output as "dev-token"
    When I run jf "c add dev --url {platform}/ --access-token {dev-token} --interactive=false"
    Then the command succeeds
    When I run jf "rt dl team-a-local/a.txt dev/ --flat --fail-no-op --server-id dev"
    Then the command succeeds
    And the files "dev/a.txt" and "a.txt" are identical
    When I run jf "rt dl team-b-local/b.txt dev/ --flat --fail-no-op --server-id dev"
    Then the command succeeds
    And the files "dev/b.txt" and "b.txt" are identical

    # The same user's token scoped to team-a-local: the group's read on
    # team-b-local does not reach it (scopes only narrow, decision 0005).
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"username":"exit-dev","scope":"artifact:team-a-local:r","expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "scoped-token"
    When I run jf "c add scoped --url {platform}/ --access-token {scoped-token} --interactive=false"
    Then the command succeeds
    When I run jf "rt dl team-a-local/a.txt scoped/ --flat --fail-no-op --server-id scoped"
    Then the command succeeds
    And the files "scoped/a.txt" and "a.txt" are identical
    When I run jf "rt dl team-b-local/b.txt scoped/ --flat --fail-no-op --server-id scoped"
    Then the command fails
    And the file "scoped/b.txt" does not exist
    When I GET "/artifactory/team-b-local/b.txt" with the kept token "scoped-token"
    Then the response status is 403
    When I GET "/artifactory/team-a-local/a.txt" with the kept token "scoped-token"
    Then the response status is 200
