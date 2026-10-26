@conda
Feature: Native Conda channels resolve packages through the current micromamba client
  The pinned micromamba client reads platform and noarch repodata, verifies
  package hashes, and installs from the repository classes Artifactory supports.

  Scenario Outline: micromamba installs from a <class> channel
    Given the registry is running
    And the Conda client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"conda"}
      """
    Then the response status is 200
    Given a controlled Conda package under "conda"
    When I deploy the file "conda/artifactweave-conda-fixture-1.0.0-0.tar.bz2" to "/artifactory/<repo>/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2" as admin
    Then the response status is 201
    When I GET "/artifactory/api/conda/<repo>/noarch/repodata.json" as admin until the response status is 200
    Then the response body contains "artifactweave-conda-fixture-1.0.0-0.tar.bz2"
    Given the Conda channel is "<repo>"
    When I run micromamba "create --yes --prefix /work/conda-env --channel {conda_channel} --override-channels artifactweave-conda-fixture=1.0.0"
    Then the command succeeds
    And the file "conda-env/share/artifactweave-conda-fixture/hello.txt" contains "ArtifactWeave Conda fixture"

    Examples:
      | class     | repo        |
      | local     | conda-local |
      | federated | conda-fed   |

  Scenario: micromamba installs a modern .conda package
    Given the registry is running
    And the Conda client can reach the registry
    When I PUT "/artifactory/api/repositories/conda-modern" as admin with body:
      """
      {"rclass":"local","packageType":"conda"}
      """
    Then the response status is 200
    Given a current Conda package under "conda"
    When I deploy the file "conda/ca-certificates-2026.7.22-hbd8a1cb_0.conda" to "/artifactory/conda-modern/noarch/ca-certificates-2026.7.22-hbd8a1cb_0.conda" as admin
    Then the response status is 201
    Given the Conda channel is "conda-modern"
    When I run micromamba "create --yes --prefix /work/conda-env --channel {conda_channel} --override-channels ca-certificates=2026.7.22"
    Then the command succeeds

  Scenario: micromamba installs through a virtual channel in member order
    Given the registry is running
    And the Conda client can reach the registry
    When I PUT "/artifactory/api/repositories/conda-member" as admin with body:
      """
      {"rclass":"local","packageType":"conda"}
      """
    Then the response status is 200
    Given a controlled Conda package under "conda"
    When I deploy the file "conda/artifactweave-conda-fixture-1.0.0-0.tar.bz2" to "/artifactory/conda-member/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/conda-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"conda","repositories":["conda-member"]}
      """
    Then the response status is 200
    Given the Conda channel is "conda-virtual"
    When I run micromamba "create --yes --prefix /work/conda-env --channel {conda_channel} --override-channels artifactweave-conda-fixture=1.0.0"
    Then the command succeeds

  Scenario: micromamba installs through the remote cache after the upstream stops
    Given the registry is running
    And the Conda client can reach the registry
    And a Conda upstream serves the controlled package
    And the Conda upstream redirects the package
    When I PUT "/artifactory/api/repositories/conda-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"conda","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Conda channel is "conda-remote"
    When I run micromamba "create --yes --prefix /work/conda-env --channel {conda_channel} --override-channels artifactweave-conda-fixture=1.0.0"
    Then the command succeeds
    Given the generic upstream stops
    And I clear the Conda client cache
    When I run micromamba "create --yes --prefix /work/conda-env-offline --channel {conda_channel} --override-channels artifactweave-conda-fixture=1.0.0"
    Then the command succeeds

  Scenario: delete, restore and explicit reindex preserve the native channel
    Given the registry is running
    And the Conda client can reach the registry
    When I PUT "/artifactory/api/repositories/conda-life" as admin with body:
      """
      {"rclass":"local","packageType":"conda"}
      """
    Then the response status is 200
    Given a controlled Conda package under "conda"
    When I deploy the file "conda/artifactweave-conda-fixture-1.0.0-0.tar.bz2" to "/artifactory/conda-life/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2" as admin
    Then the response status is 201
    When I POST "/artifactory/api/conda/conda-life/reindex" as admin
    Then the response status is 200
    When I GET "/artifactory/api/conda/conda-life/noarch/repodata.json" as admin
    Then the response body contains "artifactweave-conda-fixture-1.0.0-0.tar.bz2"
    When I DELETE "/artifactory/conda-life/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2" as admin
    Then the response status is 204
    When I GET "/artifactory/api/conda/conda-life/noarch/repodata.json" as admin
    Then the response body does not contain "artifactweave-conda-fixture-1.0.0-0.tar.bz2"
    When I POST "/artifactory/api/trash/restore/conda-life/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2?to=conda-life" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/conda/conda-life/reindex" as admin
    Then the response status is 200
    Given the Conda channel is "conda-life"
    When I run micromamba "create --yes --prefix /work/conda-env --channel {conda_channel} --override-channels artifactweave-conda-fixture=1.0.0"
    Then the command succeeds

  Scenario: remote repodata refresh discovers a new modern archive
    Given the registry is running
    And the Conda client can reach the registry
    And a Conda upstream serves the controlled package
    When I PUT "/artifactory/api/repositories/conda-refresh" as admin with body:
      """
      {"rclass":"remote","packageType":"conda","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Conda channel is "conda-refresh"
    When I run micromamba "create --yes --prefix /work/conda-env --channel {conda_channel} --override-channels artifactweave-conda-fixture=1.0.0"
    Then the command succeeds
    Given the Conda upstream adds a modern package
    When I POST "/artifactory/api/zap/conda-refresh-cache/noarch" as admin with body:
      """
      """
    Then the response status is 200
    When I clear the Conda client cache
    And I run micromamba "create --yes --prefix /work/conda-env-modern --channel {conda_channel} --override-channels ca-certificates=2026.7.22"
    Then the command succeeds

  Scenario: a remote package that disagrees with repodata is refused
    Given the registry is running
    And a Conda upstream serves the controlled package
    And the Conda upstream corrupts the package
    When I PUT "/artifactory/api/repositories/conda-bad" as admin with body:
      """
      {"rclass":"remote","packageType":"conda","url":"{upstream}"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/conda/conda-bad/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2" as admin
    Then the response status is 502
