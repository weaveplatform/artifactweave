@huggingface @huggingface_xet
Feature: Native Xet transfer for model and dataset repositories
  Xet is explicitly enabled and ordinary large-file HTTP downloads are rejected by the client proof proxy.

  Scenario Outline: Hub client publishes through <class>
    Given the registry is running with Hugging Face Xet
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"huggingfaceml"}
      """
    Then the response status is 200
    When the pinned Xet client publishes and downloads model and dataset through "<repo>"

    Examples:
      | class     | repo     |
      | local     | hf-local |
      | federated | hf-fed   |

  Scenario: Hub client resolves through a virtual member
    Given the registry is running with Hugging Face Xet
    When I PUT "/artifactory/api/repositories/hf-member" as admin with body:
      """
      {"rclass":"local","packageType":"huggingfaceml"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/hf-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"huggingfaceml","repositories":["hf-member"],"defaultDeploymentRepo":"hf-member"}
      """
    Then the response status is 200
    When the pinned Xet client publishes and downloads model and dataset through "hf-virtual"
    And the pinned Xet client downloads model and dataset through "hf-virtual"

  Scenario: Hub client resolves remote content from cache while upstream is offline
    Given the registry is running with Hugging Face Xet
    And a controlled Hugging Face model and dataset upstream is available
    When I PUT "/artifactory/api/repositories/hf-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"huggingfaceml","url":"{upstream}"}
      """
    Then the response status is 200
    When the pinned Xet client downloads model and dataset through "hf-remote"
    Given the generic upstream stops
    When the pinned Xet client downloads model and dataset through "hf-remote"
