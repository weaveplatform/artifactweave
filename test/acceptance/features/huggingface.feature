@huggingface
Feature: Native Hugging Face model and dataset repositories
  The pinned Hub client uses current model snapshots and Git LFS for larger files.

  Scenario Outline: Hub client publishes through <class>
    Given the registry is running
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"huggingfaceml"}
      """
    Then the response status is 200
    When the pinned Hugging Face client publishes and downloads model and dataset through "<repo>"

    Examples:
      | class     | repo     |
      | local     | hf-local |
      | federated | hf-fed   |

  Scenario: Hub client resolves through a virtual member
    Given the registry is running
    When I PUT "/artifactory/api/repositories/hf-member" as admin with body:
      """
      {"rclass":"local","packageType":"huggingfaceml"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/hf-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"huggingfaceml","repositories":["hf-member"]}
      """
    Then the response status is 200
    When the pinned Hugging Face client publishes and downloads model and dataset through "hf-member"
    And the pinned Hugging Face client downloads model and dataset through "hf-virtual"

  Scenario: Hub client resolves remote content from cache while upstream is offline
    Given the registry is running
    And a controlled Hugging Face model and dataset upstream is available
    When I PUT "/artifactory/api/repositories/hf-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"huggingfaceml","url":"{upstream}"}
      """
    Then the response status is 200
    When the pinned Hugging Face client downloads model and dataset through "hf-remote"
    Given the generic upstream stops
    When the pinned Hugging Face client downloads model and dataset through "hf-remote"
