Feature: GET searches
  The quick, property, checksum, bad checksum, usage, dates, creation and
  pattern searches build AQL internally and answer their documented shapes
  (decision 0012, research 22 §2). The JFrog CLI's skill search uses the
  property search.

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
