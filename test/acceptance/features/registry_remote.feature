@clients @registry_remote
Feature: Authenticated remote Docker, OCI artifacts and OCI Helm
  Native clients push fixtures into a local upstream repository and pull them
  through a separate remote repository. The upstream uses its own challenge
  and credentials. This qualifies artifactweave; vendor parity awaits P19.

  Background:
    Given the registry is running
    When I PUT "/artifactory/api/repositories/registry-upstream" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/registry-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"docker","url":"{platform}","dockerProjectId":"registry-upstream","username":"admin","password":"{password}","enableTokenAuthentication":true}
      """
    Then the response status is 200

  Scenario: crane verifies every platform through the cold cache and again offline
    Given crane is available
    And a layer "layer.tar.gz" holding "hello.txt" with content "registry remote fixture"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/registry-upstream/team/image:seed"
    Then the command succeeds
    When I run crane "mutate {host}/registry-upstream/team/image:seed --set-platform linux/amd64 -t {host}/registry-upstream/team/image:amd64"
    Then the command succeeds
    When I run crane "mutate {host}/registry-upstream/team/image:amd64 --set-platform linux/arm64 -t {host}/registry-upstream/team/image:arm64"
    Then the command succeeds
    When I run crane "index append -m {host}/registry-upstream/team/image:amd64 -m {host}/registry-upstream/team/image:arm64 -t {host}/registry-upstream/team/image:multi"
    Then the command succeeds
    When I run crane "pull --format oci --platform all {host}/registry-proxy/team/image:multi cold.tar"
    Then the command succeeds
    When I run crane "manifest {host}/registry-proxy/team/image:multi"
    Then the command succeeds
    And the output contains "arm64"
    And the output contains "amd64"
    When I POST "/artifactory/api/zap/registry-proxy-cache/team/image" as admin with body:
      """
      """
    Then the response status is 200
    When I run crane "pull --format oci --platform all {host}/registry-proxy/team/image:multi after-zap.tar"
    Then the command succeeds
    When I POST "/artifactory/api/repositories/registry-proxy" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I run crane "pull --format oci --platform all {host}/registry-proxy/team/image:multi offline.tar"
    Then the command succeeds
    When I run crane "copy {host}/registry-upstream/team/image:amd64 {host}/registry-proxy/team/image:denied"
    Then the command fails

  Scenario: oras pulls an artifact and its discovered referrer through the remote
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
    When I run oras "push {host}/registry-upstream/team/notes:v1 --plain-http --artifact-type application/vnd.example.notes notes.txt:text/plain"
    Then the command succeeds
    When I run oras "attach {host}/registry-upstream/team/notes:v1 --plain-http --artifact-type application/vnd.example.review --format json review.txt:text/plain"
    Then the command succeeds
    And I keep the JSON field "digest" of the output as "referrer"
    When I run oras "pull {host}/registry-proxy/team/notes:v1 --plain-http -o pulled"
    Then the command succeeds
    And the files "notes.txt" and "pulled/notes.txt" are identical
    When I run oras "discover {host}/registry-proxy/team/notes:v1 --plain-http --distribution-spec v1.1-referrers-api --depth 1"
    Then the command succeeds
    And the output contains "application/vnd.example.review"
    When I run oras "pull {host}/registry-proxy/team/notes@{referrer} --plain-http -o reviewed"
    Then the command succeeds
    And the files "review.txt" and "reviewed/review.txt" are identical
    When I POST "/artifactory/api/repositories/registry-proxy" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I run oras "pull {host}/registry-proxy/team/notes@{referrer} --plain-http -o offline-review"
    Then the command succeeds
    And the files "review.txt" and "offline-review/review.txt" are identical

  Scenario: Helm pulls an OCI chart through the remote and retains its package metadata
    Given helm is available
    When I PUT "/artifactory/api/repositories/helm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"helmoci","url":"{platform}","dockerProjectId":"registry-upstream","username":"admin","password":"{password}","enableTokenAuthentication":true}
      """
    Then the response status is 200
    When I run helm "create acme"
    Then the command succeeds
    When I run helm "package acme --version 0.1.0"
    Then the command succeeds
    When I run helm "registry login {host} --plain-http --username admin --password {password}"
    Then the command succeeds
    When I run helm "push acme-0.1.0.tgz oci://{host}/registry-upstream --plain-http"
    Then the command succeeds
    Given a file "pulled/.keep" with content:
      """
      chart output
      """
    When I run helm "pull oci://{host}/helm-proxy/acme --version 0.1.0 --plain-http --destination pulled"
    Then the command succeeds
    And the files "acme-0.1.0.tgz" and "pulled/acme-0.1.0.tgz" are identical
    When I GET "/artifactory/api/storage/helm-proxy-cache/acme/0.1.0/manifest.json?properties=docker.manifest.type" as admin
    Then the response status is 200
    And the response body contains "application/vnd.oci.image.manifest.v1+json"
    When I POST "/artifactory/api/zap/helm-proxy-cache/acme" as admin with body:
      """
      """
    Then the response status is 200
    When I run helm "pull oci://{host}/helm-proxy/acme --version 0.1.0 --plain-http --destination pulled"
    Then the command succeeds
    And the files "acme-0.1.0.tgz" and "pulled/acme-0.1.0.tgz" are identical
    When I POST "/artifactory/api/repositories/helm-proxy" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I run helm "pull oci://{host}/helm-proxy/acme --version 0.1.0 --plain-http --destination pulled"
    Then the command succeeds
