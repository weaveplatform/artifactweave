Feature: AQL actions
  items.delete and properties.update over POST /api/search/aql (research 22
  §1.5). They answer in the find shape, listing what they change, and a dry
  run, which is also what an action without .dryRun() does, changes nothing
  (decision 0012). The JFrog CLI never sends them, so plain HTTP drives them.

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/aql-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/aql-local/tmp/a.tmp" as admin with body:
      """
      scratch
      """
    And I PUT "/artifactory/aql-local/lib/b.jar" as admin with body:
      """
      keep
      """

  Scenario: items.delete lists what it deletes, and its dry run deletes nothing
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.delete({"repo":"aql-local","name":{"$match":"*.tmp"}}).dryRun("true")
      """
    Then the response status is 200
    And the JSON field "results.0.path" equals "tmp"
    And the JSON field "results.0.name" equals "a.tmp"
    And the JSON field "range.total" equals "1"
    When I GET "/artifactory/aql-local/tmp/a.tmp" as admin
    Then the response status is 200
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.delete({"repo":"aql-local","name":{"$match":"*.tmp"}}).dryRun("false")
      """
    Then the response status is 200
    And the JSON field "results.0.name" equals "a.tmp"
    And the JSON field "range.total" equals "1"
    When I GET "/artifactory/aql-local/tmp/a.tmp" as admin
    Then the response status is 404
    When I GET "/artifactory/api/storage/auto-trashcan/aql-local/tmp/a.tmp" as admin
    Then the response status is 200
    When I GET "/artifactory/aql-local/lib/b.jar" as admin
    Then the response body is "keep"

  Scenario: properties.update replaces the values of the keys an item has
    When I PUT "/artifactory/api/storage/aql-local/lib/b.jar?properties=status=draft;owner=core" as admin
    Then the response status is 204
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      properties.update({"item.repo":"aql-local","item.name":{"$match":"*.jar"}}).keys("status","absent").newValue("approved")
      """
    Then the response status is 200
    And the JSON field "results.0.name" equals "b.jar"
    And the JSON field "results.0.properties.0.key" equals "status"
    And the JSON field "results.0.properties.0.value" equals "approved"
    And the JSON field "range.total" equals "1"
    When I GET "/artifactory/api/storage/aql-local/lib/b.jar?properties" as admin
    Then the JSON field "properties.status" equals "[draft]"
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      properties.update({"item.repo":"aql-local","item.name":{"$match":"*.jar"}}).keys("status","absent").newValue("approved").dryRun("false")
      """
    Then the response status is 200
    And the JSON field "results.0.properties.0.value" equals "approved"
    When I GET "/artifactory/api/storage/aql-local/lib/b.jar?properties" as admin
    Then the JSON field "properties.status" equals "[approved]"
    And the JSON field "properties.owner" equals "[core]"
    And the output does not contain "absent"

  Scenario: Actions refuse what JFrog does not support with them
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.delete({"repo":"aql-local"}).limit(1)
      """
    Then the response status is 400
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      properties.update({"item.repo":"aql-local"}).keys("status")
      """
    Then the response status is 400
    When I GET "/artifactory/aql-local/tmp/a.tmp" as admin
    Then the response status is 200
