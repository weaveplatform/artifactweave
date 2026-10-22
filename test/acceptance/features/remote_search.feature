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
