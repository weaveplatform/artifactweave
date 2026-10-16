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

  Scenario: A blob uploads whole and in chunks and pulls back
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I POST "/v2/docker-local/team/app/blobs/uploads/?digest=sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f" as admin with body:
      """
      hello blob
      """
    Then the response status is 201
    And the response header "Location" contains "/v2/docker-local/team/app/blobs/sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f"
    And the response header "Docker-Content-Digest" contains "sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f"
    When I HEAD "/v2/docker-local/team/app/blobs/sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f" as admin
    Then the response status is 200
    And the response header "Content-Length" contains "10"
    When I GET "/v2/docker-local/team/app/blobs/sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f" as admin
    Then the response status is 200
    And the response body is "hello blob"
    When I POST "/v2/docker-local/team/app/blobs/uploads/" as admin
    Then the response status is 202
    And the response header "Range" contains "0-0"
    And I keep the path of the response header "Location" as "upload"
    When the next request has header "Content-Range" "0-5"
    And I PATCH "{upload}" as admin with body:
      """
      chunk-
      """
    Then the response status is 202
    And the response header "Range" contains "0-5"
    When the next request has header "Content-Range" "2-4"
    And I PATCH "{upload}" as admin with body:
      """
      two
      """
    Then the response status is 416
    And the JSON field "errors.0.code" equals "BLOB_UPLOAD_INVALID"
    When I PUT "{upload}?digest=sha256:c089183495a5984e2c76941009c89d4e8f881c799c4beb102aec7d515cb32ed2" as admin with body:
      """
      two
      """
    Then the response status is 201
    When I GET "/artifactory/api/docker/docker-local/v2/team/app/blobs/sha256:c089183495a5984e2c76941009c89d4e8f881c799c4beb102aec7d515cb32ed2" as admin
    Then the response status is 200
    And the response body is "chunk-two"
    When I GET "/artifactory/api/storage/docker-local/team/app/_uploads/sha256__c089183495a5984e2c76941009c89d4e8f881c799c4beb102aec7d515cb32ed2" as admin
    Then the response status is 200
    And the JSON field "checksums.sha256" equals "c089183495a5984e2c76941009c89d4e8f881c799c4beb102aec7d515cb32ed2"
