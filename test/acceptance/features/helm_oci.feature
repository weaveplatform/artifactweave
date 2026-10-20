@clients @helm
Feature: Helm charts in Helm OCI repositories, pushed with jf helm
  A helmoci repository is served by the Docker registry (decision 0031), and
  decision 0039 adds what charts need: the chart's own version in the
  package index, and Max Unique Tags keeping N tags of a chart that Helm
  3.18 and later push twice, by digest and then by tag. jf helm runs the
  helm on its PATH, here the pinned release CI downloads; a scenario skips
  without jf or helm. Both run on the runner itself, so they need no Docker.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And the JFrog CLI runs the pinned helm
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "helm create acme"
    Then the command succeeds

  # jf helm push finds the chart's version folder with AQL, sets the build
  # properties on it and records its manifest and blobs in a helm module
  # (research 30, 1.3). A version holding "+" is not found by jf, which
  # searches the version where the tag holds "_", so the chart has none.
  @helmoci_exit
  Scenario: jf helm push publishes a build-info naming the chart
    When I PUT "/artifactory/api/repositories/helmoci-local" as admin with body:
      """
      {"rclass":"local","packageType":"helmoci"}
      """
    Then the response status is 200
    When I run jf "helm package acme --version 0.1.0"
    Then the command succeeds
    When I run jf "helm push acme-0.1.0.tgz oci://{host}/helmoci-local --plain-http --username admin --password {password} --build-name=charts --build-number=1"
    Then the command succeeds
    When I run jf "rt bp charts 1"
    Then the command succeeds
    When I GET "/artifactory/api/build/charts/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.type" equals "helm"
    And the JSON field "buildInfo.modules.0.id" equals "acme:0.1.0"
    And the response body contains "acme/0.1.0/manifest.json"
    When I GET "/artifactory/api/storage/helmoci-local/acme/0.1.0/manifest.json?properties=build.name" as admin
    Then the response status is 200
    And the response body contains "charts"
    # Keep credentials in the scenario's file store. An empty host entry
    # disables the client's automatic selection of the host OS keychain.
    Given a file ".config/helm/registry/config.json" with expanded content:
      """
      {"auths":{"{host}":{}}}
      """
    When I run helm "registry login {host} --plain-http --username admin --password {password}"
    Then the command succeeds
    Given a file "pulled/.keep" with content:
      """
      The OCI archive is downloaded into this directory.
      """
    When I run helm "pull oci://{host}/helmoci-local/acme --version 0.1.0 --plain-http --destination pulled"
    Then the command succeeds
    And the files "acme-0.1.0.tgz" and "pulled/acme-0.1.0.tgz" are identical
    When I run helm "show chart oci://{host}/helmoci-local/acme --version 0.1.0 --plain-http"
    Then the command succeeds
    And the output contains "name: acme"
    And the output contains "version: 0.1.0"
    # A new tag needs only Read and Deploy. Changing its contents exercises
    # Strict Tag Overwrite; Helm skips an identical manifest already present.
    When I PUT "/artifactory/api/security/users/chart-writer" as admin with body:
      """
      {"name":"chart-writer","email":"writer@example.invalid","password":"Chart-writer-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/chart-publish" as admin with body:
      """
      {"name":"chart-publish","repositories":["helmoci-local"],"includesPattern":"**","principals":{"users":{"chart-writer":["r","w"]}}}
      """
    Then the response status is 200
    When I run helm "registry login {host} --plain-http --username chart-writer --password Chart-writer-123"
    Then the command succeeds
    When I run helm "package acme --version 0.2.0"
    Then the command succeeds
    When I run helm "push acme-0.2.0.tgz oci://{host}/helmoci-local --plain-http"
    Then the command succeeds
    Given a file "acme/replacement.txt" with content:
      """
      A replacement of the same chart version requires Delete.
      """
    When I run helm "package acme --version 0.2.0"
    Then the command succeeds
    When I run helm "push acme-0.2.0.tgz oci://{host}/helmoci-local --plain-http"
    Then the command fails
    And the server answered 403 to PUT "/v2/helmoci-local/acme/manifests/0.2.0"
    When I PUT "/artifactory/api/security/permissions/chart-publish" as admin with body:
      """
      {"name":"chart-publish","repositories":["helmoci-local"],"includesPattern":"**","principals":{"users":{"chart-writer":["r","w","d"]}}}
      """
    Then the response status is 200
    When I run helm "push acme-0.2.0.tgz oci://{host}/helmoci-local --plain-http"
    Then the command succeeds
    When I run helm "pull oci://{host}/helmoci-local/acme --version 0.2.0 --plain-http --destination pulled"
    Then the command succeeds
    And the files "acme-0.2.0.tgz" and "pulled/acme-0.2.0.tgz" are identical

  # Each push leaves the chart's tag folder and a digest folder holding the
  # same manifest; the limit removes the older tag with its digest folder.
  Scenario: Max Unique Tags keeps one chart version and its digest copy
    When I PUT "/artifactory/api/repositories/helmoci-limited" as admin with body:
      """
      {"rclass":"local","packageType":"helmoci","maxUniqueTags":1}
      """
    Then the response status is 200
    When I run jf "helm package acme --version 0.1.0"
    Then the command succeeds
    When I run jf "helm push acme-0.1.0.tgz oci://{host}/helmoci-limited --plain-http --username admin --password {password}"
    Then the command succeeds
    When I run jf "helm package acme --version 0.2.0"
    Then the command succeeds
    When I run jf "helm push acme-0.2.0.tgz oci://{host}/helmoci-limited --plain-http --username admin --password {password}"
    Then the command succeeds
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"helmoci-limited","name":"manifest.json"})
      """
    Then the response status is 200
    And the JSON field "range.total" equals "2"
    And the response body contains "acme/0.2.0"
    And the response body contains "acme/sha256:"
    And the response body does not contain "acme/0.1.0"
    When I run jf "helm pull oci://{host}/helmoci-limited/acme --version 0.2.0 --plain-http --username admin --password {password} --untar --untardir pulled"
    Then the command succeeds
    And the file "pulled/acme/Chart.yaml" contains "version: 0.2.0"
