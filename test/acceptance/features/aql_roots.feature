Feature: Repository roots in AQL
  Every repository that stores items has a root, a folder whose path and
  name are ".", and items.find returns it as Artifactory 7.x does (#445).
  The JFrog CLI skips a result named "." when it reduces folders and when
  it deletes (jfrog-client-go artifactory/services/utils/searchutil.go and
  deleteutils.go), so its file searches, downloads and deletes work as
  before; a search that includes folders lists the root.

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/roots-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """

  Scenario: items.find returns a repository's root
    When I PUT "/artifactory/roots-local/lib/b.jar" as admin with body:
      """
      keep
      """
    And I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"roots-local","type":"folder","path":"."}).include("repo","path","name","type")
      """
    Then the response status is 200
    And the JSON field "results.0.repo" equals "roots-local"
    And the JSON field "results.0.path" equals "."
    And the JSON field "results.0.name" equals "."
    And the JSON field "results.0.type" equals "folder"
    And the JSON field "results.1.name" equals "lib"
    And the JSON field "range.total" equals "2"

  @jf
  Scenario: jf rt s finds the files, and jf rt dl and jf rt del pass the root over
    Given the JFrog CLI is available
    And a file "lib/b.jar" with content:
      """
      keep
      """
    When I run jf "rt u lib/b.jar roots-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt s roots-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "Found 1 artifact."
    And the output contains "roots-local/lib/b.jar"
    # With folders the search lists the root as "roots-local/", as it would
    # against 7.x: the client skips "." only when it reduces folders.
    When I run jf "rt s roots-local/ --include-dirs --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "Found 3 artifacts."
    When I run jf "rt dl roots-local/ out/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "lib/b.jar" and "out/lib/b.jar" are identical
    When I run jf "rt del roots-local/ --quiet --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/repositories/roots-local" as admin
    Then the response status is 200
    When I GET "/artifactory/roots-local/lib/b.jar" as admin
    Then the response status is 404
