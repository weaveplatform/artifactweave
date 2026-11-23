Feature: Maven repositories with mvn, the JFrog CLI and Gradle
  calculateMetadata rebuilds maven-metadata.xml on request, generatePom writes
  the POM of a jar deployed without one (#282), and maxUniqueSnapshots keeps
  the newest unique builds, deleted by a job that runs on a timer (#283). The
  scenarios shorten the timer to a second. The P7 Maven exit checks (#284)
  follow: mvn deploys and resolves releases and snapshots under each
  snapshotVersionBehavior and checksum policy, Maven 3.10 and 3.9 both run
  the round trip, and jf mvn and jf gradle publish with a build. mvn runs in
  the containers pinned by digest (test/acceptance/clients); jf runs the
  runner's own mvn and gradle.

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
    And the server answered no request with 500 since the last command
    When I run jf "rt u lib-1.0.jar mvn-jf/org/acme/lib/1.0/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
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
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-snap"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-snap"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    # Two builds of a jar and a POM, and the snapshot folder's metadata.
    Then within 20 seconds the JSON field "children" of GET "/artifactory/api/storage/mvn-snap/org/acme/smoke/snap/1.0-SNAPSHOT" as admin has 5 entries
    When I GET "/artifactory/mvn-snap/org/acme/smoke/snap/1.0-SNAPSHOT/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<buildNumber>3</buildNumber>"

  # The P7 Maven exit checks (#284) start here. Every mvn deploy runs package
  # then deploy:deploy, never install, and every resolve uses a fresh local
  # repository chained to private pinned tools, so an artifact under test
  # comes from artifactweave alone.
  @maven
  Scenario Outline: <maven> deploys a release and a snapshot twice, and a build resolves both with strict checksums
    Given the registry is running
    And the <maven> client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-local" as admin with body:
      """
      {"rclass":"local","packageType":"maven"}
      """
    Then the response status is 200
    Given the Maven project "lib" in "lib"
    And the Maven project "snap" in "snap"
    And the Maven project "app" in "app"
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
    When I run mvn "-f lib/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-local"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-local"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-local"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    # Two unique builds of the jar and the POM beside the snapshot metadata,
    # which names the second.
    Then within 20 seconds the JSON field "children" of GET "/artifactory/api/storage/mvn-local/org/acme/smoke/snap/1.0-SNAPSHOT" as admin has 5 entries
    And within 20 seconds the body of GET "/artifactory/mvn-local/org/acme/smoke/snap/1.0-SNAPSHOT/maven-metadata.xml" as admin contains "<buildNumber>2</buildNumber>"
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<release>1.0.0</release>"
    # A tampered checksum is refused under client-checksums, so the .sha1
    # mvn sent stays the one served.
    When I PUT "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar.sha1" as admin with body:
      """
      0000000000000000000000000000000000000000
      """
    Then the response status is 409
    And the response body contains "Checksum error for"
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar.sha1" as admin
    Then the response status is 200
    And the response body does not contain "0000000000000000000000000000000000000000"
    # -C fails the build on any checksum that does not match its file. The
    # consumer compiles against both jars, so each must be the real one.
    When I run mvn "-f app/pom.xml -s settings.xml -C -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2} -Daw.url={url}/mvn-local package"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file "fresh/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" exists
    And the file "fresh/org/acme/smoke/snap/1.0-SNAPSHOT/maven-metadata-aw.xml" contains "<buildNumber>2</buildNumber>"
    # A warm tool supply cannot satisfy product resolution after its registry
    # target disappears; this second consumer head is empty too.
    When I DELETE "/artifactory/api/repositories/mvn-local" as admin
    Then the response status is 200
    When I run mvn "-f app/pom.xml -s settings.xml -C -Dmaven.repo.local=/work/disabled-product -Dmaven.repo.local.tail={m2} -Daw.url={url}/mvn-local package"
    Then the command fails
    And the server answered no request with 500 since the last command

    Examples:
      | maven     |
      | Maven     |
      | Maven 3.9 |

  @maven
  Scenario: mvn deploys a snapshot twice to a non-unique repository, each build replacing the last
    Given the registry is running
    And the Maven client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-nonunique" as admin with body:
      """
      {"rclass":"local","packageType":"maven","snapshotVersionBehavior":"non-unique"}
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
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-nonunique"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-nonunique"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    # mvn sends unique names; the repository stores them as -SNAPSHOT, so
    # the folder holds one jar, one POM and the metadata.
    Then within 20 seconds the JSON field "children" of GET "/artifactory/api/storage/mvn-nonunique/org/acme/smoke/snap/1.0-SNAPSHOT" as admin has 3 entries
    When I GET "/artifactory/mvn-nonunique/org/acme/smoke/snap/1.0-SNAPSHOT/snap-1.0-SNAPSHOT.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-nonunique/org/acme/smoke/snap/1.0-SNAPSHOT/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<buildNumber>1</buildNumber>"
    And the response body does not contain "<timestamp>"
    When I run mvn "-s settings.xml -C -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2} org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact=org.acme.smoke:snap:1.0-SNAPSHOT -DremoteRepositories=aw::default::{url}/mvn-nonunique"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file "fresh/org/acme/smoke/snap/1.0-SNAPSHOT/snap-1.0-SNAPSHOT.jar" exists

  @maven
  Scenario: A releases-only repository takes mvn's release and refuses its snapshot with 409
    Given the registry is running
    And the Maven client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-releases" as admin with body:
      """
      {"rclass":"local","packageType":"maven","handleSnapshots":false}
      """
    Then the response status is 200
    Given the Maven project "lib" in "lib"
    And the Maven project "snap" in "snap"
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
    When I run mvn "-f lib/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-releases"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-releases"
    Then the command fails
    And the server answered no request with 500 since the last command
    And the output contains "409"
    When I GET "/artifactory/api/storage/mvn-releases/org/acme/smoke/snap" as admin
    Then the response status is 404
    When I GET "/artifactory/mvn-releases/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" as admin
    Then the response status is 200

  # Under server-generated-checksums a client's checksum is taken and never
  # served, so a tampered one cannot fail a build that checks strictly.
  @maven
  Scenario: Under server-generated checksums a tampered .sha1 is ignored and mvn resolves with strict checksums
    Given the registry is running
    And the Maven client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-trust" as admin with body:
      """
      {"rclass":"local","packageType":"maven","checksumPolicyType":"server-generated-checksums"}
      """
    Then the response status is 200
    When I PUT "/artifactory/mvn-trust/org/acme/trust/lib/1.0/lib-1.0.pom" as admin with body:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>org.acme.trust</groupId><artifactId>lib</artifactId><version>1.0</version></project>
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-trust/org/acme/trust/lib/1.0/lib-1.0.jar" as admin with body:
      """
      the trusted library
      """
    Then the response status is 201
    When I PUT "/artifactory/mvn-trust/org/acme/trust/lib/1.0/lib-1.0.jar.sha1" as admin with body:
      """
      0000000000000000000000000000000000000000
      """
    Then the response status is 201
    When I GET "/artifactory/mvn-trust/org/acme/trust/lib/1.0/lib-1.0.jar.sha1" as admin
    Then the response status is 200
    And the response body is "00fae1d7b4d308e4f08622aa5681033b4b8f1468"
    Given a file "settings.xml" with expanded content:
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
    When I run mvn "-s settings.xml -C -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2} org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact=org.acme.trust:lib:1.0 -DremoteRepositories=aw::default::{url}/mvn-trust"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file "fresh/org/acme/trust/lib/1.0/lib-1.0.jar" exists

  # jf mvn runs Maven with the JFrog Maven extractor, which deploys each
  # module's files itself at -SNAPSHOT paths with the build's timestamp, and
  # records them for jf rt bp. jf runs the runner's own Maven, 3.10, which
  # the extractor jf 2.131.0 downloads (2.44.1) starts on (#523). The
  # runner's Java is 17, below the fixtures' release 21. The build installs
  # into its own product head, with a private pinned tool tail and a seeded
  # extractor. External Maven repositories are blocked during assertions.
  @jf @maven @maven_virtual
  Scenario Outline: jf mvn deploys a release and a snapshot through <class> with a build, and jf rt bp publishes it
    Given the registry is running
    And the JFrog CLI is available
    And mvn is on the runner's PATH
    And a maven deployment repository "<repo>" of class "<class>" is ready
    Given the Maven project "lib" in "lib"
    And the Maven project "snap" in "snap"
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "mvnc --server-id-deploy aw --repo-deploy-releases <repo> --repo-deploy-snapshots <repo>" in "lib"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "mvnc --server-id-deploy aw --repo-deploy-releases <repo> --repo-deploy-snapshots <repo>" in "snap"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "mvn install -B -Dmaven.repo.local={dir}/jf-m2 -Dmaven.compiler.release=17 --build-name mvn-accept --build-number 1" in "lib"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "mvn install -B -Dmaven.repo.local={dir}/jf-m2 -Dmaven.compiler.release=17 --build-name mvn-accept --build-number 1" in "snap"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "rt bp mvn-accept 1 --server-id aw"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I GET "/artifactory/api/build/mvn-accept/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules" has 2 entries
    And the response body contains "org.acme.smoke:lib:1.0.0"
    And the response body contains "org.acme.smoke:snap:1.0-SNAPSHOT"
    When I GET "/artifactory/api/storage/<ddr>/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar?properties" as admin
    Then the response status is 200
    And the response body contains "build.name"
    And the response body contains "mvn-accept"
    And within 20 seconds the body of GET "/artifactory/<repo>/org/acme/smoke/lib/maven-metadata.xml" as admin contains "<release>1.0.0</release>"
    # One unique build, named from the build's timestamp, of the jar and
    # the POM, beside the snapshot metadata.
    Then within 20 seconds the JSON field "children" of GET "/artifactory/api/storage/<ddr>/org/acme/smoke/snap/1.0-SNAPSHOT" as admin has 3 entries
    And the response body contains "-1.jar"
    When I GET "/artifactory/<repo>/org/acme/smoke/snap/1.0-SNAPSHOT/snap-1.0-SNAPSHOT.jar" as admin
    Then the response status is 200

    Examples:
      | class   | repo           | ddr                   |
      | local   | mvn-jf         | mvn-jf                |
      | virtual | mvn-jf-virtual | mvn-jf-virtual-member |

  # Gradle's run is informational (decisions 0015 and 0036): the job runs
  # @gradle in a step of its own that does not gate the merge.
  @jf @gradle @maven_virtual
  Scenario Outline: jf gradle publishes through <class> with a build, and jf rt bp publishes it
    Given the registry is running
    And the JFrog CLI is available
    And gradle is on the runner's PATH
    And a gradle deployment repository "<repo>" of class "<class>" is ready
    Given a file "glib/settings.gradle" with content:
      """
      rootProject.name = 'glib'
      """
    And a file "glib/build.gradle" with content:
      """
      plugins {
          id 'java-library'
          id 'maven-publish'
      }

      group = 'org.acme.gradle'
      version = '1.0.0'

      publishing {
          publications {
              mavenJava(MavenPublication) {
                  from components.java
              }
          }
      }
      """
    And a file "glib/src/main/java/org/acme/gradle/Glib.java" with content:
      """
      package org.acme.gradle;

      /** The class jf gradle publishes. */
      public final class Glib {
          private Glib() {}
      }
      """
    # Gradle's own caches stay in the scenario's files.
    And the JFrog CLI runs with "GRADLE_USER_HOME" set to "{dir}/.gradle"
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "gradlec --server-id-deploy aw --repo-deploy <repo> --deploy-maven-desc --use-wrapper=false --uses-plugin=false" in "glib"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "gradle artifactoryPublish --no-daemon --build-name gradle-accept --build-number 1" in "glib"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run jf "rt bp gradle-accept 1 --server-id aw"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I GET "/artifactory/<repo>/org/acme/gradle/glib/1.0.0/glib-1.0.0.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/<repo>/org/acme/gradle/glib/1.0.0/glib-1.0.0.pom" as admin
    Then the response status is 200
    And within 20 seconds the body of GET "/artifactory/<repo>/org/acme/gradle/glib/maven-metadata.xml" as admin contains "<release>1.0.0</release>"
    When I GET "/artifactory/api/build/gradle-accept/1" as admin
    Then the response status is 200
    And the response body contains "org.acme.gradle:glib:1.0.0"

    When I GET "/artifactory/api/storage/<ddr>/org/acme/gradle/glib/1.0.0/glib-1.0.0.jar" as admin
    Then the response status is 200

    Examples:
      | class   | repo           | ddr                   |
      | local   | gradle-local   | gradle-local          |
      | virtual | gradle-virtual | gradle-virtual-member |


  @maven @maven_virtual
  Scenario Outline: <maven> deploys release and snapshots through a mixed virtual and resolves merged versions without repository escape
    Given the registry is running
    And the <maven> client can reach the registry
    And a mixed Maven virtual deployment fixture is ready
    Then within 20 seconds the body of GET "/artifactory/mvn-member/org/acme/p10virtual1559/range/maven-metadata.xml" as admin contains "<version>1.0</version>"
    And within 20 seconds the body of GET "/artifactory/mvn-member/org/acme/p10virtual1559/shared/1.0-SNAPSHOT/maven-metadata.xml" as admin contains "<buildNumber>1</buildNumber>"
    Given the Maven project "lib" in "lib"
    And the Maven project "snap" in "snap"
    And the Maven project "app" in "app"
    And the Maven virtual consumer uses remote, range and shared snapshot dependencies
    And a file "settings.xml" with expanded content:
      """
      <settings><servers><server><id>aw</id><username>admin</username><password>{password}</password></server></servers></settings>
      """
    When I run mvn "-f lib/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-virtual"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-virtual"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run mvn "-f snap/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-virtual"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    Then within 20 seconds the JSON field "children" of GET "/artifactory/api/storage/mvn-member/org/acme/smoke/snap/1.0-SNAPSHOT" as admin has 5 entries
    And within 20 seconds the body of GET "/artifactory/mvn-virtual/org/acme/smoke/snap/1.0-SNAPSHOT/maven-metadata.xml" as admin contains "<buildNumber>2</buildNumber>"
    When I GET "/artifactory/api/storage/mvn-member/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" as admin
    Then the response status is 200
    When I run mvn "-f app/pom.xml -s settings.xml -C -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2} -Daw.url={url}/mvn-virtual package"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And Maven resolved the expected mixed origins without POM repository escape
    When I run mvn "-f lib/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-no-ddr"
    Then the command fails
    And the server answered no request with 500 since the last command
    And the server answered 405 to PUT "/artifactory/mvn-no-ddr/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar"
    When I PUT "/artifactory/mvn-no-ddr/no-target.txt" as admin with body:
      """
      probe
      """
    Then the response status is 405
    And the response header "Allow" contains "GET"
    And the response body contains "No local repository was configured as local deployment repository for the (mvn-no-ddr) virtual repository."

    Examples:
      | maven     |
      | Maven     |
      | Maven 3.9 |
