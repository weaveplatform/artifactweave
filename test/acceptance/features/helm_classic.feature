@helm @helm_classic
Feature: Helm pulls from a derived repository index
  Classic Helm repositories rebuild index.yaml from uploaded chart archives.
  Both URL modes resolve through the real Helm client without a client index.

  Scenario Outline: helm updates and pulls with relative URLs <relative>
    Given the registry is running with environment "ARTIFACTWEAVE_HELM_RELATIVE_URLS=<relative>"
    And helm is available
    When I PUT "/artifactory/api/repositories/helm-local" as admin with body:
      """
      {"rclass":"local","packageType":"helm"}
      """
    Then the response status is 200
    When I GET "/artifactory/helm-local/index.yaml" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "application/x-yaml"
    And the response body contains "entries: {}"
    When I run helm "create acme"
    Then the command succeeds
    When I run helm "package acme --destination charts"
    Then the command succeeds
    When I deploy the files under "charts" to "/artifactory/helm-local" as admin
    And I run helm "repo add aw {url}/api/helm/helm-local --username admin --password {password}"
    Then the command succeeds
    When I run helm "repo update aw"
    Then the command succeeds
    When I run helm "pull aw/acme --version 0.1.0"
    Then the command succeeds
    And the files "charts/acme-0.1.0.tgz" and "acme-0.1.0.tgz" are identical
    And the server answered 200 to GET "/artifactory/<download>/acme-0.1.0.tgz"

    Examples:
      | relative | download            |
      | true     | api/helm/helm-local |
      | false    | helm-local          |
