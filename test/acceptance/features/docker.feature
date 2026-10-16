Feature: Docker and OCI registry
  The registry protocol on the repository path, /v2/<repo>/<image>, and on
  Artifactory's own prefix, /artifactory/api/docker/<repo>/v2 (decisions 0003
  and 0031). A client probes /v2/ first and reads the version header from
  the answer, the challenge included.

  Background:
    Given the registry is running

  Scenario: The version probe challenges an anonymous client
    When I GET "/v2/"
    Then the response status is 401
    And the response header "Docker-Distribution-API-Version" contains "registry/2.0"
    And the response header "WWW-Authenticate" contains "Basic"
    And the JSON field "errors.0.code" equals "UNAUTHORIZED"
    When I GET "/artifactory/api/docker/docker-local/v2/"
    Then the response status is 401
    And the response header "Docker-Distribution-API-Version" contains "registry/2.0"

  Scenario: The version probe answers an authenticated client
    When I GET "/v2/" as admin
    Then the response status is 200
    And the response header "Docker-Distribution-API-Version" contains "registry/2.0"
    And the response body is "{}"
    When I GET "/artifactory/api/docker/docker-local/v2/" as admin
    Then the response status is 200
    And the response header "Docker-Distribution-API-Version" contains "registry/2.0"

  Scenario: Registry paths reach a repository by key in any case
    When I PUT "/artifactory/api/repositories/Docker-Local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I GET "/v2/docker-local/Team/app/manifests/latest" as admin
    Then the response status is 400
    And the JSON field "errors.0.code" equals "NAME_INVALID"
    When I GET "/artifactory/api/docker/docker-local/v2/Team/app/manifests/latest" as admin
    Then the response status is 400
    And the JSON field "errors.0.code" equals "NAME_INVALID"
    When I GET "/v2/missing/app/manifests/latest" as admin
    Then the response status is 404
    And the JSON field "errors.0.code" equals "NAME_UNKNOWN"

  Scenario: A remote Docker repository is not served yet
    When I PUT "/artifactory/api/repositories/docker-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"docker","url":"https://registry-1.docker.io"}
      """
    Then the response status is 200
    When I GET "/v2/docker-remote/library/nginx/manifests/latest" as admin
    Then the response status is 405
    And the JSON field "errors.0.code" equals "UNSUPPORTED"
    And the response body contains "remote repository"
