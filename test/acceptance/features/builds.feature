@builds
Feature: Build-info
  The Builds API (decision 0014) as the JFrog CLI drives it: an upload that
  records a build, jf rt bp, and jf rt bp --overwrite, which deletes each run
  of the number before publishing it again (research 22 §3.3).

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
