Feature: Deploy and download generic artifacts
  Generic deploys, downloads and deletes through the item paths (research 02),
  as plain HTTP clients and the JFrog CLI drive them (research 09).

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """

  Scenario: Deploy, download, checksum deploy and delete over HTTP
    When I PUT "/artifactory/generic-local/dir/hello.txt;team=core" as admin with body:
      """
      hello artifactweave
      """
    Then the response status is 201
    And the JSON field "path" equals "/dir/hello.txt"
    And the JSON field "checksums.sha1" equals "afe5052f1ac78ab0db1811ea8aa0107e0e186371"
    When I GET "/artifactory/generic-local/dir/hello.txt" as admin
    Then the response status is 200
    And the response body is "hello artifactweave"
    And the response header "Accept-Ranges" contains "bytes"
    And the response header "X-Checksum-Sha1" contains "afe5052f1ac78ab0db1811ea8aa0107e0e186371"
    When the next request has header "Range" "bytes=6-"
    And I GET "/artifactory/generic-local/dir/hello.txt" as admin
    Then the response status is 206
    And the response body is "artifactweave"
    When I GET "/artifactory/generic-local/dir/hello.txt.sha1" as admin
    Then the response body is "afe5052f1ac78ab0db1811ea8aa0107e0e186371"
    When the next request has header "X-Checksum-Deploy" "true"
    And the next request has header "X-Checksum-Sha1" "afe5052f1ac78ab0db1811ea8aa0107e0e186371"
    And I PUT "/artifactory/generic-local/copy/hello.txt" as admin
    Then the response status is 201
    When I GET "/artifactory/generic-local/copy/hello.txt" as admin
    Then the response body is "hello artifactweave"
    When I DELETE "/artifactory/generic-local/dir/hello.txt" as admin
    Then the response status is 204
    When I GET "/artifactory/generic-local/dir/hello.txt" as admin
    Then the response status is 404
    When I GET "/artifactory/api/v1/uploads/config" as admin
    Then the JSON field "supported" equals "true"

  @jf
  Scenario: jf rt u with a checksum deploy, jf rt s, and a split jf rt dl
    Given the JFrog CLI is available
    And a file "big.bin" of 65536 bytes
    When I run jf "rt u {dir}/big.bin generic-local/a/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u {dir}/big.bin generic-local/b/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 201 to PUT "/artifactory/generic-local/b/big.bin"
    And the server never answered 404 to PUT "/artifactory/generic-local/b/big.bin"
    When I run jf "rt s generic-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "generic-local/b/big.bin"
    When I run jf "rt dl generic-local/a/big.bin {dir}/out/ --flat --split-count=2 --min-split=16 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "big.bin" and "out/big.bin" are identical
    And the server answered 206 to GET "/artifactory/generic-local/a/big.bin"

  # A checksum deploy reuses stored content only for a caller that may read
  # a file holding it (#379). The CLI takes the 404 as a miss and uploads.
  @jf
  Scenario: jf rt u uploads content the user may not read instead of reusing it
    Given the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/hidden-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/api/security/users/deployer" as admin with body:
      """
      {"name": "deployer", "email": "deployer@example.com", "password": "Deployer-pass1"}
      """
    # A new user joins readers, which reads every repository.
    And I PATCH "/access/api/v2/groups/readers/members" as admin with JSON:
      """
      {"remove": ["deployer"]}
      """
    And I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name": "deployer-generic", "resources": {"artifact": {
        "actions": {"users": {"deployer": ["READ", "WRITE"]}},
        "targets": {"generic-local": {"include_patterns": ["**"], "exclude_patterns": []}}}}}
      """
    And a file "big.bin" of 65536 bytes
    When I run jf "rt u {dir}/big.bin hidden-local/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u {dir}/big.bin generic-local/mine/ --flat --url {url} --user deployer --password Deployer-pass1"
    Then the command succeeds
    And the server answered 404 to PUT "/artifactory/generic-local/mine/big.bin"
    And the server answered 201 to PUT "/artifactory/generic-local/mine/big.bin"
    # Now a file the user may read holds the content, so the next one hits.
    When I run jf "rt u {dir}/big.bin generic-local/again/ --flat --url {url} --user deployer --password Deployer-pass1"
    Then the command succeeds
    And the server answered 201 to PUT "/artifactory/generic-local/again/big.bin"
    And the server never answered 404 to PUT "/artifactory/generic-local/again/big.bin"
    When I GET "/artifactory/generic-local/mine/big.bin" as "deployer" with password "Deployer-pass1"
    Then the response status is 200

  # Replacing a file with its own content needs deploy alone, as on 7.x
  # (#400), so a CI job that runs jf rt u again with a user who may not
  # delete succeeds: the small file is uploaded with its checksums, the big
  # one deployed by checksum. Other content still needs delete.
  @jf
  Scenario: jf rt u run again by a user who may deploy but not delete
    Given the JFrog CLI is available
    And I PUT "/artifactory/api/security/users/ci" as admin with body:
      """
      {"name": "ci", "email": "ci@example.com", "password": "Ci-pass-1"}
      """
    # A new user joins readers, which reads every repository.
    And I PATCH "/access/api/v2/groups/readers/members" as admin with JSON:
      """
      {"remove": ["ci"]}
      """
    And I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name": "ci-generic", "resources": {"artifact": {
        "actions": {"users": {"ci": ["READ", "WRITE"]}},
        "targets": {"generic-local": {"include_patterns": ["**"], "exclude_patterns": []}}}}}
      """
    And a file "small.txt" with content:
      """
      small
      """
    And a file "big.bin" of 65536 bytes
    When I run jf "rt u {dir}/small.txt generic-local/ci/ --flat --url {url} --user ci --password Ci-pass-1"
    Then the command succeeds
    When I run jf "rt u {dir}/big.bin generic-local/ci/ --flat --url {url} --user ci --password Ci-pass-1"
    Then the command succeeds
    When I run jf "rt u {dir}/small.txt generic-local/ci/ --flat --url {url} --user ci --password Ci-pass-1"
    Then the command succeeds
    When I run jf "rt u {dir}/big.bin generic-local/ci/ --flat --url {url} --user ci --password Ci-pass-1"
    Then the command succeeds
    And the server never answered 403 to PUT "/artifactory/generic-local/ci/small.txt"
    And the server never answered 403 to PUT "/artifactory/generic-local/ci/big.bin"
    Given a file "small.txt" with content:
      """
      changed
      """
    When I run jf "rt u {dir}/small.txt generic-local/ci/ --flat --url {url} --user ci --password Ci-pass-1"
    Then the command fails
    And the server answered 403 to PUT "/artifactory/generic-local/ci/small.txt"
    When I GET "/artifactory/generic-local/ci/small.txt" as "ci" with password "Ci-pass-1"
    Then the response body is "small"
