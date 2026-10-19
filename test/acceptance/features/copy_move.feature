Feature: Copy, move and archives
  Copy and move between paths and repositories, archives exploded on deploy,
  and entries read from inside a stored archive (research 02), as plain HTTP
  clients and the JFrog CLI drive them (research 09).

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/src-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/api/repositories/dst-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """

  Scenario: Copy and move over HTTP, with a dry run
    When I PUT "/artifactory/src-local/d/a.txt" as admin with body:
      """
      alpha
      """
    And I POST "/artifactory/api/copy/src-local/d?to=/dst-local/copied" as admin
    Then the response status is 200
    And the response body contains "1 artifacts and 1 folders were copied"
    When I GET "/artifactory/dst-local/copied/a.txt" as admin
    Then the response body is "alpha"
    When I POST "/artifactory/api/move/src-local/d/a.txt?to=/dst-local/moved/a.txt&dry=1" as admin
    Then the response status is 200
    And the response body contains "Dry run for moving"
    When I GET "/artifactory/src-local/d/a.txt" as admin
    Then the response status is 200
    When I POST "/artifactory/api/move/src-local/d/a.txt?to=/dst-local/moved/a.txt" as admin
    Then the response status is 200
    When I GET "/artifactory/src-local/d/a.txt" as admin
    Then the response status is 404
    When I GET "/artifactory/dst-local/moved/a.txt" as admin
    Then the response body is "alpha"
    # A missing source is a bad request with a report, as JFrog's copy and
    # move pages say (#400).
    When I POST "/artifactory/api/copy/src-local/missing?to=/dst-local/x" as admin
    Then the response status is 400
    And the response header "Content-Type" contains "CopyOrMoveResult"
    And the response body contains "The source src-local:missing does not exist"

  @jf
  Scenario: jf rt cp and jf rt mv, with a dry run
    Given the JFrog CLI is available
    And a file "one.txt" with content:
      """
      one
      """
    When I run jf "rt u {dir}/one.txt src-local/jf/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt cp src-local/jf/one.txt dst-local/cp/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 200 to POST "/artifactory/api/copy/src-local/jf/one.txt"
    When I GET "/artifactory/dst-local/cp/one.txt" as admin
    Then the response body is "one"
    When I run jf "rt mv src-local/jf/one.txt dst-local/mv/ --flat --dry-run --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 200 to POST "/artifactory/api/move/src-local/jf/one.txt"
    When I GET "/artifactory/src-local/jf/one.txt" as admin
    Then the response status is 200
    When I run jf "rt mv src-local/jf/one.txt dst-local/mv/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/src-local/jf/one.txt" as admin
    Then the response status is 404
    When I GET "/artifactory/dst-local/mv/one.txt" as admin
    Then the response body is "one"

  @jf
  Scenario: jf rt u --explode, and an entry read from a stored archive
    Given the JFrog CLI is available
    And a zip file "bundle.zip" holding "inner/readme.txt" with content "packed"
    When I run jf "rt u {dir}/bundle.zip src-local/ex/ --flat --explode --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/src-local/ex/inner/readme.txt" as admin
    Then the response body is "packed"
    When I GET "/artifactory/src-local/ex/bundle.zip" as admin
    Then the response status is 404
    When I run jf "rt u {dir}/bundle.zip src-local/kept/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/src-local/kept/bundle.zip!/inner/readme.txt" as admin
    Then the response body is "packed"
