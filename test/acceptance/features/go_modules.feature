@goproxy
Feature: Go module repositories answer the GOPROXY protocol
  A local Go repository's reads at /artifactory/api/go/{repo} (decision
  0035): a version is listed, served and resolved once both its .mod and its
  .zip are stored, its .info is synthesised when none was deployed, and the
  checksum database answers 404 to anyone, so the go command consults
  sum.golang.org itself.

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/go-local" as admin with body:
      """
      {"rclass": "local", "packageType": "go"}
      """
    Then the response status is 200

  Scenario: The GOPROXY reads over HTTP
    When I PUT "/artifactory/go-local/example.com/acme/lib/@v/v1.0.0.mod" as admin with body:
      """
      module example.com/acme/lib
      """
    Then the response status is 201
    # Not visible until its .zip is stored too.
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/list" as admin
    Then the response status is 404
    And the response header "Content-Type" contains "text/plain"
    When I PUT "/artifactory/go-local/example.com/acme/lib/@v/v1.0.0.zip" as admin with body:
      """
      not inspected
      """
    Then the response status is 201
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/list" as admin
    Then the response status is 200
    And the response body is "v1.0.0"
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/v1.0.0.info" as admin
    Then the response status is 200
    And the JSON field "Version" equals "v1.0.0"
    And the JSON field "Time" is not empty
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@latest" as admin
    Then the response status is 200
    And the JSON field "Version" equals "v1.0.0"
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/v1.0.0.mod" as admin
    Then the response status is 200
    And the response body is "module example.com/acme/lib"
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/v1.0.0.zip" as admin
    Then the response status is 200
    And the response header "X-Checksum-Sha256" contains "0"
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/v1.0.0.zip"
    Then the response status is 401
    When I GET "/artifactory/api/go/go-local/sumdb/sum.golang.org/supported"
    Then the response status is 404

  @go
  Scenario: go mod download resolves modules through the Go API
    Given the Go toolchain is available
    And a Go module "example.com/acme/lib" at "v1.0.0" laid out as a module proxy under "goproxy"
    # An upper-case path element, which the go command case-encodes as !corp.
    And a Go module "example.com/Corp/Upper" at "v1.2.0" laid out as a module proxy under "goproxy"
    When I deploy the files under "goproxy" to "/artifactory/go-local" as admin
    And the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/api/go/go-local"
    And I run go "mod download -json example.com/acme/lib@latest"
    Then the command succeeds
    And the output contains "v1.0.0.zip"
    And the output contains "GoModSum"
    When I run go "mod download -json example.com/Corp/Upper@v1.2.0"
    Then the command succeeds
    And the output contains "!corp/!upper/@v/v1.2.0.zip"
