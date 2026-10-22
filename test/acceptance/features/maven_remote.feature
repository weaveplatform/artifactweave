@p9_maven_remote @maven
Feature: Maven downloads authoritative upstream content through a remote cache
  The current native Maven client uses an empty artifact cache for each run.
  The shared local tail contains plugins only for this fixture namespace.

  Scenario: Maven resolves release, parent, transitive and both snapshot forms cold and during an upstream outage
    Given the registry is running
    And the Maven client can reach the registry
    And a Maven upstream serves parent, transitive, release and snapshot fixtures
    When I PUT "/artifactory/api/repositories/maven-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"maven","url":"{upstream}","rejectInvalidJars":true,"remoteRepoChecksumPolicyType":"fail"}
      """
    Then the response status is 200
    Given a file "settings.xml" with expanded content:
      """
      <settings><servers><server><id>aw</id><username>admin</username><password>{password}</password></server></servers></settings>
      """
    And a file "pom.xml" with expanded content:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>org.acme.consumer</groupId><artifactId>remote-test</artifactId><version>1</version>
      <repositories><repository><id>aw</id><url>{url}/maven-remote</url><releases><enabled>true</enabled></releases><snapshots><enabled>true</enabled><updatePolicy>always</updatePolicy></snapshots></repository></repositories>
      <dependencies>
      <dependency><groupId>org.acme.p9remote625</groupId><artifactId>release</artifactId><version>1.0</version></dependency>
      <dependency><groupId>org.acme.p9remote625</groupId><artifactId>unique</artifactId><version>1.1-SNAPSHOT</version></dependency>
      <dependency><groupId>org.acme.p9remote625</groupId><artifactId>nonunique</artifactId><version>2.0-SNAPSHOT</version></dependency>
      </dependencies></project>
      """
    When I run mvn "-s settings.xml -C -Dmaven.repo.local=/work/cold -Dmaven.repo.local.tail={m2} org.apache.maven.plugins:maven-dependency-plugin:3.8.1:copy-dependencies -DoutputDirectory=/work/cold-jars"
    Then the command succeeds
    And the Maven upstream received a cold GET for every fixture
    And the file "cold-jars/transitive-1.0.jar" exists
    Given the generic upstream stops
    When I run mvn "-s settings.xml -C -Dmaven.repo.local=/work/warm -Dmaven.repo.local.tail={m2} org.apache.maven.plugins:maven-dependency-plugin:3.8.1:copy-dependencies -DoutputDirectory=/work/warm-jars"
    Then the command succeeds
    And the file "warm-jars/release-1.0.jar" exists
    And the file "warm-jars/transitive-1.0.jar" exists
    And the file "warm-jars/unique-1.1-SNAPSHOT.jar" exists
    And the file "warm-jars/nonunique-2.0-SNAPSHOT.jar" exists
    When I GET "/artifactory/maven-remote-cache/org/acme/p9remote625/unique/1.1-SNAPSHOT/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "1.1-20261021.120000-1"
