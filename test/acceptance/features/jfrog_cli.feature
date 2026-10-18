@jf
Feature: JFrog CLI smoke
  The jf binary must work unchanged. These scenarios run only when
  ARTIFACTWEAVE_JF_BIN points at a jf binary; the CI acceptance job downloads
  a pinned release.

  Background:
    Given the registry is running
    And the JFrog CLI is available

  Scenario: jf rt ping
    When I run jf "rt ping --url {url}"
    Then the command succeeds

  Scenario: jf config add with a dummy token does not contact the server
    When I run jf "config add acceptance --url {url} --access-token dummy --interactive=false"
    Then the command succeeds

  # Without --sort-by or --limit, jf keeps "property" in the include, so
  # --offset sends .offset() beside it (jfrog-client-go
  # includePropertiesInAqlForSpec). The offset counts items, not property
  # rows (decision 0012): the jar, uploaded first with two properties, is the
  # one item skipped.
  Scenario: jf rt s and jf rt dl with --offset skip items beside the property include
    Given I PUT "/artifactory/api/repositories/libs-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And a file "app-1.0.jar" with content:
      """
      the application
      """
    And a file "app-1.0.pom" with content:
      """
      <project/>
      """
    And a file "readme.md" with content:
      """
      read me
      """
    When I run jf "rt u app-1.0.jar libs-local/org/ --flat --target-props build.name=app;release=1.0 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u app-1.0.pom libs-local/org/ --flat --target-props build.name=app --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u readme.md libs-local/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds

    When I run jf "rt s libs-local/ --offset 1 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "libs-local/org/app-1.0.pom"
    And the output contains "libs-local/readme.md"
    And the output does not contain "app-1.0.jar"
    And the server answered 200 to POST "/artifactory/api/search/aql"
    And the server never answered 400 to POST "/artifactory/api/search/aql"

    When I run jf "rt dl libs-local/ out/ --offset 1 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "app-1.0.pom" and "out/org/app-1.0.pom" are identical
    And the files "readme.md" and "out/readme.md" are identical
    And the file "out/org/app-1.0.jar" does not exist

    # A sort and a limit beside the include apply to the items too.
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"libs-local"}).include("name","property").sort({"$desc":["name"]}).offset(1).limit(1)
      """
    Then the response status is 200
    And the JSON field "results" has 1 entries
    And the JSON field "results.0.name" equals "app-1.0.pom"
    And the JSON field "results.0.properties.0.key" equals "build.name"
    And the JSON field "range.start_pos" equals "1"
    And the JSON field "range.limit" equals "1"
