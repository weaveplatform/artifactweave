Feature: Docker and OCI registry
  The registry protocol on the repository path, /v2/<repo>/<image>, and on
  Artifactory's own prefix, /artifactory/api/docker/<repo>/v2 (decisions 0003
  and 0031). A client probes /v2/ first and reads the version header from
  the answer, the challenge included, then fetches a token from the realm
  the challenge names (decision 0005).

  Background:
    Given the registry is running

  Scenario: The version probe challenges an anonymous client
    When I GET "/v2/"
    Then the response status is 401
    And the response header "Docker-Distribution-API-Version" contains "registry/2.0"
    And the response header "WWW-Authenticate" contains "Bearer realm="
    And the response header "WWW-Authenticate" contains "/artifactory/api/docker/null/v2/token"
    And the JSON field "errors.0.code" equals "UNAUTHORIZED"
    When I GET "/artifactory/api/docker/docker-local/v2/"
    Then the response status is 401
    And the response header "Docker-Distribution-API-Version" contains "registry/2.0"
    And the response header "WWW-Authenticate" contains "/artifactory/api/docker/docker-local/v2/token"

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

  @jf
  Scenario: An image pushed by tag is stored as Artifactory lays it out
    Given the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I POST "/v2/docker-local/team/app/blobs/uploads/?digest=sha256:fb5dd9fcad531a9110d9bc07268019760d607378a0dd81ed14d403719c4180a8" as admin with body:
      """
      {"architecture":"amd64","os":"linux","config":{"Labels":{"org.example.team":"platform"}}}
      """
    Then the response status is 201
    When I POST "/v2/docker-local/team/app/blobs/uploads/?digest=sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f" as admin with body:
      """
      hello blob
      """
    Then the response status is 201
    When the next request has header "Content-Type" "application/vnd.oci.image.manifest.v1+json"
    And I PUT "/v2/docker-local/team/app/manifests/v1" as admin with body:
      """
      {"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:fb5dd9fcad531a9110d9bc07268019760d607378a0dd81ed14d403719c4180a8","size":89},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f","size":10}]}
      """
    Then the response status is 201
    And the response header "Docker-Content-Digest" contains "sha256:"
    And the response header "Location" contains "/v2/docker-local/team/app/manifests/sha256:"
    When the next request has header "Accept" "application/vnd.oci.image.manifest.v1+json"
    And I GET "/v2/docker-local/team/app/manifests/v1" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "application/vnd.oci.image.manifest.v1+json"
    And the JSON field "layers.0.digest" equals "sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f"
    When I run jf "rt s docker-local/team/app/v1/manifest.json --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "docker-local/team/app/v1/manifest.json"
    And the output contains "docker.manifest.digest"
    And the output contains "application/vnd.oci.image.manifest.v1+json"
    And the output contains "docker.label.org.example.team"
    When I run jf "rt s docker-local/team/app/v1/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "docker-local/team/app/v1/sha256__fb5dd9fcad531a9110d9bc07268019760d607378a0dd81ed14d403719c4180a8"
    And the output contains "docker-local/team/app/v1/sha256__e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f"


  Scenario: A registry token opens the registry and nothing else
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I GET "/v2/docker-local/app/manifests/latest"
    Then the response status is 401
    And the response header "WWW-Authenticate" contains "repository:docker-local/app:pull"
    When I GET "/artifactory/api/docker/null/v2/token?scope=repository:docker-local/app:pull,push" as admin
    Then the response status is 200
    And the JSON field "expires_in" equals "3600"
    And I keep the JSON field "token" of the response as "registry"
    When I GET "/v2/" with the kept token "registry"
    Then the response status is 200
    When I GET "/artifactory/api/repositories" with the kept token "registry"
    Then the response status is 401
    When I POST "/v2/token" as admin
    Then the response status is 405
    When I GET "/artifactory/api/docker/docker-local/v2/auth" as admin
    Then the response status is 200
    And the JSON field "auth" is not empty

  @docker
  Scenario: docker login against the binary
    Given the Docker daemon can reach the registry
    When I run docker login as "admin" with password "{password}"
    Then the command succeeds
    And the output contains "Login Succeeded"
    When I run docker login as "admin" with password "wrong"
    Then the command fails

  Scenario: crane logs in and fetches a push token
    Given crane is available
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "auth token --push {host}/docker-local/app"
    Then the command succeeds
    And I keep the JSON field "token" of the output as "crane"
    When I GET "/v2/" with the kept token "crane"
    Then the response status is 200
