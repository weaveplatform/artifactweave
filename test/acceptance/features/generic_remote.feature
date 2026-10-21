@jf @generic_remote
Feature: Generic remote retrieval through native clients
  The upstream fixture measures our HTTP behavior, not an observed Artifactory response.
  A reader without Deploy can fill a remote cache. Cache aliases never contact upstream.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And a generic upstream serves "/curl.bin" with content "curl remote bytes"
    And a generic upstream serves "/jf.bin" with content "jf remote bytes"
    And I PUT "/artifactory/api/repositories/remote" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{upstream}"}
      """
    And the response status is 200
    And I PUT "/artifactory/api/security/users/reader" as admin with body:
      """
      {"name":"reader","email":"reader@example.com","password":"Reader-pass1"}
      """
    And the response status is 201
    And I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"remote-read","resources":{"artifact":{"actions":{"users":{"reader":["READ"]}},"targets":{"remote":{"include_patterns":["**"]}}}}}
      """
    And the response status is 201

  Scenario: curl and JFrog CLI fetch cold bytes and reuse cache through an outage
    When I GET "/artifactory/remote-cache/curl.bin" as "reader" with password "Reader-pass1"
    Then the response status is 404
    And the generic upstream received 0 GET requests for "/curl.bin"
    And the generic upstream received 0 HEAD requests for "/curl.bin"
    When I run curl "--fail --silent --show-error --user reader:Reader-pass1 {url}/remote/curl.bin --output curl-cold.bin"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/curl.bin"
    When I run jf "rt direct-download remote/jf.bin jf-cold/ --flat --fail-no-op --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/jf.bin"
    When I GET "/artifactory/api/storage/remote-cache/curl.bin" as "reader" with password "Reader-pass1"
    Then the response status is 200
    And the JSON field "repo" equals "remote-cache"
    When I run jf "rt s remote-cache/*.bin --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the output contains "remote-cache/jf.bin"
    And the output contains "remote-cache/curl.bin"
    Given the generic upstream stops
    When I run curl "--fail --silent --show-error --user reader:Reader-pass1 {url}/remote/curl.bin --output curl-warm.bin"
    Then the command succeeds
    And the files "curl-cold.bin" and "curl-warm.bin" are identical
    When I run jf "rt direct-download remote/jf.bin jf-warm/ --flat --fail-no-op --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the files "jf-cold/jf.bin" and "jf-warm/jf.bin" are identical
    When I run jf "rt dl remote-cache/*.bin alias/ --flat --fail-no-op --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the files "curl-cold.bin" and "alias/curl.bin" are identical
    And the files "jf-cold/jf.bin" and "alias/jf.bin" are identical
    And the generic upstream received 1 GET requests for "/curl.bin"
    And the generic upstream received 1 GET requests for "/jf.bin"
    When I DELETE "/artifactory/api/repositories/remote" as admin
    Then the response status is 200
    When I GET "/artifactory/api/storage/remote-cache/jf.bin" as admin
    Then the response status is 404
