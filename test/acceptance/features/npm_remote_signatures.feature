@npm @npm_remote
Feature: npm audit signatures verifies what an npm remote repository relays
  An npm remote repository relays its upstream's dist.signatures, and
  answers its own /-/npm/v1/keys with the upstream's keys document, so npm
  audit signatures verifies a package installed through a remote of a
  signing upstream, as 7.x's npm documentation has it: "Artifactory
  provides the signature directly from the remote repository" (decision
  0034, "Signatures"). The fake upstream signs each version with a key it
  lists. Before it asks the registry, npm initialises the Sigstore TUF
  client, which needs tuf-repo-cdn.sigstore.dev (docs/testing/npm.md).

  Scenario Outline: <npm> verifies the signatures an npm remote relays from its upstream
    Given the registry is running
    And the <npm> client can reach the registry
    And an npm upstream serves "remote-demo" at "1.0.0"
    And an npm upstream serves "@acme/remote-scoped" at "1.0.0"
    When I PUT "/artifactory/api/repositories/npm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"npm","url":"{upstream}"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-proxy/
      //{host}/artifactory/api/npm/npm-proxy/:_auth={basicauth}
      """
    And a file "package.json" with content:
      """
      {"name":"remote-consumer","version":"1.0.0","dependencies":{"remote-demo":"1.0.0","@acme/remote-scoped":"1.0.0"}}
      """
    When I run npm "install --ignore-scripts"
    Then the command succeeds
    When I run npm "audit signatures"
    Then the command succeeds
    And the output contains "2 packages have verified registry signatures"
    And the generic upstream received 1 GET requests for "/-/npm/v1/keys"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |
