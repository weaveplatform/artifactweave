@clients
Feature: Real registry clients round-trip images (the P6 exit checks)
  The P6 exit criteria (docs/implementation/plan.md): docker push and pull
  through the runner's daemon, crane, oras and cosign round trips, and the
  JFrog CLI's Docker commands decision 0031 covers. Each client is the
  pinned release CI downloads; a scenario skips when its client is absent,
  and the docker ones on any host but Linux, whose daemon cannot reach the
  binary on 127.0.0.1.

  Background:
    Given the registry is running
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

  @docker
  Scenario: docker builds, pushes and pulls an image
    Given the Docker daemon can reach the registry
    And a file "image/Dockerfile" with content:
      """
      FROM scratch
      COPY hello.txt /hello.txt
      """
    And a file "image/hello.txt" with content:
      """
      hello from artifactweave
      """
    When I run docker login as "admin" with password "{password}"
    Then the command succeeds
    When I run docker "build --platform linux/amd64 -t {host}/docker-local/team/app:v1 image"
    Then the command succeeds
    When I run docker "push {host}/docker-local/team/app:v1"
    Then the command succeeds
    When I GET "/artifactory/api/storage/docker-local/team/app/v1/manifest.json" as admin
    Then the response status is 200
    When I GET "/artifactory/api/search/prop?docker.os=linux&docker.architecture=amd64&repos=docker-local" as admin
    Then the response status is 200
    And the response body contains "/docker-local/team/app/v1/manifest.json"
    When I run docker "rmi {host}/docker-local/team/app:v1"
    Then the command succeeds
    When I run docker "pull {host}/docker-local/team/app:v1"
    Then the command succeeds
    And the output contains "Downloaded newer image"

  Scenario: crane copies, reads and deletes images
    Given crane is available
    And a layer "layer.tar.gz" holding "hello.txt" with content "hello from crane"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/docker-local/team/base:v1"
    Then the command succeeds
    When I run crane "copy {host}/docker-local/team/base:v1 {host}/docker-prod/team/base:v1"
    Then the command succeeds
    When I GET "/artifactory/api/storage/docker-prod/team/base/v1/manifest.json" as admin
    Then the response status is 200
    When I run crane "manifest {host}/docker-prod/team/base:v1"
    Then the command succeeds
    And the output contains "schemaVersion"
    When I run crane "digest {host}/docker-prod/team/base:v1"
    Then the command succeeds
    And I keep the output as "digest"
    When I run crane "delete {host}/docker-prod/team/base@{digest}"
    Then the command succeeds
    When I run crane "manifest {host}/docker-prod/team/base:v1"
    Then the command fails
    When I run crane "manifest {host}/docker-local/team/base:v1"
    Then the command succeeds

  Scenario: oras pushes, pulls, attaches and discovers an artifact
    Given oras is available
    And a file "notes.txt" with content:
      """
      release notes
      """
    And a file "review.txt" with content:
      """
      approved
      """
    When I run oras "login {host} -u admin -p {password} --plain-http"
    Then the command succeeds
    When I run oras "push {host}/docker-local/team/notes:v1 --plain-http --artifact-type application/vnd.example.notes notes.txt:text/plain"
    Then the command succeeds
    When I run oras "pull {host}/docker-local/team/notes:v1 --plain-http -o pulled"
    Then the command succeeds
    And the files "notes.txt" and "pulled/notes.txt" are identical
    When I run oras "attach {host}/docker-local/team/notes:v1 --plain-http --artifact-type application/vnd.example.review review.txt:text/plain"
    Then the command succeeds
    When I run oras "discover {host}/docker-local/team/notes:v1 --plain-http"
    Then the command succeeds
    And the output contains "application/vnd.example.review"

  Scenario: cosign signs an image and verifies it, the signature found through referrers
    Given crane is available
    And cosign is available
    And a layer "layer.tar.gz" holding "hello.txt" with content "hello from cosign"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/docker-local/team/signed:v1"
    Then the command succeeds
    When I run crane "digest {host}/docker-local/team/signed:v1"
    Then the command succeeds
    And I keep the output as "digest"
    When I run cosign "generate-key-pair"
    Then the command succeeds
    When I run cosign "sign --key cosign.key --use-signing-config=false --tlog-upload=false --allow-http-registry --allow-insecure-registry {host}/docker-local/team/signed@{digest}"
    Then the command succeeds
    When I run cosign "verify --key cosign.pub --insecure-ignore-tlog=true --allow-http-registry --allow-insecure-registry {host}/docker-local/team/signed@{digest}"
    Then the command succeeds
    When I GET "/v2/docker-local/team/signed/referrers/{digest}" as admin
    Then the response status is 200
    And the JSON field "manifests" has 1 entries

  Scenario: jf rt build-docker-create records an image crane pushed
    Given crane is available
    And the JFrog CLI is available
    And a layer "layer.tar.gz" holding "hello.txt" with content "hello from kaniko's stand-in"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/docker-local/team/built:v1"
    Then the command succeeds
    When I run crane "digest {host}/docker-local/team/built:v1"
    Then the command succeeds
    And I keep the output as "digest"
    And a file "image-file" with expanded content:
      """
      {host}/docker-local/team/built:v1@{digest}
      """
    When I run jf "rt build-docker-create docker-local --image-file image-file --build-name=docked --build-number=1 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp docked 1 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/build/docked/1" as admin
    Then the response status is 200
    And the response body contains "team/built:v1"

  Scenario: jf rt docker-promote copies and moves a tag between repositories
    Given crane is available
    And the JFrog CLI is available
    And a layer "layer.tar.gz" holding "hello.txt" with content "hello from promotion"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/docker-local/team/promo:v1"
    Then the command succeeds
    When I run jf "rt docker-promote team/promo docker-local docker-prod --source-tag=v1 --target-tag=v1-prod --copy --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/v2/docker-prod/team/promo/manifests/v1-prod" as admin
    Then the response status is 200
    When I GET "/v2/docker-local/team/promo/manifests/v1" as admin
    Then the response status is 200
    When I run jf "rt docker-promote team/promo docker-local docker-prod --source-tag=v1 --target-docker-image=team/moved --target-tag=v1 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/v2/docker-prod/team/moved/manifests/v1" as admin
    Then the response status is 200
    When I GET "/v2/docker-local/team/promo/manifests/v1" as admin
    Then the response status is 404

  @docker
  Scenario: jf docker push and pull record build-info
    Given the Docker daemon can reach the registry
    And the JFrog CLI is available
    And a file "image/Dockerfile" with content:
      """
      FROM scratch
      COPY hello.txt /hello.txt
      """
    And a file "image/hello.txt" with content:
      """
      hello from jf docker
      """
    When I run docker login as "admin" with password "{password}"
    Then the command succeeds
    When I run docker "build -t {host}/docker-local/team/jfapp:v1 image"
    Then the command succeeds
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "docker push {host}/docker-local/team/jfapp:v1 --server-id aw --build-name=jfdock --build-number=1"
    Then the command succeeds
    When I run jf "rt bp jfdock 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/jfdock/1" as admin
    Then the response status is 200
    And the response body contains "team/jfapp:v1"
    When I run docker "rmi {host}/docker-local/team/jfapp:v1"
    Then the command succeeds
    When I run jf "docker pull {host}/docker-local/team/jfapp:v1 --server-id aw --build-name=jfdock-pull --build-number=1"
    Then the command succeeds
