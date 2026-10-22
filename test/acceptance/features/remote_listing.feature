@remote_listing
Feature: Bounded remote browsing through curl
  These controlled indexes prove artifactweave behavior, not vendor parity.

  Background:
    Given the registry is running
    And a remote directory index at "/" contains:
      """
      <!DOCTYPE html><h1>Index of /</h1><a href="nested/">nested</a><a href="../escape">parent</a>
      """
    And a remote directory index at "/nested/" contains:
      """
      <!DOCTYPE html><h1>Index of /nested</h1><a href="uncached.bin?secret=private">file</a><a href="private.bin">private</a><a href="https://foreign.invalid/secret">foreign</a>
      """
    And I PUT "/artifactory/api/repositories/browse" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{upstream}","excludesPattern":"nested/private.bin"}
      """
    And the response status is 200

  Scenario: Browsing exposes uncached children without fetching their bytes
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/browse/"
    Then the command succeeds
    And the output does not contain "nested/"
    And the generic upstream received 0 GET requests for "/"
    When I POST "/artifactory/api/repositories/browse" as admin with body:
      """
      {"listRemoteFolderItems":true}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/browse/"
    Then the command succeeds
    And the output contains "nested/"
    And the output does not contain "escape"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/browse/nested/"
    Then the command succeeds
    And the output contains "uncached.bin"
    And the output does not contain "private"
    And the output does not contain "foreign.invalid"
    And the generic upstream received 0 GET requests for "/nested/uncached.bin"
    When I GET "/artifactory/api/storage/browse/nested/uncached.bin" as admin
    Then the response status is 404
    When I GET "/artifactory/browse-cache/" as admin
    Then the response status is 200
    And the response body does not contain "nested/"
    And the generic upstream received 1 GET requests for "/"
    And the generic upstream received 1 GET requests for "/nested/"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/browse" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/browse/nested/"
    Then the command succeeds
    And the output contains "uncached.bin"
    And the generic upstream received 1 GET requests for "/nested/"
