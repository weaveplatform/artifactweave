@jf @cdn
Feature: CDN delivery through Azure Front Door
  Decision 0029: a repository with cdnRedirect on an azure store with a
  Front Door answers a download with a 302 to the Front Door endpoint
  carrying a read SAS fixed to the current window. Front Door checks no
  signature: the edge is pkg/cdn/cdntest's Front Door fake, which passes the
  SAS through to Azurite, the registry's own storage, and Azurite checks it.
  The edge caches whole-object responses under the full URL, SAS included,
  so a window's repeated URL is served from the cache. The registry listens
  on 127.0.0.1 and the edge on localhost, so clients drop their credentials
  on the redirect, as they do for a real CDN.

  Background:
    Given the registry is running on Azure behind a Front Door edge
    And the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/fd-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic","cdnRedirect":true}
      """
    And a file "big.bin" of 6000000 bytes

  Scenario: jf rt dl splits a large download across Front Door without credentials
    When I run jf "rt u big.bin fd-local/a/big.bin --url {url} --user admin --password {password}"
    Then the command succeeds
    # Above the CLI's 5,120 KiB split threshold: the registry answers the
    # HEAD itself and redirects each ranged GET to the edge, which passes it
    # with its SAS to Azurite.
    When I run jf "rt dl fd-local/a/big.bin {dir}/out/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "big.bin" and "out/big.bin" are identical
    And the CDN edge served at least 2 ranged requests
    And the CDN edge never received an Authorization header
    And the CDN edge refuses the last path unsigned

  Scenario: one window's URL is served from the edge's cache
    When I run jf "rt u big.bin fd-local/b/big.bin --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/fd-local/b/big.bin" as admin
    Then the response status is 200
    And the response header "Content-Disposition" contains "big.bin"
    When I GET "/artifactory/fd-local/b/big.bin" as admin
    Then the response status is 200
    And the response header "Content-Disposition" contains "big.bin"
    And the CDN edge served 2 requests
    And the CDN edge answered 1 request from its cache
    And the CDN edge never received an Authorization header
