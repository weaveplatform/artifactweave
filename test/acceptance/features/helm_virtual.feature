@helm @helm_virtual
Feature: Helm resolves local and remote charts through one virtual repository

  Scenario Outline: signed charts and remote dependencies resolve with relative URLs <relative>
    Given the registry is running with environment "ARTIFACTWEAVE_HELM_RELATIVE_URLS=<relative>"
    And helm is available
    When I run helm "create acme"
    Then the command succeeds
    Given a Helm signing keyring in "signing"
    When I run helm "package acme --destination charts --sign --key artifactweave --keyring signing/private.gpg"
    Then the command succeeds
    When I run helm "repo index charts"
    Then the command succeeds
    Given a Helm upstream serves the files under "charts"
    When I PUT "/artifactory/api/repositories/helm-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"helm","url":"{upstream}"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/helm-local" as admin with body:
      """
      {"rclass":"local","packageType":"helm"}
      """
    Then the response status is 200
    When I run helm "create web"
    Then the command succeeds
    When I run helm "package web --destination local"
    Then the command succeeds
    When I deploy the files under "local" to "/artifactory/helm-local" as admin
    When I PUT "/artifactory/api/repositories/helm-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"helm","repositories":["helm-local","helm-remote"]}
      """
    Then the response status is 200
    When I run helm "repo add aw {url}/api/helm/helm-virtual --username admin --password {password}"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run helm "repo update aw"
    Then the command succeeds
    When I run helm "search repo aw --versions"
    Then the command succeeds
    And the output contains "aw/acme"
    And the output contains "aw/web"
    When I run helm "pull aw/acme --version 0.1.0 --verify --keyring signing/public.gpg"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the files "charts/acme-0.1.0.tgz" and "acme-0.1.0.tgz" are identical
    And the files "charts/acme-0.1.0.tgz.prov" and "acme-0.1.0.tgz.prov" are identical
    And the server answered 200 to GET "/artifactory/<download>/acme-0.1.0.tgz"
    And the server answered 200 to GET "/artifactory/<download>/acme-0.1.0.tgz.prov"
    When I run helm "pull aw/web --version 0.1.0"
    Then the command succeeds
    And the files "local/web-0.1.0.tgz" and "web-0.1.0.tgz" are identical
    When I run helm "template preview acme-0.1.0.tgz"
    Then the command succeeds
    And the output contains "kind: Deployment"
    Given a file "parent/Chart.yaml" with content:
      """
      apiVersion: v2
      name: parent
      version: 1.0.0
      dependencies:
        - name: acme
          version: 0.1.0
          repository: "@aw"
      """
    When I run helm "dependency update parent"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the files "charts/acme-0.1.0.tgz" and "parent/charts/acme-0.1.0.tgz" are identical
    When I GET "/artifactory/api/helm/helm-virtual/index.yaml" as admin
    Then the response status is 200
    And the response body does not contain "local://"

    Examples:
      | relative | download             |
      | true     | api/helm/helm-virtual |
      | false    | helm-virtual         |
