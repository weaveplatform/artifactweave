Feature: Maven repositories' REST operations and snapshot cleanup
  calculateMetadata rebuilds maven-metadata.xml on request, generatePom writes
  the POM of a jar deployed without one (#282), and maxUniqueSnapshots keeps
  the newest unique builds, deleted by a job that runs on a timer (#283). The
  scenarios shorten the timer to a second.

  Scenario: maxUniqueSnapshots keeps two unique builds per classifier
    Given the registry is running with environment "ARTIFACTWEAVE_MAVEN_SNAPSHOT_CLEANUP_INTERVAL=1s"
    When I PUT "/artifactory/api/repositories/mvn-snap" as admin with body:
      """
      {"rclass":"local","packageType":"maven","maxUniqueSnapshots":2}
      """
    Then the response status is 200
    When I PUT "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar" as admin with body:
      """
      build 1
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1-sources.jar" as admin with body:
      """
      build 1 sources
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar" as admin with body:
      """
      build 2
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.110000-3.jar" as admin with body:
      """
      build 3
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.110000-3.pom" as admin with body:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>app</artifactId><version>1.0-SNAPSHOT</version></project>
      """
    Then the response status is 201
    # Build 1's main artifact goes; its sources stay, the only build of
    # that classifier.
    Then within 20 seconds GET "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar" as admin answers 404
    When I GET "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1-sources.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-snap/org/acme/app/1.0-SNAPSHOT/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<buildNumber>3</buildNumber>"
    And the response body contains "<value>1.0-20261017.110000-3</value>"

  Scenario: calculateMetadata rebuilds metadata, and generatePom writes a jar's POM
    Given the registry is running
    When I PUT "/artifactory/api/repositories/mvn-rest" as admin with body:
      """
      {"rclass":"local","packageType":"maven"}
      """
    Then the response status is 200
    Given a zip file "app-1.0.jar" holding "META-INF/maven/org.acme/app/pom.xml" with content "<project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>app</artifactId><version>1.0</version><name>embedded</name></project>"
    When I deploy the file "app-1.0.jar" to "/artifactory/mvn-rest/org/acme/app/1.0/app-1.0.jar" as admin
    Then the response status is 201
    When I POST "/artifactory/api/maven/generatePom/mvn-rest/org/acme/app/1.0/app-1.0.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-rest/org/acme/app/1.0/app-1.0.pom" as admin
    Then the response status is 200
    And the response body contains "<name>embedded</name>"
    When I GET "/artifactory/mvn-rest/org/acme/app/maven-metadata.xml" as admin
    Then the response status is 200
    When I DELETE "/artifactory/mvn-rest/org/acme/app/maven-metadata.xml" as admin
    Then the response status is 204
    When I GET "/artifactory/mvn-rest/org/acme/app/maven-metadata.xml" as admin
    Then the response status is 404
    When I POST "/artifactory/api/maven/calculateMetadata/mvn-rest/org/acme" as admin
    Then the response status is 200
    And the response body is ""
    When I GET "/artifactory/mvn-rest/org/acme/app/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<release>1.0</release>"
    When I POST "/artifactory/api/maven/calculateMetadata/mvn-rest/org/missing" as admin
    Then the response status is 404

  # The acceptance #282 asks for: after jf rt u, which stores the files and
  # no metadata of its own, calculateMetadata writes what a deploy writes.
  @jf
  Scenario: calculateMetadata after jf rt u writes the metadata a deploy writes
    Given the registry is running
    And the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/mvn-jf" as admin with body:
      """
      {"rclass":"local","packageType":"maven"}
      """
    Then the response status is 200
    Given a file "lib-1.0.pom" with content:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>lib</artifactId><version>1.0</version></project>
      """
    And a file "lib-1.0.jar" with content:
      """
      the library
      """
    When I run jf "rt u lib-1.0.pom mvn-jf/org/acme/lib/1.0/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u lib-1.0.jar mvn-jf/org/acme/lib/1.0/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/mvn-jf/org/acme/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<release>1.0</release>"
    When I DELETE "/artifactory/mvn-jf/org/acme/lib/maven-metadata.xml" as admin
    Then the response status is 204
    When I POST "/artifactory/api/maven/calculateMetadata/mvn-jf/org/acme/lib?noneRecursive=true" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-jf/org/acme/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<latest>1.0</latest>"
    And the response body contains "<release>1.0</release>"
    And the response body contains "<version>1.0</version>"

  @maven
  Scenario: mvn deploys a snapshot three times and the cleanup keeps two builds
    Given the registry is running with environment "ARTIFACTWEAVE_MAVEN_SNAPSHOT_CLEANUP_INTERVAL=1s"
    And the Maven client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-snap" as admin with body:
      """
      {"rclass":"local","packageType":"maven","maxUniqueSnapshots":2}
      """
    Then the response status is 200
    Given the Maven project "snap" in "snap"
    And a file "settings.xml" with expanded content:
      """
      <settings>
        <servers>
          <server>
            <id>aw</id>
            <username>admin</username>
            <password>{password}</password>
          </server>
        </servers>
      </settings>
      """
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-snap"
    Then the command succeeds
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-snap"
    Then the command succeeds
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-snap"
    Then the command succeeds
    # Two builds of a jar and a POM, and the snapshot folder's metadata.
    Then within 20 seconds the JSON field "children" of GET "/artifactory/api/storage/mvn-snap/org/acme/smoke/snap/1.0-SNAPSHOT" as admin has 5 entries
    When I GET "/artifactory/mvn-snap/org/acme/smoke/snap/1.0-SNAPSHOT/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<buildNumber>3</buildNumber>"
