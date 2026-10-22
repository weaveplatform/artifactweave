@remote @helm @helm_remote
Feature: Classic Helm remote repositories preserve upstream indexes and signatures
  Classic chart/provenance proxying is qualified independently from Helm OCI.

  Scenario Outline: a signed chart resolves through a cold remote and a fresh client during outage <relative>
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
      {"rclass":"remote","packageType":"helm","url":"{upstream}","hardFail":true}
      """
    Then the response status is 200
    When I run helm "repo add aw {url}/api/helm/helm-remote --username admin --password {password}"
    Then the command succeeds
    When I run helm "repo update aw"
    Then the command succeeds
    When I run helm "pull aw/acme --version 0.1.0 --verify --keyring signing/public.gpg"
    Then the command succeeds
    And the files "charts/acme-0.1.0.tgz" and "acme-0.1.0.tgz" are identical
    And the files "charts/acme-0.1.0.tgz.prov" and "acme-0.1.0.tgz.prov" are identical
    When I run helm "verify acme-0.1.0.tgz --keyring signing/public.gpg"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/acme-0.1.0.tgz"
    And the generic upstream received 1 GET requests for "/acme-0.1.0.tgz.prov"
    When I POST "/artifactory/api/repositories/helm-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    And the Helm client metadata cache is cleared
    When I run helm "repo update aw"
    Then the command succeeds
    When I run helm "pull aw/acme --version 0.1.0 --verify --keyring signing/public.gpg"
    Then the command succeeds
    And the files "charts/acme-0.1.0.tgz" and "acme-0.1.0.tgz" are identical
    And the files "charts/acme-0.1.0.tgz.prov" and "acme-0.1.0.tgz.prov" are identical
    When I run helm "verify acme-0.1.0.tgz --keyring signing/public.gpg"
    Then the command succeeds

    Examples:
      | relative |
      | true     |
      | false    |

  Scenario: Helm rejects an invalid cryptographic signature in preserved provenance
    Given the registry is running
    And helm is available
    When I run helm "create acme"
    Then the command succeeds
    Given a Helm signing keyring in "signing"
    When I run helm "package acme --destination charts --sign --key artifactweave --keyring signing/private.gpg"
    Then the command succeeds
    When I run helm "repo index charts"
    Then the command succeeds
    Given the Helm provenance "charts/acme-0.1.0.tgz.prov" has a tampered signed description
    And a Helm upstream serves the files under "charts"
    When I PUT "/artifactory/api/repositories/helm-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"helm","url":"{upstream}"}
      """
    Then the response status is 200
    When I run helm "repo add aw {url}/api/helm/helm-remote --username admin --password {password}"
    Then the command succeeds
    When I run helm "pull aw/acme --version 0.1.0 --verify --keyring signing/public.gpg"
    Then the command fails
    And the generic upstream received 1 GET requests for "/acme-0.1.0.tgz.prov"
