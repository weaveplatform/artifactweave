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
    Then the response status is 400
    And the response body contains "must be lower case without an underscore"
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I GET "/v2/Docker-Local/team/app/manifests/latest" as admin
    Then the response status is 404
    And the JSON field "errors.0.code" equals "MANIFEST_UNKNOWN"
    When I GET "/artifactory/api/docker/Docker-Local/v2/team/app/manifests/latest" as admin
    Then the response status is 404
    And the JSON field "errors.0.code" equals "MANIFEST_UNKNOWN"
    When I GET "/v2/docker-local/Team/app/manifests/latest" as admin
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
    And the auth entry is keyed by the registry URL

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

  Scenario: A multi-architecture image and its signature
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I POST "/v2/docker-local/team/app/blobs/uploads/?digest=sha256:d6f56bc20064075ce319ac2e6fcef5de9ea21773b0a8a4398c4405222971f9c0" as admin with body:
      """
      {"architecture":"arm64","os":"linux"}
      """
    Then the response status is 201
    When I POST "/v2/docker-local/team/app/blobs/uploads/?digest=sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f" as admin with body:
      """
      hello blob
      """
    Then the response status is 201
    When I POST "/v2/docker-local/team/app/blobs/uploads/?digest=sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" as admin with body:
      """
      {}
      """
    Then the response status is 201
    When the next request has header "Content-Type" "application/vnd.oci.image.manifest.v1+json"
    And I PUT "/v2/docker-local/team/app/manifests/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d" as admin with body:
      """
      {"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:d6f56bc20064075ce319ac2e6fcef5de9ea21773b0a8a4398c4405222971f9c0","size":37},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f","size":10}]}
      """
    Then the response status is 201
    When the next request has header "Content-Type" "application/vnd.oci.image.index.v1+json"
    And I PUT "/v2/docker-local/team/app/manifests/multi" as admin with body:
      """
      {"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d","size":399,"platform":{"architecture":"arm64","os":"linux"}}]}
      """
    Then the response status is 201
    And the response header "Docker-Content-Digest" contains "sha256:fee8ebdf520089c0e85b9a2c792c6181c7e2440b1b7ae178f6d2c36a5afd035c"
    When the next request has header "Accept" "application/vnd.oci.image.index.v1+json"
    And I GET "/v2/docker-local/team/app/manifests/multi" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "application/vnd.oci.image.index.v1+json"
    And the JSON field "manifests.0.platform.architecture" equals "arm64"
    When I GET "/v2/docker-local/team/app/manifests/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d" as admin
    Then the response status is 200
    And the JSON field "config.digest" equals "sha256:d6f56bc20064075ce319ac2e6fcef5de9ea21773b0a8a4398c4405222971f9c0"
    When I GET "/artifactory/api/storage/docker-local/team/app/multi/list.manifest.json" as admin
    Then the response status is 200
    When I GET "/artifactory/api/search/prop?docker.os=linux&docker.architecture=arm64&repos=docker-local" as admin
    Then the response status is 200
    And the response body contains "/docker-local/team/app/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d/manifest.json"
    # The list's push also tagged its platform multi-linux-arm64, as 7.x
    # does from 7.21.2: a tag folder holding a copy of the platform manifest,
    # which so carries the same platform properties.
    When I GET "/v2/docker-local/team/app/tags/list" as admin
    Then the response status is 200
    And the JSON field "tags" has 2 entries
    And the JSON field "tags.0" equals "multi"
    And the JSON field "tags.1" equals "multi-linux-arm64"
    When I GET "/artifactory/api/storage/docker-local/team/app/multi-linux-arm64/manifest.json" as admin
    Then the response status is 200
    And the JSON field "checksums.sha256" equals "871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d"
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"docker-local","@docker.architecture":"arm64"}).include("path","name")
      """
    Then the response status is 200
    And the JSON field "range.total" equals "2"
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"docker-local","path":{"$match":"team/app/sha256:*"},"@docker.architecture":"arm64"}).include("path","name")
      """
    Then the response status is 200
    And the JSON field "range.total" equals "1"
    And the JSON field "results.0.path" equals "team/app/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d"
    And the JSON field "results.0.name" equals "manifest.json"
    # The platform manifest's folder keeps the digest's colon, as a 7.x local
    # repository does: jf docker build searches this exact path with $eq.
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"$and":[{"repo":"docker-local"},{"$or":[{"path":{"$eq":"team/app/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d"}}]}]}).include("name","repo","path","sha256","actual_sha1","actual_md5")
      """
    Then the response status is 200
    And the JSON field "range.total" equals "3"
    When I GET "/artifactory/api/storage/docker-local/team/app/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d" as admin
    Then the response status is 200
    And the JSON field "path" equals "/team/app/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d"
    And the JSON field "children" has 3 entries
    When I POST "/artifactory/api/copy/docker-local/team/app/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d?to=/docker-local/copied/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d" as admin
    Then the response status is 200
    When I GET "/v2/docker-local/copied/manifests/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d" as admin
    Then the response status is 200
    When the next request has header "Content-Type" "application/vnd.oci.image.manifest.v1+json"
    And I PUT "/v2/docker-local/team/app/manifests/sha256:22d26dd7f5f0802e86de103510b816796739580901e8585e66b20c185be0f69b" as admin with body:
      """
      {"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.signature","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2}],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d","size":399},"annotations":{"signer":"ci"}}
      """
    Then the response status is 201
    And the response header "OCI-Subject" contains "sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d"
    When I GET "/v2/docker-local/team/app/referrers/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "application/vnd.oci.image.index.v1+json"
    And the JSON field "manifests.0.digest" equals "sha256:22d26dd7f5f0802e86de103510b816796739580901e8585e66b20c185be0f69b"
    And the JSON field "manifests.0.artifactType" equals "application/vnd.example.signature"
    And the JSON field "manifests.0.annotations.signer" equals "ci"
    And the JSON field "manifests" has 1 entries
    When I GET "/v2/docker-local/team/app/referrers/sha256:871a7b7e86fbe5b01f905fb4c5f1b6d8a9aaece5265f56e243d3c68c9fd6019d?artifactType=application/vnd.example.sbom" as admin
    Then the response status is 200
    And the response header "OCI-Filters-Applied" contains "artifactType"
    And the JSON field "manifests" has 0 entries

  Scenario: Tags list, catalogs and manifest deletes
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
    When the next request has header "Content-Type" "application/vnd.oci.image.manifest.v1+json"
    And I PUT "/v2/docker-local/team/app/manifests/v2" as admin with body:
      """
      {"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:fb5dd9fcad531a9110d9bc07268019760d607378a0dd81ed14d403719c4180a8","size":89},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f","size":10}]}
      """
    Then the response status is 201
    When I GET "/v2/docker-local/team/app/tags/list?n=1" as admin
    Then the response status is 200
    And the JSON field "name" equals "docker-local/team/app"
    And the JSON field "tags" has 1 entries
    And the JSON field "tags.0" equals "v1"
    And the response header "Link" contains "last=v1"
    When I GET "/v2/_catalog" as admin
    Then the response status is 200
    And the JSON field "repositories.0" equals "docker-local/team/app"
    When I GET "/artifactory/api/docker/docker-local/v2/_catalog" as admin
    Then the response status is 200
    And the JSON field "repositories.0" equals "team/app"
    When I DELETE "/v2/docker-local/team/app/manifests/v1" as admin
    Then the response status is 202
    When I GET "/v2/docker-local/team/app/manifests/v1" as admin
    Then the response status is 404
    And the JSON field "errors.0.code" equals "MANIFEST_UNKNOWN"
    And the JSON field "errors.0.message" equals "The named manifest is not known to the registry."
    And the JSON field "errors.0.detail.manifest" equals "docker-local/team/app"
    When I GET "/artifactory/api/docker/docker-local/v2/team/app/tags/list" as admin
    Then the response status is 200
    And the JSON field "tags" has 1 entries
    And the JSON field "tags.0" equals "v2"

  Scenario: jf rt docker-promote copies, renames and moves an image
    Given the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/docker-prod" as admin with body:
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
    When the next request has header "Content-Type" "application/vnd.oci.image.manifest.v1+json"
    And I PUT "/v2/docker-local/team/app/manifests/v2" as admin with body:
      """
      {"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:fb5dd9fcad531a9110d9bc07268019760d607378a0dd81ed14d403719c4180a8","size":89},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:e997afd18e5f6be004fc193aed2c90291e68ab2c7599a62538c935b7fca6ab0f","size":10}]}
      """
    Then the response status is 201
    When I run jf "rt docker-promote team/app docker-local docker-prod --source-tag=v1 --copy=true --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/v2/docker-prod/team/app/manifests/v1" as admin
    Then the response status is 200
    When I GET "/v2/docker-local/team/app/manifests/v1" as admin
    Then the response status is 200
    When I run jf "rt docker-promote team/app docker-local docker-prod --source-tag=v1 --target-docker-image=team/web --target-tag=release --copy=true --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/v2/docker-prod/team/web/manifests/release" as admin
    Then the response status is 200
    When I run jf "rt s docker-prod/team/web/release/manifest.json --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "team/web"
    When I run jf "rt docker-promote team/app docker-local docker-prod --source-tag=v2 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/v2/docker-prod/team/app/manifests/v2" as admin
    Then the response status is 200
    When I GET "/v2/docker-local/team/app/manifests/v2" as admin
    Then the response status is 404
    When I POST "/artifactory/api/docker/docker-prod/v2/parentManifests" as admin with body:
      """
      {"dockerRepository":"team/app","tag":"v2"}
      """
    Then the response status is 200
    And the JSON field "parentManifestLists" has 0 entries
