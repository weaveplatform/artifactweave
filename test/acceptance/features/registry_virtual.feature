@clients @registry_virtual
Feature: Virtual Docker, OCI and Helm OCI repositories resolve local and remote members

  Background:
    Given the registry is running
    When I PUT "/artifactory/api/repositories/registry-upstream" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/registry-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/registry-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"docker","url":"{platform}","dockerProjectId":"registry-upstream","username":"admin","password":"{password}","enableTokenAuthentication":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/registry-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"docker","repositories":["registry-local","registry-proxy"]}
      """
    Then the response status is 200

  Scenario: crane pulls local and remote images by tag and digest through one virtual
    Given crane is available
    And a layer "local.tar.gz" holding "hello.txt" with content "local image"
    And a layer "remote.tar.gz" holding "hello.txt" with content "remote image"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f local.tar.gz -t {host}/registry-local/team/image:local"
    Then the command succeeds
    When I run crane "append -f remote.tar.gz -t {host}/registry-upstream/team/image:remote"
    Then the command succeeds
    When I run crane "copy {host}/registry-upstream/team/image:remote {host}/registry-upstream/team/image:local"
    Then the command succeeds
    When I run crane "digest {host}/registry-local/team/image:local"
    Then the command succeeds
    And I keep the output as "local_digest"
    When I run crane "digest {host}/registry-virtual/team/image:local"
    Then the command succeeds
    And the output is the kept value "local_digest"
    When I run crane "pull {host}/registry-virtual/team/image:local local-image.tar"
    Then the command succeeds
    When I run crane "pull {host}/registry-virtual/team/image:remote remote-image.tar"
    Then the command succeeds
    When I run crane "digest {host}/registry-upstream/team/image:remote"
    Then the command succeeds
    And I keep the output as "remote_digest"
    When I run crane "pull {host}/registry-virtual/team/image@{remote_digest} digest-image.tar"
    Then the command succeeds
    And the server answered no request with 500 since the last command

  Scenario: oras pulls both OCI members through a virtual repository
    Given oras is available
    And a file "notes.txt" with content:
      """
      OCI virtual fixture
      """
    When I PUT "/artifactory/api/repositories/oci-local" as admin with body:
      """
      {"rclass":"local","packageType":"oci"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/oci-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"oci","url":"{platform}","dockerProjectId":"registry-upstream","username":"admin","password":"{password}","enableTokenAuthentication":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/oci-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"oci","repositories":["oci-local","oci-proxy"]}
      """
    Then the response status is 200
    When I run oras "login {host} -u admin -p {password} --plain-http"
    Then the command succeeds
    When I run oras "push {host}/oci-local/team/local:v1 --plain-http --artifact-type application/vnd.example.notes notes.txt:text/plain"
    Then the command succeeds
    When I run oras "push {host}/registry-upstream/team/remote:v1 --plain-http --artifact-type application/vnd.example.notes notes.txt:text/plain"
    Then the command succeeds
    When I run oras "pull {host}/oci-virtual/team/local:v1 --plain-http -o local"
    Then the command succeeds
    And the files "notes.txt" and "local/notes.txt" are identical
    When I run oras "pull {host}/oci-virtual/team/remote:v1 --plain-http -o remote"
    Then the command succeeds
    And the files "notes.txt" and "remote/notes.txt" are identical
    And the server answered no request with 500 since the last command

  Scenario: Helm pulls local and remote chart versions through a Helm OCI virtual
    Given helm is available
    When I PUT "/artifactory/api/repositories/helm-local" as admin with body:
      """
      {"rclass":"local","packageType":"helmoci"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/helm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"helmoci","url":"{platform}","dockerProjectId":"registry-upstream","username":"admin","password":"{password}","enableTokenAuthentication":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/helm-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"helmoci","repositories":["helm-local","helm-proxy"]}
      """
    Then the response status is 200
    When I run helm "create acme"
    Then the command succeeds
    When I run helm "package acme --version 0.1.0"
    Then the command succeeds
    When I run helm "package acme --version 0.2.0"
    Then the command succeeds
    # Seed this scenario's file store so Helm does not select the OS keychain.
    Given a file ".config/helm/registry/config.json" with expanded content:
      """
      {"auths":{"{host}":{}}}
      """
    When I run helm "registry login {host} --plain-http --username admin --password {password}"
    Then the command succeeds
    When I run helm "push acme-0.1.0.tgz oci://{host}/helm-local --plain-http"
    Then the command succeeds
    When I run helm "push acme-0.2.0.tgz oci://{host}/registry-upstream --plain-http"
    Then the command succeeds
    Given a file "pulled/.keep" with content:
      """
      output directory
      """
    When I run helm "pull oci://{host}/helm-virtual/acme --version 0.1.0 --plain-http --destination pulled"
    Then the command succeeds
    And the files "acme-0.1.0.tgz" and "pulled/acme-0.1.0.tgz" are identical
    When I run helm "pull oci://{host}/helm-virtual/acme --version 0.2.0 --plain-http --destination pulled"
    Then the command succeeds
    And the files "acme-0.2.0.tgz" and "pulled/acme-0.2.0.tgz" are identical
    And the server answered no request with 500 since the last command

  @docker
  Scenario: docker pulls a remote image through a virtual repository
    Given the Docker daemon can reach the registry
    And crane is available
    And a layer "layer.tar.gz" holding "hello.txt" with content "Docker virtual fixture"
    When I run crane "auth login {host} -u admin -p {password}"
    Then the command succeeds
    When I run crane "append -f layer.tar.gz -t {host}/registry-upstream/team/image:seed"
    Then the command succeeds
    When I run crane "mutate {host}/registry-upstream/team/image:seed --set-platform linux/amd64 -t {host}/registry-upstream/team/image:remote"
    Then the command succeeds
    When I run docker login as "admin" with password "{password}"
    Then the command succeeds
    When I run docker "pull {host}/registry-virtual/team/image:remote"
    Then the command succeeds
    And the server answered no request with 500 since the last command
