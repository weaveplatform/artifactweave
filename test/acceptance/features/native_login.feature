@native_login
Feature: Native clients share optional browser approval
  Browser approval is a small HTML form, independent of the general console.
  Each client obtains a normal revocable P4 token through its actual protocol.

  @npm
  Scenario Outline: Pinned npm web login uses the approved credential
    Given the registry supports native browser approval
    And the <client> client can reach the registry
    When <client> completes browser approval and uses its stored credential
    Then the native approval token preserves permissions and is revocable

    Examples:
      | client |
      | npm    |
      | npm 12 |

  @npm @native_login_disabled
  Scenario Outline: Disabled approval never issues a browser credential
    Given the registry has native browser approval disabled
    And the <client> client can reach the registry
    When <client> is refused browser credentials while approval is disabled

    Examples:
      | client |
      | npm    |
      | npm 12 |

  @terraform
  Scenario: Terraform uses PKCE and its actual localhost callback
    Given the registry supports native browser approval
    And the npm client can reach the registry
    And terraform is available
    When Terraform completes browser approval and initializes a protected backend
    Then the native approval token preserves permissions and is revocable
