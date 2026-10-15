Feature: GET searches
  The quick, property, checksum, bad checksum, usage, dates, creation,
  pattern, GAVC, versions and latest version searches build AQL internally
  and answer their documented shapes (decision 0012, research 22 §2); the
  dependency and build artifacts searches read build-info. The JFrog CLI's
  skill search uses the property search.

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/generic-local/org/app-1.0.jar;team=core" as admin with body:
      """
      app
      """
    And I PUT "/artifactory/generic-local/org/docs/readme.txt" as admin with body:
      """
      read me
      """

  Scenario: Quick, property, checksum and bad checksum search
    When I GET "/artifactory/api/search/artifact?name=app" as admin
    Then the response status is 200
    And the response body contains "/artifactory/api/storage/generic-local/org/app-1.0.jar"
    And the output does not contain "readme"
    When the next request has header "X-Result-Detail" "info, properties"
    And I GET "/artifactory/api/search/artifact?name=app&repos=generic-local" as admin
    Then the JSON field "results.0.repo" equals "generic-local"
    And the JSON field "results.0.path" equals "/org/app-1.0.jar"
    And the JSON field "results.0.properties.team.0" equals "core"
    When I GET "/artifactory/api/search/prop?team=core,edge" as admin
    Then the response status is 200
    And the response body contains "/generic-local/org/app-1.0.jar"
    When I GET "/artifactory/api/search/prop" as admin
    Then the response status is 400
    When I GET "/artifactory/api/search/checksum?sha1=7d1043473d55bfa90e8530d35801d4e381bc69f0" as admin
    Then the response status is 200
    And the response body contains "/generic-local/org/app-1.0.jar"
    When the next request has header "Accept" "application/vnd.org.jfrog.artifactory.search.BadChecksumSearchResult+json"
    And I GET "/artifactory/api/search/badChecksum?type=md5&repos=generic-local" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "BadChecksumSearchResult"
    And the JSON field "results.0.serverMd5" equals "d2a57dc1d883fd21fb9951699df71cc7"
    And the JSON field "results.0.clientMd5" equals ""
    When I GET "/artifactory/api/search/artifact?name=app"
    Then the response status is 401

  Scenario: Creation, dates, usage and pattern search need a named user
    When I GET "/artifactory/api/search/creation?from=0&repos=generic-local" as admin
    Then the response status is 200
    And the JSON field "results.0.created" is not empty
    When I GET "/artifactory/api/search/creation?from=0&repos=absent-local" as admin
    Then the response status is 404
    When I GET "/artifactory/api/search/dates?from=2000-01-01T00:00:00Z&dateFields=created,lastModified" as admin
    Then the response status is 200
    And the JSON field "results.1.lastModified" is not empty
    When I GET "/artifactory/api/search/usage?notUsedSince=4102444800000" as admin
    Then the response status is 200
    And the response body contains "/generic-local/org/docs/readme.txt"
    When I GET "/artifactory/api/search/pattern?pattern=generic-local:org/*/*.txt" as admin
    Then the response status is 200
    And the JSON field "files.0" equals "org/docs/readme.txt"
    And the JSON field "sourcePattern" equals "generic-local:org/*/*.txt"
    When I GET "/artifactory/api/search/pattern?pattern=generic-local:org/**" as admin
    Then the response status is 400
    When I GET "/artifactory/api/search/usage?notUsedSince=0"
    Then the response status is 401

  Scenario: GAVC, versions and latest version read Maven coordinates through the layout
    Given I PUT "/artifactory/api/repositories/maven-local" as admin with body:
      """
      {"rclass": "local", "packageType": "maven"}
      """
    And I PUT "/artifactory/maven-local/org/acme/lib/1.0/lib-1.0.jar;version=1.0" as admin with body:
      """
      one
      """
    And I PUT "/artifactory/maven-local/org/acme/lib/1.1/lib-1.1.jar;version=1.1" as admin with body:
      """
      one point one
      """
    And I PUT "/artifactory/maven-local/org/acme/lib/1.1/lib-1.1-sources.jar" as admin with body:
      """
      sources
      """
    And I PUT "/artifactory/maven-local/org/acme/lib/2.0-SNAPSHOT/lib-2.0-20260101.120000-1.jar" as admin with body:
      """
      snapshot
      """
    When I GET "/artifactory/api/search/gavc?g=org.acme&a=lib&v=1.1&c=sources" as admin
    Then the response status is 200
    And the JSON field "results" has 1 entries
    And the response body contains "/artifactory/api/storage/maven-local/org/acme/lib/1.1/lib-1.1-sources.jar"
    When I GET "/artifactory/api/search/gavc?g=org.acme&a=lib&v=1.1&specific=true&repos=maven-local" as admin
    Then the response status is 200
    And the JSON field "results.0.ext" equals "jar"
    And the JSON field "results.0.version" equals "1.1"
    And the JSON field "results.1.classifier" equals "sources"
    And the response body contains "/artifactory/maven-local/org/acme/lib/1.1/lib-1.1.jar"
    When I GET "/artifactory/api/search/versions?g=org.acme&a=lib" as admin
    Then the response status is 200
    And the JSON field "results" has 3 entries
    And the JSON field "results.0.version" equals "2.0-SNAPSHOT"
    And the JSON field "results.0.integration" equals "true"
    When I GET "/artifactory/api/search/latestVersion?g=org.acme&a=lib" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "text/plain"
    And the response body is "1.1"
    When I GET "/artifactory/api/search/latestVersion?g=org.acme&a=lib&v=2.0-SNAPSHOT" as admin
    Then the response body is "2.0-20260101.120000-1"
    When I GET "/artifactory/api/search/latestVersion?g=org.acme&a=none" as admin
    Then the response status is 404
    When I GET "/artifactory/api/versions/maven-local/org/acme?listFiles=1" as admin
    Then the response status is 200
    And the JSON field "version" equals "1.1"
    And the JSON field "artifacts" has 1 entries
    And the response body contains "/artifactory/maven-local/org/acme/lib/1.1/lib-1.1.jar"
    When I GET "/artifactory/api/versions/_any/_any"
    Then the response status is 401

  Scenario: Dependency and build artifacts searches read build-info
    Given I PUT "/artifactory/generic-local/dist/a.bin;build.name=app;build.number=7" as admin with body:
      """
      artifact a
      """
    And I PUT "/artifactory/api/build" as admin with body:
      """
      {"name": "app", "number": "7", "started": "2026-10-15T09:30:00.000+0000",
       "modules": [{"id": "m",
         "artifacts": [{"name": "a.bin", "sha1": "78d9aabd9545cc42a965eccb43ea43215a78efd3"}],
         "dependencies": [{"id": "org:app:1.0", "sha1": "7d1043473d55bfa90e8530d35801d4e381bc69f0"}]}]}
      """
    When I GET "/artifactory/api/search/dependency?sha1=7d1043473d55bfa90e8530d35801d4e381bc69f0" as admin
    Then the response status is 200
    And the JSON field "results" has 1 entries
    And the response body contains "/artifactory/api/build/app/7"
    When I POST "/artifactory/api/search/buildArtifacts" as admin with body:
      """
      {"buildName": "app", "buildNumber": "LATEST", "repos": ["generic-local"],
       "mappings": [{"input": "dist/.+"}]}
      """
    Then the response status is 200
    And the JSON field "results" has 1 entries
    And the response body contains "/artifactory/generic-local/dist/a.bin"
    When I POST "/artifactory/api/search/buildArtifacts" as admin with body:
      """
      {"buildName": "app", "buildNumber": "8"}
      """
    Then the response status is 404

  @jf
  Scenario: jf skills search --prop finds a skill through the property search
    Given the JFrog CLI is available
    When I PUT "/artifactory/generic-local/my-skill/1.0.0/my-skill-1.0.0.zip;skill.name=my-skill;skill.version=1.0.0" as admin with body:
      """
      zip
      """
    And I run jf "skills search my-skill --prop --repo generic-local --format json --url {platform} --user admin --password {password}"
    Then the command succeeds
    And the output contains "my-skill"
    And the output contains "1.0.0"
    And the output does not contain "app-1.0"
    And the server answered 200 to GET "/artifactory/api/search/prop"
