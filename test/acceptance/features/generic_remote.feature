@jf @generic_remote
Feature: Generic remote retrieval through native clients
  The upstream fixture measures our HTTP behavior, not an observed Artifactory response.
  A reader fills a remote cache with Deploy/Cache beside Read, as 7.x requires (#1587).
  Cache aliases never contact upstream.

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
      {"name":"remote-read","resources":{"artifact":{"actions":{"users":{"reader":["READ","WRITE"]}},"targets":{"remote":{"include_patterns":["**"]}}}}}
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
    And the server answered no request with 500 since the last command
    And the generic upstream received 1 GET requests for "/jf.bin"
    When I GET "/artifactory/api/storage/remote-cache/curl.bin" as "reader" with password "Reader-pass1"
    Then the response status is 200
    And the JSON field "repo" equals "remote-cache"
    When I run jf "rt s remote-cache/*.bin --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output contains "remote-cache/jf.bin"
    And the output contains "remote-cache/curl.bin"
    Given the generic upstream stops
    When I run curl "--fail --silent --show-error --user reader:Reader-pass1 {url}/remote/curl.bin --output curl-warm.bin"
    Then the command succeeds
    And the files "curl-cold.bin" and "curl-warm.bin" are identical
    When I run jf "rt direct-download remote/jf.bin jf-warm/ --flat --fail-no-op --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the files "jf-cold/jf.bin" and "jf-warm/jf.bin" are identical
    When I run jf "rt dl remote-cache/*.bin alias/ --flat --fail-no-op --url {url} --user reader --password Reader-pass1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the files "curl-cold.bin" and "alias/curl.bin" are identical
    And the files "jf-cold/jf.bin" and "alias/jf.bin" are identical
    And the generic upstream received 1 GET requests for "/curl.bin"
    And the generic upstream received 1 GET requests for "/jf.bin"
    When I DELETE "/artifactory/api/repositories/remote" as admin
    Then the response status is 200
    When I GET "/artifactory/api/storage/remote-cache/jf.bin" as admin
    Then the response status is 404

  Scenario: jf rt direct-download through a virtual takes a local copy before the remote cache
    # The virtual lists the remote first; 7.x still searches every local
    # member before any remote cache (decision 0008, R1; #1252).
    When I run curl "--fail --silent --show-error --user reader:Reader-pass1 {url}/remote/jf.bin --output cached.bin"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/jf.bin"
    When I PUT "/artifactory/api/repositories/order-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/order-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"generic","repositories":["remote","order-local"]}
      """
    Then the response status is 200
    When I PUT "/artifactory/order-local/jf.bin" as admin with body:
      """
      local bytes
      """
    Then the response status is 201
    When I run jf "rt direct-download order-virtual/jf.bin virtual/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file "virtual/jf.bin" contains "local bytes"
    And the generic upstream received 1 GET requests for "/jf.bin"
