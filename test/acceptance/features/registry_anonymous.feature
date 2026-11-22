@clients @registry_anonymous
Feature: Anonymous registry pulls require the descriptor switch and an explicit read grant
  The publisher uses crane alone, so the Docker daemon has never received
  administrator credentials. Each anonymous client uses a new empty config.

  Background:
    Given the registry is running
    And crane is available
    When I PUT "/artifactory/api/repositories/docker-local" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/docker-private" as admin with body:
      """
      {"rclass":"local","packageType":"docker"}
      """
    Then the response status is 200
    Given a layer "public.tar.gz" holding "hello.txt" with content "anonymous registry fixture for {host}"
    When I run crane login as "admin" with password "{password}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run crane "append -f public.tar.gz -t {host}/docker-local/team/public:seed"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run crane "mutate {host}/docker-local/team/public:seed --set-platform linux/amd64 -t {host}/docker-local/team/public:v1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run crane "copy {host}/docker-local/team/public:v1 {host}/docker-private/team/public:v1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run crane "digest {host}/docker-local/team/public:v1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And I keep the output as "digest"
    Given I use fresh anonymous registry credentials

  @registry_regression
  Scenario: crane pulls anonymously through a registry token and cannot push
    When I run crane "pull {host}/docker-local/team/public:v1 disabled.tar"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I run crane "pull {host}/docker-local/team/public:v1 ungranted.tar"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"registry-anonymous-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"docker-local":{"include_patterns":["**"]}}}}}
      """
    Then the response status is 201
    When I GET "/v2/"
    Then the response status is 401
    And the response header "WWW-Authenticate" contains "Bearer realm="
    When I run crane "pull {host}/docker-local/team/public@{digest} anonymous.tar"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the registry served the pull as anonymous
    When I run crane "digest {host}/docker-local/team/public:v1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output is the kept value "digest"
    When I run crane "copy {host}/docker-local/team/public:v1 {host}/docker-local/team/denied:v1"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I GET "/v2/docker-local/team/denied/manifests/v1" as admin
    Then the response status is 404
    When I run crane "pull {host}/docker-private/team/public:v1 private.tar"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I GET "/v2/docker-private/team/public/manifests/v1" as admin
    Then the response status is 200
    When I DELETE "/access/api/v2/permissions/registry-anonymous-read" as admin
    Then the response status is 204
    When I run crane "pull {host}/docker-local/team/public:v1 revoked.tar"
    Then the command fails
    And the server answered no request with 500 since the last command

  @docker
  Scenario: docker pulls anonymously through a registry token and cannot push
    Given the Docker daemon can reach the registry
    When I run docker "pull {host}/docker-local/team/public:v1"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I run docker "pull {host}/docker-local/team/public:v1"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"registry-anonymous-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"docker-local":{"include_patterns":["**"]}}}}}
      """
    Then the response status is 201
    When I run docker "pull {host}/docker-local/team/public@{digest}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the registry served the pull as anonymous
    When I run docker "tag {host}/docker-local/team/public@{digest} {host}/docker-local/team/denied:v1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run docker "push {host}/docker-local/team/denied:v1"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I GET "/v2/docker-local/team/denied/manifests/v1" as admin
    Then the response status is 404
    When I run docker "pull {host}/docker-private/team/public:v1"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I GET "/v2/docker-private/team/public/manifests/v1" as admin
    Then the response status is 200
    When I DELETE "/access/api/v2/permissions/registry-anonymous-read" as admin
    Then the response status is 204
    When I run docker "pull {host}/docker-local/team/public:v1"
    Then the command fails
    And the server answered no request with 500 since the last command
    When I run docker "rmi {host}/docker-local/team/public@{digest} {host}/docker-local/team/denied:v1"
    Then the command succeeds
    And the server answered no request with 500 since the last command
