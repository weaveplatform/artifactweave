Feature: Upstream registry credentials are independent of local registry tokens
  The source foundation authenticates against a controlled Distribution registry.
  Native client routing and cache publication are qualified by the registry adapter.

  Scenario Outline: Exchange and reuse a pull token
    Given a controlled registry for upstream user "<user>"
    When the source pulls "team/image" twice using a local registry bearer token
    Then the token service received one exchange for "repository:team/image:pull"
    When the upstream token expires
    And the source pulls "team/image" twice using a local registry bearer token
    Then the token service received two exchanges for "repository:team/image:pull"

    Examples:
      | user   |
      |        |
      | reader |

  Scenario: Tokens remain isolated between image scopes
    Given a controlled registry for upstream user "reader"
    When the source pulls "team/image" twice using a local registry bearer token
    And the source pulls "other/image" twice using a local registry bearer token
    Then the token service received separate scopes for both images
