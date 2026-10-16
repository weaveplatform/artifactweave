@builds
Feature: Build-info
  The Builds API (decision 0014) as the JFrog CLI drives it: an upload that
  records a build, jf rt bp, and jf rt bp --overwrite, which deletes each run
  of the number before publishing it again (research 22 §3.3); jf rt ba,
  which appends a published build to the next; then jf rt bpr, which
  promotes a build, and jf rt bdi, which discards old runs.

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """

  Scenario: Publish, read, append, rename and delete over HTTP
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name": "team/app", "number": "1", "started": "2026-10-15T09:30:00.000+0000",
       "vcs": [{"revision": "abc"}], "modules": [{"id": "m1", "type": "generic"}]}
      """
    Then the response status is 200
    When I GET "/artifactory/api/build/team%2Fapp/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.vcs.0.revision" equals "abc"
    When I GET "/artifactory/api/storage/artifactory-build-info/team%252Fapp/1-1792056600000.json" as admin
    Then the response status is 200
    When I POST "/artifactory/api/build/append/team%2Fapp/1" as admin with body:
      """
      [{"id": "m2", "type": "generic"}]
      """
    Then the response status is 204
    When I GET "/artifactory/api/build/team%2Fapp/1" as admin
    Then the JSON field "buildInfo.modules" has 2 entries
    When I POST "/artifactory/api/build/rename/team%2Fapp?to=team%2Fservice" as admin
    Then the response status is 200
    When I GET "/artifactory/api/build" as admin
    Then the JSON field "builds.0.uri" equals "/team/service"
    When I DELETE "/artifactory/api/build/team%2Fservice?deleteAll=1" as admin
    Then the response status is 200
    When I GET "/artifactory/api/build/team%2Fservice" as admin
    Then the response status is 404
    When I GET "/artifactory/api/storage/artifactory-build-info/team%252Fservice" as admin
    Then the response status is 404

  @jf
  Scenario: jf rt u with a build, jf rt bp, and jf rt bp --overwrite
    Given the JFrog CLI is available
    And a file "app.bin" with content:
      """
      build artifact
      """
    When I run jf "rt bc accept-app 7"
    Then the command succeeds
    When I run jf "rt u {dir}/app.bin generic-local/app/ --flat --build-name=accept-app --build-number=7 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp accept-app 7 --detailed-summary --url {url} --user admin --password {password}"
    Then the command succeeds
    # The summary's sha256 is the X-Checksum-Sha256 the publish answered.
    And I keep the JSON field "sha256" of the output as "published"
    And the server answered 200 to PUT "/artifactory/api/build"
    When I GET "/artifactory/api/build/accept-app/7" as admin
    Then the response status is 200
    And the JSON field "buildInfo.name" equals "accept-app"
    And the JSON field "buildInfo.modules.0.artifacts.0.name" equals "app.bin"
    When I GET "/artifactory/api/builds/buildArtifacts/accept-app/7/artifactory-build-info" as admin
    Then the response status is 200
    And the JSON field "0.repo" equals "generic-local"
    And the JSON field "0.path" equals "app"
    # A second publish of the number is a second run.
    When I run jf "rt u {dir}/app.bin generic-local/app/ --flat --build-name=accept-app --build-number=7 --url {url} --user admin --password {password}"
    And I run jf "rt bp accept-app 7 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/build/accept-app" as admin
    Then the JSON field "buildsNumbers" has 2 entries
    # --overwrite deletes both runs of 7, then publishes one.
    When I run jf "rt u {dir}/app.bin generic-local/app/ --flat --build-name=accept-app --build-number=7 --url {url} --user admin --password {password}"
    And I run jf "rt bp accept-app 7 --overwrite --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 200 to POST "/artifactory/api/build/delete"
    When I GET "/artifactory/api/build/accept-app" as admin
    Then the JSON field "buildsNumbers" has 1 entries
    And the JSON field "buildsNumbers.0.uri" equals "/7"

  # With a pattern, the CLI finds a build's files through AQL on
  # artifact.module.build.name and .number (decision 0012), then keeps those
  # whose build.name and build.number properties match.
  @jf
  Scenario: jf rt s --build and jf rt dl --build find what a build produced
    Given the JFrog CLI is available
    And a file "app.bin" with content:
      """
      build artifact
      """
    And a file "other.bin" with content:
      """
      not in the build
      """
    When I run jf "rt u {dir}/app.bin generic-local/app/ --flat --build-name=search-app --build-number=3 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u {dir}/other.bin generic-local/app/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp search-app 3 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt s generic-local/ --build=search-app/3 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "generic-local/app/app.bin"
    And the output does not contain "other.bin"
    And the server answered 200 to POST "/artifactory/api/search/aql"
    When I run jf "rt dl generic-local/app/ {dir}/out/ --flat --build=search-app/3 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "app.bin" and "out/app.bin" are identical
    And the file "out/other.bin" does not exist

  # jf rt ba reads the appended run's started time and the checksums of its
  # stored build-info document, then records a module of type "build" that
  # names it; jf rt bp publishes that module like any other.
  @jf
  Scenario: jf rt ba appends a published build to the next one
    Given the JFrog CLI is available
    And a file "lib.bin" with content:
      """
      library artifact
      """
    When I run jf "rt u {dir}/lib.bin generic-local/lib/ --flat --build-name=lib --build-number=4 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp lib 4 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt ba bundle 1 lib 4 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp bundle 1 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/build/bundle/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.type" equals "build"
    And the JSON field "buildInfo.modules.0.id" equals "lib/4"

  # jf rt bpr sends the promotion media type and needs 200; jf rt bdi sends
  # count as a string and needs 204 (research 22 §3.1).
  @jf
  Scenario: jf rt bpr promotes a build and jf rt bdi discards its old runs
    Given the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/release-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/api/repositories/archive-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And a file "app.bin" with content:
      """
      promoted artifact
      """
    When I run jf "rt u {dir}/app.bin generic-local/app/ --flat --build-name=promo-app --build-number=1 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp promo-app 1 --url {url} --user admin --password {password}"
    Then the command succeeds
    # A dry run changes nothing.
    When I run jf "rt bpr promo-app 1 release-local --dry-run --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/storage/release-local/app/app.bin" as admin
    Then the response status is 404
    When I run jf "rt bpr promo-app 1 release-local --status=released --comment=approved --copy --props=stage=prod --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 200 to POST "/artifactory/api/build/promote/promo-app/1"
    When I GET "/artifactory/api/storage/release-local/app/app.bin?properties" as admin
    Then the response status is 200
    And the JSON field "properties.stage.0" equals "prod"
    When I GET "/artifactory/api/storage/generic-local/app/app.bin" as admin
    Then the response status is 200
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      build.promotions.find().include("status","comment","repo")
      """
    Then the response status is 200
    And the response body contains "approved"
    And the response body contains "release-local"
    # A move from the one source repository named.
    When I run jf "rt bpr promo-app 1 archive-local --source-repo=release-local --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/storage/release-local/app/app.bin" as admin
    Then the response status is 404
    When I GET "/artifactory/api/storage/archive-local/app/app.bin" as admin
    Then the response status is 200
    When I run jf "rt bpr promo-app 9 release-local --url {url} --user admin --password {password}"
    Then the command fails
    # Two more runs, then keep only the newest.
    When I run jf "rt u {dir}/app.bin generic-local/app2/ --flat --build-name=promo-app --build-number=2 --url {url} --user admin --password {password}"
    And I run jf "rt bp promo-app 2 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u {dir}/app.bin generic-local/app3/ --flat --build-name=promo-app --build-number=3 --url {url} --user admin --password {password}"
    And I run jf "rt bp promo-app 3 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bdi promo-app --max-builds=1 --exclude-builds=1 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 204 to POST "/artifactory/api/build/retention/promo-app"
    When I GET "/artifactory/api/build/promo-app" as admin
    Then the JSON field "buildsNumbers" has 2 entries
    And the JSON field "buildsNumbers.0.uri" equals "/3"
    And the JSON field "buildsNumbers.1.uri" equals "/1"

  Scenario: Retention over HTTP, and a fail-fast promotion that changes nothing
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name": "ret-app", "number": "1", "started": "2026-10-15T09:30:00.000+0000",
       "modules": [{"id": "m", "artifacts": [{"name": "gone.bin", "sha1": "0123456789abcdef0123456789abcdef01234567"}]}]}
      """
    Then the response status is 200
    When I POST "/artifactory/api/build/promote/ret-app/1" as admin with body:
      """
      {"status": "released", "targetRepo": "generic-local", "failFast": true}
      """
    Then the response status is 400
    And the JSON field "messages.0.level" equals "ERROR"
    And the JSON field "messages.0.message" equals "Unable to find artifact 'gone.bin' of build 'ret-app' #1"
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name": "ret-app", "number": "2", "started": "2026-10-16T09:30:00.000+0000",
       "buildRetention": {"count": 1}}
      """
    Then the response status is 200
    When I GET "/artifactory/api/build/ret-app" as admin
    Then the JSON field "buildsNumbers" has 1 entries
    When I POST "/artifactory/api/build/retention/ret-app?async=false" as admin with body:
      """
      {"count": 0}
      """
    Then the response status is 400
