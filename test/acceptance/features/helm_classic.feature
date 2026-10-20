@helm @helm_classic
Feature: Helm pulls from a derived repository index
  Classic Helm repositories rebuild index.yaml from uploaded chart archives.
  Both URL modes resolve through the real Helm client without a client index.

  Scenario Outline: helm verifies and renders a signed chart with relative URLs <relative>
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
    Given a Helm signing keyring in "signing"
    When I run helm "package acme --destination charts --sign --key artifactweave --keyring signing/private.gpg"
    Then the command succeeds
    When I deploy the files under "charts" to "/artifactory/helm-local" as admin
    And I run helm "repo add aw {url}/api/helm/helm-local --username admin --password {password}"
    Then the command succeeds
    When I run helm "repo update aw"
    Then the command succeeds
    When I run helm "search repo aw/acme --versions"
    Then the command succeeds
    And the output contains "aw/acme"
    And the output contains "0.1.0"
    When I run helm "pull aw/acme --version 0.1.0 --verify --keyring signing/public.gpg"
    Then the command succeeds
    And the files "charts/acme-0.1.0.tgz" and "acme-0.1.0.tgz" are identical
    And the files "charts/acme-0.1.0.tgz.prov" and "acme-0.1.0.tgz.prov" are identical
    And the server answered 200 to GET "/artifactory/<download>/acme-0.1.0.tgz"
    And the server answered 200 to GET "/artifactory/<download>/acme-0.1.0.tgz.prov"
    When I GET "/artifactory/api/helm/helm-local/index.yaml" as admin
    Then the response status is 200
    And the Helm index has one "acme" version "0.1.0" matching "acme-0.1.0.tgz"
    When I run helm "template preview acme-0.1.0.tgz"
    Then the command succeeds
    And the output contains "kind: Deployment"

    Examples:
      | relative | download            |
      | true     | api/helm/helm-local |
      | false    | helm-local          |

  Scenario: layout enforcement refuses renamed and duplicate charts
    Given the registry is running
    And helm is available
    When I PUT "/artifactory/api/repositories/helm-enforced" as admin with body:
      """
      {"rclass":"local","packageType":"helm","forceMetadataNameVersion":true,"forceNonDuplicateChart":true}
      """
    Then the response status is 200
    When I run helm "create acme"
    Then the command succeeds
    When I run helm "package acme --destination charts"
    Then the command succeeds
    When I deploy the file "charts/acme-0.1.0.tgz" to "/artifactory/helm-enforced/renamed.tgz" as admin
    Then the response status is 403
    And the response body contains "the package file renamed does not match the package name/version acme:0.1.0 in the metadata file"
    When I deploy the file "charts/acme-0.1.0.tgz" to "/artifactory/helm-enforced/acme-0.1.0.tgz" as admin
    Then the response status is 201
    When I deploy the file "charts/acme-0.1.0.tgz" to "/artifactory/helm-enforced/another/acme-0.1.0.tgz" as admin
    Then the response status is 403
    And the response body contains "a package with the same name and version acme-0.1.0 already exists in the repository"
    When I GET "/artifactory/api/helm/helm-enforced/index.yaml" as admin
    Then the response status is 200
    And the Helm index has one "acme" version "0.1.0" matching "charts/acme-0.1.0.tgz"
    And the response body does not contain "another/"
    And the response body does not contain "renamed.tgz"

  Scenario: duplicates select the newest chart and the index follows item changes
    Given the registry is running
    And helm is available
    When I PUT "/artifactory/api/repositories/helm-life" as admin with body:
      """
      {"rclass":"local","packageType":"helm"}
      """
    Then the response status is 200
    When I run helm "create acme"
    Then the command succeeds
    When I run helm "package acme --destination first"
    Then the command succeeds
    When I deploy the file "first/acme-0.1.0.tgz" to "/artifactory/helm-life/original/acme-0.1.0.tgz" as admin
    Then the response status is 201
    Given a file "acme/newest.txt" with content:
      """
      The second archive has different content and the same chart name/version.
      """
    When I run helm "package acme --destination second"
    Then the command succeeds
    When I deploy the file "second/acme-0.1.0.tgz" to "/artifactory/helm-life/duplicate/renamed.tgz" as admin
    Then the response status is 201
    When I GET "/artifactory/api/helm/helm-life/index.yaml" as admin
    Then the response status is 200
    And the Helm index has one "acme" version "0.1.0" matching "second/acme-0.1.0.tgz"
    And the response body contains "duplicate/renamed.tgz"
    And the response body does not contain "original/acme-0.1.0.tgz"
    When I DELETE "/artifactory/helm-life/duplicate/renamed.tgz" as admin
    Then the response status is 204
    When I GET "/artifactory/api/helm/helm-life/index.yaml" as admin
    Then the response status is 200
    And the Helm index has one "acme" version "0.1.0" matching "first/acme-0.1.0.tgz"
    And the response body contains "original/acme-0.1.0.tgz"
    When I POST "/artifactory/api/copy/helm-life/original/acme-0.1.0.tgz?to=/helm-life/copied/acme-0.1.0.tgz" as admin
    Then the response status is 200
    When I GET "/artifactory/api/helm/helm-life/index.yaml" as admin
    Then the response status is 200
    And the response body contains "copied/acme-0.1.0.tgz"
    When I POST "/artifactory/api/move/helm-life/copied/acme-0.1.0.tgz?to=/helm-life/moved/acme-0.1.0.tgz" as admin
    Then the response status is 200
    When I GET "/artifactory/api/helm/helm-life/index.yaml" as admin
    Then the response status is 200
    And the response body contains "moved/acme-0.1.0.tgz"
    And the response body does not contain "copied/"
    When I PUT "/artifactory/api/storage/helm-life/moved/acme-0.1.0.tgz?properties=chart.name=stale" as admin
    Then the response status is 204
    When I PUT "/artifactory/api/storage/helm-life/original/acme-0.1.0.tgz?properties=chart.name=outside-partial" as admin
    Then the response status is 204
    When I POST "/artifactory/api/helm/helm-life/moved/reindex" as admin
    Then the response status is 200
    And the response body contains "scheduled to run"
    Then within 10 seconds the body of GET "/artifactory/api/storage/helm-life/moved/acme-0.1.0.tgz?properties=chart.name" as admin does not contain "stale"
    When I GET "/artifactory/api/storage/helm-life/moved/acme-0.1.0.tgz?properties=chart.name" as admin
    Then the response status is 200
    And the JSON field "properties" equals "map[chart.name:[acme]]"
    When I GET "/artifactory/api/storage/helm-life/original/acme-0.1.0.tgz?properties=chart.name" as admin
    Then the response status is 200
    And the response body contains "outside-partial"
    When I GET "/artifactory/api/helm/helm-life/index.yaml" as admin
    Then the response status is 200
    And the Helm index has one "acme" version "0.1.0" matching "first/acme-0.1.0.tgz"
    When I DELETE "/artifactory/helm-life/original" as admin
    Then the response status is 204
    When I DELETE "/artifactory/helm-life/moved" as admin
    Then the response status is 204
    When I GET "/artifactory/api/helm/helm-life/index.yaml" as admin
    Then the response status is 200
    And the response body contains "entries: {}"
