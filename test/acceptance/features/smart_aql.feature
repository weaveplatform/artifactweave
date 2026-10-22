@jf @smart_aql
Feature: Transitive AQL discovers upstream items without inventing cache entries
  A controlled peer demonstrates our behavior; Artifactory wire parity remains a capture obligation.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And a controlled Smart Remote peer serves an artifact
    And a file "workspace.marker" with content:
      """
      Transitive AQL native fixture
      """
    When I PUT "/artifactory/api/repositories/smart" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{smart_upstream}","username":"peer","password":"peer-secret","contentSynchronisation":{"enabled":true}}
      """
    Then the response status is 200

  Scenario: Ordinary JFrog transitive search and download use returned cold remote paths
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"smart"})
      """
    Then the response status is 200
    And the JSON field "results" has 0 entries
    When I run jf "rt search smart/*.bin --transitive=true --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "smart/file.bin"
    When I GET "/artifactory/smart-cache/file.bin" as admin
    Then the response status is 404
    Given the JFrog CLI runs with "JFROG_CLI_TRANSITIVE_DOWNLOAD" set to "true"
    When I run jf "rt download smart/*.bin cold/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/smart-cache/file.bin --output cached.bin"
    Then the command succeeds
    And the files "cold/file.bin" and "cached.bin" are identical
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"smart"})
      """
    Then the response status is 200
    And the JSON field "results" has 1 entries
    And the JSON field "results.0.repo" equals "smart-cache"
    Given the JFrog CLI runs with "JFROG_CLI_TRANSITIVE_DOWNLOAD" set to "false"
    And the Smart peer becomes unavailable
    When I run jf "rt download smart/*.bin offline/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "cold/file.bin" and "offline/file.bin" are identical
