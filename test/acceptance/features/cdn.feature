@jf @cdn
Feature: CDN delivery
  Decision 0029: a repository with cdnRedirect, or downloadRedirect, on a
  store with a CDN answers a download with a 302 to the CDN's signed URL:
  CloudFront in front of an s3 store, Cloud CDN in front of a gcs store.
  The edge is pkg/cdn/cdntest's fake: it checks the signature as the real
  CDN does and serves the blob from the registry's own bucket, in MinIO or
  fake-gcs-server. The registry listens on 127.0.0.1 and the edge on
  localhost, so clients treat them as different hosts and drop their
  credentials on the redirect, as they do for a real CDN.

  Scenario Outline: jf rt dl splits a large download across the <cdn> edge without credentials
    Given the registry is running on <store> behind a <cdn> edge
    And the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/cdn-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic","cdnRedirect":true}
      """
    And a file "big.bin" of 6000000 bytes
    When I run jf "rt u big.bin cdn-local/a/big.bin --url {url} --user admin --password {password}"
    Then the command succeeds
    # Above the CLI's 5,120 KiB split threshold: it sends a HEAD, which the
    # registry answers itself, then ranged GETs, each redirected to the edge.
    When I run jf "rt dl cdn-local/a/big.bin {dir}/out/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "big.bin" and "out/big.bin" are identical
    And the CDN edge served at least 2 ranged requests
    And the CDN edge never received an Authorization header
    And the CDN edge refuses the last path unsigned

    Examples:
      | store | cdn        |
      | S3    | CloudFront |
      | GCS   | Cloud CDN  |

  Scenario: the redirect header keeps a download on the registry
    Given the registry is running on S3 behind a CloudFront edge
    And the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/cdn-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic","cdnRedirect":true}
      """
    And a file "big.bin" of 6000000 bytes
    When I run jf "rt u big.bin cdn-local/b/big.bin --url {url} --user admin --password {password}"
    Then the command succeeds
    When the next request has header "X-JFrog-Download-Redirect-To" "None"
    And I GET "/artifactory/cdn-local/b/big.bin" as admin
    Then the response status is 200
    And the CDN edge served 0 requests
    When I GET "/artifactory/cdn-local/b/big.bin" as admin
    Then the response status is 200
    And the response header "Content-Disposition" contains "big.bin"
    And the CDN edge served 1 request
    And the CDN edge never received an Authorization header
