@remote_search
Feature: Upstream version searches discover uncached artifacts without downloading them
  This fixture proves protocol and policy behavior, not a captured vendor response.

  Scenario: curl searches authoritative Maven metadata with browsing disabled and survives an upstream outage
    Given the registry is running
    And an upstream search document "/org/acme/lib/maven-metadata.xml" of type "application/xml" contains:
      """
      <metadata><groupId>org.acme</groupId><artifactId>lib</artifactId><versioning><versions><version>1.0</version><version>2.0</version></versions></versioning></metadata>
      """
    When I PUT "/artifactory/api/repositories/search-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"maven","url":"{upstream}","listRemoteFolderItems":false}
      """
    Then the response status is 200
    When I GET "/artifactory/api/search/versions?g=org.acme&a=lib&repos=search-remote&remote=0" as admin
    Then the response status is 404
    And the generic upstream received 0 GET requests for "/org/acme/lib/maven-metadata.xml"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/latestVersion?g=org.acme&a=lib&repos=search-remote&remote=1"
    Then the command succeeds
    And the output contains "2.0"
    And the generic upstream received 1 GET requests for "/org/acme/lib/maven-metadata.xml"
    And the generic upstream received 0 GET requests for "/org/acme/lib/2.0/lib-2.0.jar"
    When I GET "/artifactory/api/storage/search-remote-cache/org/acme/lib/2.0/lib-2.0.jar" as admin
    Then the response status is 404
    Given the generic upstream stops
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/latestVersion?g=org.acme&a=lib&repos=search-remote&remote=1"
    Then the command succeeds
    And the output contains "2.0"
    When I GET "/artifactory/api/search/versions?g=org.acme&a=lib&repos=search-remote-cache&remote=1" as admin
    Then the response status is 404

  Scenario: Ivy listings obey the browsing flag and GAVC returns proxy downloads
    Given the registry is running
    And an upstream search document "/org.acme/lib/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="3.0/">3.0/</a>
      """
    And an upstream search document "/org.acme/lib/3.0/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="jars/">jars/</a>
      """
    And an upstream search document "/org.acme/lib/3.0/jars/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="lib-3.0.jar">lib-3.0.jar</a>
      """
    When I PUT "/artifactory/api/repositories/search-ivy" as admin with body:
      """
      {"rclass":"remote","packageType":"ivy","repoLayoutRef":"ivy-default","url":"{upstream}","listRemoteFolderItems":false}
      """
    Then the response status is 200
    When I GET "/artifactory/api/search/versions?g=org.acme&a=lib&repos=search-ivy&remote=1" as admin
    Then the response status is 404
    And the generic upstream received 0 GET requests for "/org.acme/lib/"
    When I POST "/artifactory/api/repositories/search-ivy" as admin with body:
      """
      {"listRemoteFolderItems":true}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/versions?g=org.acme&a=lib&repos=search-ivy&remote=1"
    Then the command succeeds
    And the output contains "3.0"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/gavc?g=org.acme&a=lib&repos=search-ivy&specific=true&remote=1"
    Then the command succeeds
    And the output contains "downloadUri"
    And the output contains "/search-ivy/org.acme/lib/3.0/jars/lib-3.0.jar"
    And the generic upstream received 0 GET requests for "/org.acme/lib/3.0/jars/lib-3.0.jar"

  Scenario: Mapped Maven discovery returns Gradle canonical paths and preserves descriptor bytes
    Given the registry is running
    And an upstream search document "/org/acme/lib/maven-metadata.xml" of type "application/xml" contains:
      """
      <metadata><groupId>org.acme</groupId><artifactId>lib</artifactId><versioning><versions><version>2.0</version></versions></versioning></metadata>
      """
    And an upstream search document "/org/acme/lib/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="maven-metadata.xml">metadata</a><a href="2.0/">2.0/</a>
      """
    And an upstream search document "/org/acme/lib/2.0/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="lib-2.0.jar">jar</a><a href="lib-2.0-sources.jar">sources</a><a href="lib-2.0.pom">pom</a>
      """
    And an upstream search document "/org/acme/lib/2.0/lib-2.0.pom" of type "application/xml" contains:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>lib</artifactId><version>2.0</version></project>
      """
    When I PUT "/artifactory/api/repositories/search-mapped" as admin with body:
      """
      {"rclass":"remote","packageType":"maven","repoLayoutRef":"gradle-default","remoteRepoLayoutRef":"maven-2-default","url":"{upstream}","listRemoteFolderItems":false}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/latestVersion?g=org.acme&a=lib&repos=search-mapped&remote=1"
    Then the command succeeds
    And the output contains "2.0"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/gavc?g=org.acme&a=lib&repos=search-mapped&specific=true&remote=1"
    Then the command succeeds
    And the output contains "/search-mapped/org.acme/lib/2.0/lib-2.0.jar"
    And the output contains "/search-mapped/org.acme/lib/2.0/lib-2.0-sources.jar"
    And the output contains "/search-mapped/org.acme/lib/ivy-2.0.xml"
    And the generic upstream received 0 GET requests for "/org/acme/lib/2.0/lib-2.0.jar"
    And the generic upstream received 0 GET requests for "/org/acme/lib/2.0/lib-2.0.pom"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/search-mapped/org.acme/lib/ivy-2.0.xml"
    Then the command succeeds
    And the output contains "<artifactId>lib</artifactId>"
    And the generic upstream received 1 GET requests for "/org/acme/lib/2.0/lib-2.0.pom"
    When I GET "/artifactory/search-mapped/org.acme/lib/2.0/lib-2.0.pom" as admin
    Then the response status is 400
    And the generic upstream received 1 GET requests for "/org/acme/lib/2.0/lib-2.0.pom"
    Given the generic upstream stops
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/latestVersion?g=org.acme&a=lib&repos=search-mapped&remote=1"
    Then the command succeeds
    And the output contains "2.0"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/gavc?g=org.acme&a=lib&repos=search-mapped&specific=true&remote=1"
    Then the command succeeds
    And the output contains "/search-mapped/org.acme/lib/ivy-2.0.xml"

  Scenario: A custom canonical layout discovers an Ivy upstream without downloading packages
    Given the registry is running
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      repoLayouts:
        search-custom-ivy:
          name: search-custom-ivy
          artifactPathPattern: 'canonical/[orgPath]/[module]/[baseRev]/[type]/[module](-[classifier])-[baseRev].[ext]'
          descriptorPathPattern: 'canonical/[orgPath]/[module]/[baseRev]/[type]/ivy-[baseRev].xml'
          distinctiveDescriptorPathPattern: true
      """
    Then the response status is 200
    Given an upstream search document "/org.acme/lib/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="3.0/">3.0/</a>
      """
    And an upstream search document "/org.acme/lib/3.0/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="jars/">jars/</a><a href="ivys/">ivys/</a>
      """
    And an upstream search document "/org.acme/lib/3.0/jars/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="lib-3.0.jar">jar</a><a href="lib-sources-3.0.jar">sources</a>
      """
    And an upstream search document "/org.acme/lib/3.0/ivys/" of type "text/html" contains:
      """
      <!DOCTYPE html><a href="ivy-3.0.xml">ivy</a>
      """
    When I PUT "/artifactory/api/repositories/search-custom" as admin with body:
      """
      {"rclass":"remote","packageType":"ivy","repoLayoutRef":"search-custom-ivy","remoteRepoLayoutRef":"ivy-default","url":"{upstream}","listRemoteFolderItems":true}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/versions?g=org.acme&a=lib&repos=search-custom&remote=1"
    Then the command succeeds
    And the output contains "3.0"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/gavc?g=org.acme&a=lib&repos=search-custom&specific=true&remote=1"
    Then the command succeeds
    And the output contains "/search-custom/canonical/org/acme/lib/3.0/jar/lib-3.0.jar"
    And the output contains "/search-custom/canonical/org/acme/lib/3.0/jar/lib-sources-3.0.jar"
    And the output contains "/search-custom/canonical/org/acme/lib/3.0/ivy/ivy-3.0.xml"
    And the generic upstream received 0 GET requests for "/org.acme/lib/3.0/jars/lib-3.0.jar"
    And the generic upstream received 0 GET requests for "/org.acme/lib/3.0/ivys/ivy-3.0.xml"
    Given the generic upstream stops
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/api/search/versions?g=org.acme&a=lib&repos=search-custom&remote=1"
    Then the command succeeds
    And the output contains "3.0"
