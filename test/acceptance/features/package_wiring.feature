@package_wiring
Feature: The item tree is the only source of truth for local packages
  Each format keeps its package index and metadata from the item tree
  (decisions 0018 and 0032), so the generic item operations drive what npm,
  Go, Maven, PyPI, NuGet, Helm and Terraform clients read: a deploy by REST is served, a copy is served
  from its target, a delete hides it and a restore from the trash can brings
  it back. A reindex over an index the hooks kept changes nothing; the
  formattest contract rebuilds each format's index from an empty one. Part
  of the P7 and P8 exit checks (#295, #560). Content operations use REST;
  the P8 scenarios also observe the local queue to await reindex completion.

  Background:
    Given the registry is running

  Scenario: Deploy, copy, delete and restore drive P7 reads, and a reindex keeps them
    Given I PUT "/artifactory/api/repositories/npm-a" as admin with body:
      """
      {"rclass": "local", "packageType": "npm"}
      """
    And I PUT "/artifactory/api/repositories/npm-b" as admin with body:
      """
      {"rclass": "local", "packageType": "npm"}
      """
    And I PUT "/artifactory/api/repositories/go-a" as admin with body:
      """
      {"rclass": "local", "packageType": "go"}
      """
    And I PUT "/artifactory/api/repositories/go-b" as admin with body:
      """
      {"rclass": "local", "packageType": "go"}
      """
    And I PUT "/artifactory/api/repositories/mvn-a" as admin with body:
      """
      {"rclass": "local", "packageType": "maven"}
      """
    And I PUT "/artifactory/api/repositories/mvn-b" as admin with body:
      """
      {"rclass": "local", "packageType": "maven"}
      """
    Then the response status is 200

    # Deployed by REST, each as its own client would find it.
    Given an npm package "acme-wire" at "1.0.0" with its tarball at "/artifactory/api/npm/npm-a/acme-wire/-/acme-wire-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-wire-1.0.0.tgz" to "/artifactory/npm-a/acme-wire/-/acme-wire-1.0.0.tgz" as admin
    Then the response status is 201
    Given a Go module "example.com/acme/wire" at "v1.0.0" laid out as a module proxy under "goproxy"
    When I deploy the files under "goproxy" to "/artifactory/go-a" as admin
    And I PUT "/artifactory/mvn-a/org/acme/wire/lib/1.0/lib-1.0.pom" as admin with body:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>org.acme.wire</groupId><artifactId>lib</artifactId><version>1.0</version></project>
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-a/org/acme/wire/lib/1.0/lib-1.0.jar" as admin with body:
      """
      the wired library
      """
    Then the response status is 201
    When I GET "/artifactory/api/npm/npm-a/acme-wire" as admin
    Then the response status is 200
    And the response body contains "/artifactory/api/npm/npm-a/acme-wire/-/acme-wire-1.0.0.tgz"
    When I GET "/artifactory/api/go/go-a/example.com/acme/wire/@v/list" as admin
    Then the response status is 200
    And the response body contains "v1.0.0"
    And within 20 seconds the body of GET "/artifactory/mvn-a/org/acme/wire/lib/maven-metadata.xml" as admin contains "<release>1.0</release>"

    # A copy is indexed in its target as a deploy there would be.
    When I POST "/artifactory/api/copy/npm-a/acme-wire?to=/npm-b/acme-wire" as admin
    Then the response status is 200
    When I POST "/artifactory/api/copy/go-a/example.com?to=/go-b/example.com" as admin
    Then the response status is 200
    When I POST "/artifactory/api/copy/mvn-a/org?to=/mvn-b/org" as admin
    Then the response status is 200
    When I GET "/artifactory/api/npm/npm-b/acme-wire" as admin
    Then the response status is 200
    And the response body contains "/artifactory/api/npm/npm-b/acme-wire/-/acme-wire-1.0.0.tgz"
    When I GET "/artifactory/api/go/go-b/example.com/acme/wire/@v/v1.0.0.info" as admin
    Then the response status is 200
    And the JSON field "Version" equals "v1.0.0"
    And within 20 seconds the body of GET "/artifactory/mvn-b/org/acme/wire/lib/maven-metadata.xml" as admin contains "<release>1.0</release>"

    # A delete takes the version out of every read in its repository alone.
    When I DELETE "/artifactory/npm-a/acme-wire" as admin
    Then the response status is 204
    When I DELETE "/artifactory/go-a/example.com/acme/wire/@v/v1.0.0.zip" as admin
    Then the response status is 204
    When I DELETE "/artifactory/mvn-a/org/acme/wire/lib/1.0" as admin
    Then the response status is 204
    When I GET "/artifactory/api/npm/npm-a/acme-wire" as admin
    Then the response status is 404
    When I GET "/artifactory/api/go/go-a/example.com/acme/wire/@v/v1.0.0.info" as admin
    Then the response status is 404
    And within 20 seconds the body of GET "/artifactory/mvn-a/org/acme/wire/lib/maven-metadata.xml" as admin does not contain "<version>1.0</version>"
    When I GET "/artifactory/api/npm/npm-b/acme-wire" as admin
    Then the response status is 200

    # Restored from the trash can, each is read again.
    When I POST "/artifactory/api/trash/restore/npm-a/acme-wire?to=npm-a" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/trash/restore/go-a/example.com/acme/wire/@v/v1.0.0.zip?to=go-a" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/trash/restore/mvn-a/org/acme/wire/lib/1.0?to=mvn-a" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I GET "/artifactory/api/npm/npm-a/acme-wire" as admin
    Then the response status is 200
    And the response body contains "/artifactory/api/npm/npm-a/acme-wire/-/acme-wire-1.0.0.tgz"
    When I GET "/artifactory/api/go/go-a/example.com/acme/wire/@v/v1.0.0.info" as admin
    Then the response status is 200
    And within 20 seconds the body of GET "/artifactory/mvn-a/org/acme/wire/lib/maven-metadata.xml" as admin contains "<release>1.0</release>"

    # The reindexes 7.x serves for these formats rebuild what the hooks
    # kept: npm's is queued, and Maven's is calculateMetadata.
    When I POST "/artifactory/api/npm/npm-a/reindex" as admin
    Then the response status is 200
    When I POST "/artifactory/api/maven/calculateMetadata/mvn-a/org" as admin
    Then the response status is 200
    When I GET "/artifactory/api/npm/npm-a/acme-wire" as admin until the response status is 200
    Then the response body contains "/artifactory/api/npm/npm-a/acme-wire/-/acme-wire-1.0.0.tgz"
    And the JSON field "dist-tags.latest" equals "1.0.0"
    When I GET "/artifactory/mvn-a/org/acme/wire/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<release>1.0</release>"

  @p8_wiring
  Scenario Outline: <format> reads follow REST deploy, copy, delete, trash restore and reindex
    Given a "<format>" package wiring fixture
    When I PUT "/artifactory/api/repositories/wire-a" as admin with body:
      """
      {"rclass":"local","packageType":"<type>","terraformType":"<terraform>"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/wire-b" as admin with body:
      """
      {"rclass":"local","packageType":"<type>","terraformType":"<terraform>"}
      """
    Then the response status is 200
    When I deploy the file "<file>" to "/artifactory/wire-a/{wiring-item}" as admin
    Then the response status is 201
    And the "<format>" reads in "wire-a" serve the wiring package

    When I POST "/artifactory/api/copy/wire-a/{wiring-item}?to=/wire-b/{wiring-item}" as admin
    Then the response status is 200
    And the "<format>" reads in "wire-b" serve the wiring package
    When I DELETE "/artifactory/wire-a/{wiring-item}" as admin
    Then the response status is 204
    And the "<format>" reads in "wire-a" hide the wiring package
    And the "<format>" reads in "wire-b" serve the wiring package

    When I POST "/artifactory/api/trash/restore/wire-a/{wiring-item}?to=wire-a" as admin with body:
      """
      {}
      """
    Then the response status is 202
    And the "<format>" reads in "wire-a" serve the wiring package

    When I POST "/artifactory/api/<type>/wire-a/reindex" as admin
    Then the response status is 200
    When I POST "/artifactory/api/<type>/wire-b/reindex" as admin
    Then the response status is 200
    And the queued reindex of "wire-a" completes within 20 seconds
    And the queued reindex of "wire-b" completes within 20 seconds
    And the "<format>" reads in "wire-a" serve the wiring package
    And the "<format>" reads in "wire-b" serve the wiring package

    Examples:
      | format             | type      | terraform | file                                       |
      | PyPI               | pypi      |           | dist/acme_wire-1.0.0-py3-none-any.whl       |
      | NuGet              | nuget     |           | dist/Acme.Wire.1.0.0.nupkg                  |
      | Helm               | helm      |           | dist/acme-wire-0.1.0.tgz                   |
      | Terraform module   | terraform | MODULE    | dist/module.zip                            |
      | Terraform provider | terraform | PROVIDER  | dist/provider.zip                          |
