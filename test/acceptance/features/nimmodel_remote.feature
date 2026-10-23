@nimmodel @registry_remote
Feature: NIM metadata and its matching container use separate remote repositories
  The model API resolves through the NIM remote while a native registry client
  pulls the matching image through a Docker remote. Each cache works offline.

  Scenario: metadata and native Docker pull resolve the same image and tag
    Given the registry is running
    And crane is available
    And an NGC fixture serves NIM metadata for the matching image
    When I PUT "/artifactory/api/repositories/nim-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nimmodel","url":"{upstream}"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/nvcr-upstream" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/nvcr-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"docker","url":"{platform}","dockerProjectId":"nvcr-upstream","username":"admin","password":"{password}","enableTokenAuthentication":true}
      """
    Then the response status is 200
    When I GET "/artifactory/api/nimmodel/nim-remote/v2/nim/metadata?resourceId=nim%2Fmeta%2Fllama&tag=1.3" as admin
    Then the response status is 200
    And the response body contains "container_version"
    And the generic upstream received 1 GET requests for "/v2/nim/metadata"
    Given a layer "layer.tar.gz" holding "model.txt" with content "paired NIM image"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/nvcr-upstream/nim/meta/llama:1.3"
    Then the command succeeds
    When I run crane "pull --format oci {host}/nvcr-remote/nim/meta/llama:1.3 cold.tar"
    Then the command succeeds
    When I POST "/artifactory/api/repositories/nim-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I POST "/artifactory/api/repositories/nvcr-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I GET "/artifactory/api/nimmodel/nim-remote/v2/nim/metadata?resourceId=nim%2Fmeta%2Fllama&tag=1.3" as admin
    Then the response status is 200
    When I run crane "pull --format oci {host}/nvcr-remote/nim/meta/llama:1.3 offline.tar"
    Then the command succeeds
