Feature: The npm, Go and Maven clients reach artifactweave
  The real clients P7's exit criteria use (decision 0015), proven against
  what artifactweave serves before P7's format code: generic repositories
  laid out the way each client reads them, and Maven deploys through item
  paths. npm and Maven run in containers pinned by digest
  (test/acceptance/clients); Go is the runner's toolchain.

  Background:
    Given the registry is running

  @go
  Scenario: go mod download from a generic repository laid out as a module proxy
    Given the Go toolchain is available
    When I PUT "/artifactory/api/repositories/go-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given a Go module "example.com/acme/lib" at "v1.0.0" laid out as a module proxy under "goproxy"
    When I deploy the files under "goproxy" to "/artifactory/go-generic" as admin
    And the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/go-generic"
    And I run go "mod download -json example.com/acme/lib@v1.0.0"
    Then the command succeeds
    And the output contains "v1.0.0.zip"
    And the output contains "GoModSum"

  @npm
  Scenario: npm installs a package from a static packument in a generic repository
    Given the npm client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given an npm package "acme-smoke" at "1.0.0" with its tarball at "/artifactory/npm-generic/tarballs/acme-smoke-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-smoke.json" to "/artifactory/npm-generic/acme-smoke" as admin
    Then the response status is 201
    When I deploy the file "npm/acme-smoke-1.0.0.tgz" to "/artifactory/npm-generic/tarballs/acme-smoke-1.0.0.tgz" as admin
    Then the response status is 201
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/npm-generic/
      //{host}/artifactory/npm-generic/:_auth={basicauth}
      """
    When I run npm "install acme-smoke@1.0.0"
    Then the command succeeds
    And the file "node_modules/acme-smoke/package.json" contains "1.0.0"
    And the file "package-lock.json" contains "sha512-"

  @maven
  Scenario: mvn deploys a release through item paths and a fresh build resolves it
    Given the Maven client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-local" as admin with body:
      """
      {"rclass":"local","packageType":"maven"}
      """
    Then the response status is 200
    Given the Maven project "lib" in "lib"
    And a file "settings.xml" with expanded content:
      """
      <settings>
        <servers>
          <server>
            <id>aw</id>
            <username>admin</username>
            <password>{password}</password>
          </server>
        </servers>
      </settings>
      """
    # package then deploy:deploy, not install: the job's cached local
    # repository must hold only plugins, never this artifact.
    When I run mvn "-f lib/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-local"
    Then the command succeeds
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<release>1.0.0</release>"
    # Under client-checksums a checksum file serves what the client sent:
    # mvn sends a SHA-1 and an MD5 after each file. The metadata's are the
    # server's own.
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar.sha1" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.pom.md5" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/maven-metadata.xml.sha1" as admin
    Then the response status is 200
    # A fresh local repository, chained to the cache for plugins only, so the
    # artifact can come from nowhere but artifactweave.
    When I run mvn "-s settings.xml -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2} org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact=org.acme.smoke:lib:1.0.0 -DremoteRepositories=aw::default::{url}/mvn-local"
    Then the command succeeds
    And the file "fresh/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" exists
