@goproxy
Feature: Go module repositories answer the GOPROXY protocol
  A local Go repository's reads at /artifactory/api/go/{repo} (decision
  0035): a version is listed with its time, served and resolved once both
  its .mod and its .zip are stored, its .info is synthesised when none was
  deployed, and the checksum database answers 404 to anyone, so the go
  command consults sum.golang.org itself. The scenarios tagged @jf are the P7 Go exit checks:
  the JFrog CLI publishes and the go command consumes, as a team using a
  local Go repository would.

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
    # Each line carries the version's time: here, without a .info, when
    # it was stored.
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/list" as admin
    Then the response status is 200
    And the response body contains "v1.0.0 20"
    When I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/v1.0.0.info" as admin
    Then the response status is 200
    And the JSON field "Version" equals "v1.0.0"
    And the JSON field "Time" is not empty
    # A pseudo-version is listed too, with the Time its .info gives.
    When I PUT "/artifactory/go-local/example.com/acme/lib/@v/v1.0.1-0.20261001000000-abcdefabcdef.mod" as admin with body:
      """
      module example.com/acme/lib
      """
    And I PUT "/artifactory/go-local/example.com/acme/lib/@v/v1.0.1-0.20261001000000-abcdefabcdef.zip" as admin with body:
      """
      not inspected
      """
    And I PUT "/artifactory/go-local/example.com/acme/lib/@v/v1.0.1-0.20261001000000-abcdefabcdef.info" as admin with body:
      """
      {"Version":"v1.0.1-0.20261001000000-abcdefabcdef","Time":"2026-10-01T00:00:00Z"}
      """
    And I GET "/artifactory/api/go/go-local/example.com/acme/lib/@v/list" as admin
    Then the response status is 200
    And the response body contains "v1.0.1-0.20261001000000-abcdefabcdef 2026-10-01T00:00:00Z"
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

  # The P7 Go exit checks (#275) start here. jf go-config writes the
  # project's .jfrog/projects/go.yaml, so each scenario runs jf in the
  # module's own directory.
  @go @jf
  Scenario: jf go-publish with a build and jf rt bp, then go downloads and builds against it
    Given the Go toolchain is available
    And the JFrog CLI is available
    And a file "greet/go.mod" with content:
      """
      module example.com/acme/greet

      go 1.22
      """
    And a file "greet/greet.go" with content:
      """
      // Package greet is the module the scenario publishes.
      package greet

      // Greeting is what the consumer reads.
      const Greeting = "hello"
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "go-config --server-id-resolve aw --server-id-deploy aw --repo-resolve go-local --repo-deploy go-local" in "greet"
    Then the command succeeds
    When I run jf "gp v1.0.0 --build-name go-accept --build-number 1" in "greet"
    Then the command succeeds
    When I run jf "rt bp go-accept 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/go-accept/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.id" equals "example.com/acme/greet"
    And the JSON field "buildInfo.modules.0.artifacts" has 3 entries
    When I GET "/artifactory/api/go/go-local/example.com/acme/greet/@v/list" as admin
    Then the response status is 200
    And the response body contains "v1.0.0 20"
    Given the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/api/go/go-local"
    When I run go "mod download -json example.com/acme/greet@v1.0.0"
    Then the command succeeds
    And the output contains "GoModSum"
    Given a file "app/go.mod" with content:
      """
      module example.com/acme/app

      go 1.22

      require example.com/acme/greet v1.0.0
      """
    And a file "app/app.go" with content:
      """
      // Package app consumes the published module.
      package app

      import "example.com/acme/greet"

      // Message is built from the published module's constant.
      const Message = greet.Greeting + ", world"
      """
    When I run go "mod tidy" in "app"
    Then the command succeeds
    When I run go "build ./..." in "app"
    Then the command succeeds
    And the file "app/go.sum" contains "example.com/acme/greet v1.0.0 h1:"

  @go @jf
  Scenario: go list -m -versions and @latest; @v/list lists a pseudo-version, which go leaves out
    Given the Go toolchain is available
    And the JFrog CLI is available
    And a file "greet/go.mod" with content:
      """
      module example.com/acme/greet

      go 1.22
      """
    And a file "greet/greet.go" with content:
      """
      // Package greet is the module the scenario publishes.
      package greet
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    And I run jf "go-config --server-id-resolve aw --server-id-deploy aw --repo-resolve go-local --repo-deploy go-local" in "greet"
    And I run jf "gp v1.0.0" in "greet"
    Then the command succeeds
    When I run jf "gp v1.1.0" in "greet"
    Then the command succeeds
    When I run jf "gp v1.1.1-0.20261001000000-abcdefabcdef" in "greet"
    Then the command succeeds
    # Listed with the time of its .info, as Artifactory 7.x lists it; the
    # go command takes tagged versions only from the list and still picks
    # v1.1.0 for @latest.
    When I GET "/artifactory/api/go/go-local/example.com/acme/greet/@v/list" as admin
    Then the response body contains "v1.1.1-0.20261001000000-abcdefabcdef 20"
    Given the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/api/go/go-local"
    When I run go "list -m -versions example.com/acme/greet"
    Then the command succeeds
    And the output contains "example.com/acme/greet v1.0.0 v1.1.0"
    And the output does not contain "v1.1.1-0.20261001000000-abcdefabcdef"
    When I run go "list -m example.com/acme/greet@latest"
    Then the command succeeds
    And the output contains "example.com/acme/greet v1.1.0"
    When I run go "mod download -json example.com/acme/greet@v1.1.1-0.20261001000000-abcdefabcdef"
    Then the command succeeds
    And the output contains "v1.1.1-0.20261001000000-abcdefabcdef.zip"

  @go @jf
  Scenario: A module path with upper case is published plain and downloaded case-encoded
    Given the Go toolchain is available
    And the JFrog CLI is available
    And a file "upper/go.mod" with content:
      """
      module example.com/Acme/Upper

      go 1.22
      """
    And a file "upper/upper.go" with content:
      """
      // Package upper is the module the scenario publishes.
      package upper
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    And I run jf "go-config --server-id-resolve aw --server-id-deploy aw --repo-resolve go-local --repo-deploy go-local" in "upper"
    And I run jf "gp v1.0.0" in "upper"
    Then the command succeeds
    When I GET "/artifactory/api/storage/go-local/example.com/Acme/Upper/@v/v1.0.0.zip" as admin
    Then the response status is 200
    Given the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/api/go/go-local"
    When I run go "mod download -json example.com/Acme/Upper@v1.0.0"
    Then the command succeeds
    And the output contains "!acme/!upper/@v/v1.0.0.zip"
    And the server answered 200 to GET "/artifactory/api/go/go-local/example.com/!acme/!upper/@v/v1.0.0.zip"

  @go @jf
  Scenario: A +incompatible version of a module without a major version suffix
    Given the Go toolchain is available
    And the JFrog CLI is available
    And a file "legacy/go.mod" with content:
      """
      module example.com/acme/legacy
      """
    And a file "legacy/legacy.go" with content:
      """
      // Package legacy is the module the scenario publishes.
      package legacy
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    And I run jf "go-config --server-id-resolve aw --server-id-deploy aw --repo-resolve go-local --repo-deploy go-local" in "legacy"
    And I run jf "gp v2.0.0+incompatible" in "legacy"
    Then the command succeeds
    # Below v2 the go command refuses +incompatible, and so does the server.
    When I run jf "gp v1.0.0+incompatible" in "legacy"
    Then the command fails
    And the server answered 400 to PUT "/artifactory/api/go/go-local/example.com/acme/legacy/@v/v1.0.0+incompatible.zip"
    Given the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/api/go/go-local"
    When I run go "list -m -versions example.com/acme/legacy"
    Then the command succeeds
    And the output contains "example.com/acme/legacy v2.0.0+incompatible"
    When I run go "mod download -json example.com/acme/legacy@v2.0.0+incompatible"
    Then the command succeeds
    And the output contains "GoModSum"

  @go @jf
  Scenario: Republishing a version: the same content again succeeds, changed content gets 409
    Given the JFrog CLI is available
    And a file "greet/go.mod" with content:
      """
      module example.com/acme/greet

      go 1.22
      """
    And a file "greet/greet.go" with content:
      """
      // Package greet is the module the scenario publishes.
      package greet

      // Greeting is what the consumer reads.
      const Greeting = "hello"
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    And I run jf "go-config --server-id-resolve aw --server-id-deploy aw --repo-resolve go-local --repo-deploy go-local" in "greet"
    And I run jf "gp v1.0.0" in "greet"
    Then the command succeeds
    # A pipeline re-run: the .zip and .mod are the same (DV-039).
    When I run jf "gp v1.0.0" in "greet"
    Then the command succeeds
    Given a file "greet/greet.go" with content:
      """
      // Package greet is the module the scenario publishes.
      package greet

      // Greeting is what the consumer reads.
      const Greeting = "changed"
      """
    When I run jf "gp v1.0.0" in "greet"
    Then the command fails
    And the server answered 409 to PUT "/artifactory/api/go/go-local/example.com/acme/greet/@v/v1.0.0.zip"

  Scenario: The checksum database answers 404 to everyone
    When I GET "/artifactory/api/go/go-local/sumdb/sum.golang.org/supported"
    Then the response status is 404
    When I GET "/artifactory/api/go/go-local/sumdb/sum.golang.org/supported" as admin
    Then the response status is 404
    When I GET "/artifactory/api/go/go-local/sumdb/sum.golang.org/lookup/example.com/acme/greet@v1.0.0" as admin
    Then the response status is 404
    When I GET "/artifactory/api/go/go-local/sumdb/sum.golang.org/latest"
    Then the response status is 404

  @go @jf
  Scenario: A reader downloads with the go command but cannot publish
    Given the Go toolchain is available
    And the JFrog CLI is available
    When I PUT "/artifactory/api/security/users/reader" as admin with body:
      """
      {"name": "reader", "email": "reader@example.com", "password": "Reader-pass1"}
      """
    Then the response status is 201
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name": "go-read", "resources": {"artifact": {
        "actions": {"users": {"reader": ["READ"]}},
        "targets": {"go-local": {"include_patterns": ["**"], "exclude_patterns": []}}}}}
      """
    Then the response status is 201
    Given a file "greet/go.mod" with content:
      """
      module example.com/acme/greet

      go 1.22
      """
    And a file "greet/greet.go" with content:
      """
      // Package greet is the module the scenario publishes.
      package greet
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    And I run jf "c add rd --url {platform}/ --artifactory-url {url}/ --user reader --password Reader-pass1 --interactive=false"
    And I run jf "go-config --server-id-resolve aw --server-id-deploy aw --repo-resolve go-local --repo-deploy go-local" in "greet"
    And I run jf "gp v1.0.0" in "greet"
    Then the command succeeds
    Given the registry is fronted by TLS
    And the Go proxy is "https://reader:Reader-pass1@{tlshost}/artifactory/api/go/go-local"
    When I run go "mod download -json example.com/acme/greet@v1.0.0"
    Then the command succeeds
    And the output contains "GoModSum"
    When I run jf "go-config --server-id-resolve rd --server-id-deploy rd --repo-resolve go-local --repo-deploy go-local" in "greet"
    And I run jf "gp v1.1.0" in "greet"
    Then the command fails
    And the server answered 403 to PUT "/artifactory/api/go/go-local/example.com/acme/greet/@v/v1.1.0.zip"
    When I GET "/artifactory/api/go/go-local/example.com/acme/greet/@v/list" as admin
    Then the response body contains "v1.0.0 20"
    And the response body does not contain "v1.1.0"
