Feature: Downloads that ask for the latest version
  A GET of a version token or a -SNAPSHOT name serves the file Artifactory
  would: under the Maven layout the highest release in the Mercury order or
  the newest unique snapshot build, and under other layouts the
  alphabetically last release or integration revision (decision 0036, #281).

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/mvn-local" as admin with body:
      """
      {"rclass": "local", "packageType": "maven"}
      """
    And I PUT "/artifactory/mvn-local/com/acme/app/1.9/app-1.9.jar;tier=gold" as admin with body:
      """
      jar 1.9
      """
    And I PUT "/artifactory/mvn-local/com/acme/app/1.10/app-1.10.jar;tier=silver" as admin with body:
      """
      jar 1.10
      """
    And I PUT "/artifactory/mvn-local/com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar" as admin with body:
      """
      build 1
      """
    And I PUT "/artifactory/mvn-local/com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar" as admin with body:
      """
      build 2
      """

  Scenario: [RELEASE] and a -SNAPSHOT name resolve in a Maven repository
    When I GET "/artifactory/mvn-local/com/acme/app/[RELEASE]/app-[RELEASE].jar" as admin
    Then the response status is 200
    And the response body is "jar 1.10"
    When I GET "/artifactory/mvn-local/com/acme/app/[RELEASE]/app-[RELEASE].jar;tier=gold" as admin
    Then the response status is 200
    And the response body is "jar 1.9"
    When I GET "/artifactory/mvn-local/com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar" as admin
    Then the response status is 200
    And the response body is "build 2"
    When I HEAD "/artifactory/mvn-local/com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar" as admin
    Then the response status is 200
    And the response header "Content-Length" contains "7"
    When I GET "/artifactory/mvn-local/com/acme/app/[RELEASE]/app-[RELEASE].war" as admin
    Then the response status is 404

  Scenario: [INTEGRATION] resolves outside the Maven layout
    When I PUT "/artifactory/api/repositories/gradle-local" as admin with body:
      """
      {"rclass": "local", "packageType": "gradle", "repoLayoutRef": "gradle-default"}
      """
    Then the response status is 200
    When I PUT "/artifactory/gradle-local/org.acme/lib/1.0-20261017100000/lib-1.0-20261017100000.jar" as admin with body:
      """
      revision 100000
      """
    And I PUT "/artifactory/gradle-local/org.acme/lib/1.0-20261017090000/lib-1.0-20261017090000.jar" as admin with body:
      """
      revision 090000
      """
    And I GET "/artifactory/gradle-local/org.acme/lib/1.0-[INTEGRATION]/lib-1.0-[INTEGRATION].jar" as admin
    Then the response status is 200
    And the response body is "revision 100000"
