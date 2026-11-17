@pypi @pypi_virtual
Feature: Python clients resolve dependencies through one virtual index
  Scenario Outline: pip and uv install wheel and sdist dependencies from virtual HTML and JSON indexes
    Given the Python remote registry is running with JSON "<json>"
    And the Python client can reach the registry
    And a virtual Python upstream serves dependent wheel and sdist in "<form>"
    And the Python installation verifier is available
    When I PUT "/artifactory/api/repositories/pypi-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"pypi","url":"{upstream}","pyPIRegistryUrl":"{upstream}","pyPIRepositorySuffix":"simple"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    When I deploy the files under "local" to "/artifactory/pypi-local" as admin
    When I PUT "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"pypi","repositories":["pypi-local","pypi-remote"]}
      """
    Then the response status is 200
    When I run python "-m venv --without-pip fresh-env"
    Then the command succeeds
    When I install virtual Python packages with "<client>" into "fresh-env"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run python "check_install.py fresh-env dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run python "check_install.py fresh-env dist/acme_source-1.0.0.tar.gz acme_source 1.0.0"
    Then the command succeeds
    When I run python "check_install.py fresh-env local/acme_private-1.0.0-py3-none-any.whl acme_private 1.0.0"
    Then the command succeeds
    When I GET "/artifactory/api/pypi/pypi-virtual/simple/" as admin
    Then the response status is 200
    And the response body contains "acme-private"
    And the response body contains "acme-smoke"
    When I POST "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"excludesPattern":".pypi/acme-smoke.*"}
      """
    Then the response status is 200
    When I run python "-m venv --without-pip excluded-env"
    Then the command succeeds
    When I install virtual Python packages with "<client>" into "excluded-env"
    Then the command fails

    Examples:
      | client | form | json  |
      | pip    | html | false |
      | uv     | html | false |
      | pip    | json | true  |
      | uv     | json | true  |
      | pip    | html | true  |
      | uv     | html | true  |

  @pypi_virtual_publish
  Scenario Outline: twine and uv publish a wheel and sdist to a virtual deployment target and install back
    Given the registry is running
    And the Python client can reach the registry
    And a Python wheel "acme-publish" at "1.0.0" in "dist"
    And a Python sdist "acme-source" at "1.0.0" in "dist"
    And the Python installation verifier is available
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"pypi","repositories":["pypi-local"],"defaultDeploymentRepo":"pypi-local"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/pypi/pypi-virtual/simple/acme-publish/" as admin
    Then the response status is 404
    When I publish virtual Python distributions with "<publisher>" at ""
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I GET "/artifactory/api/storage/pypi-local/acme-publish/1.0.0/acme_publish-1.0.0-py3-none-any.whl" as admin
    Then the response status is 200
    When I GET "/artifactory/api/storage/pypi-local/acme-source/1.0.0/acme_source-1.0.0.tar.gz" as admin
    Then the response status is 200
    When I run python "-m venv --without-pip published-env"
    Then the command succeeds
    When I install published virtual Python distributions with "<installer>" into "published-env"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run python "check_install.py published-env dist/acme_publish-1.0.0-py3-none-any.whl acme_publish 1.0.0"
    Then the command succeeds
    When I run python "check_install.py published-env dist/acme_source-1.0.0.tar.gz acme_source 1.0.0"
    Then the command succeeds

    Examples:
      | publisher | installer |
      | twine     | pip       |
      | twine     | uv        |
      | uv        | pip       |
      | uv        | uv        |

  @pypi_virtual_publish
  Scenario Outline: virtual Python publication rejects install paths and a missing deployment target
    Given the registry is running
    And the Python client can reach the registry
    And a Python wheel "acme-publish" at "1.0.0" in "dist"
    And a Python sdist "acme-source" at "1.0.0" in "dist"
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"pypi","repositories":["pypi-local"],"defaultDeploymentRepo":"pypi-local"}
      """
    Then the response status is 200
    When I publish virtual Python distributions with "<publisher>" at "simple/"
    Then the command fails
    And the server answered 415 to POST "/artifactory/api/pypi/pypi-virtual/simple/"
    When I publish virtual Python distributions with "<publisher>" at "folder/"
    Then the command fails
    And the server answered 405 to POST "/artifactory/api/pypi/pypi-virtual/folder/"
    When I POST "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"defaultDeploymentRepo":""}
      """
    Then the response status is 200
    When I publish virtual Python distributions with "<publisher>" at ""
    Then the command fails
    And the server answered 405 to POST "/artifactory/api/pypi/pypi-virtual/"
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme-publish/" as admin
    Then the response status is 404

    Examples:
      | publisher |
      | twine     |
      | uv        |

  @pypi_virtual_publish
  Scenario: virtual twine publication hides a deployment permission refusal
    Given the registry is running
    And the Python client can reach the registry
    And a Python wheel "acme-publish" at "1.0.0" in "dist"
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"pypi","repositories":["pypi-local"],"defaultDeploymentRepo":"pypi-local"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/python-reader" as admin with body:
      """
      {"name":"python-reader","email":"reader@example.invalid","password":"Python-reader-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/python-read" as admin with body:
      """
      {"name":"python-read","repositories":["pypi-local"],"includesPattern":"**","principals":{"users":{"python-reader":["r"]}}}
      """
    Then the response status is 200
    When I run twine "upload --non-interactive --repository-url {url}/api/pypi/pypi-virtual/ --username python-reader --password Python-reader-123 dist/acme_publish-1.0.0-py3-none-any.whl"
    Then the command fails
    And the server answered 403 to POST "/artifactory/api/pypi/pypi-virtual/"
    When I POST "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"hideUnauthorizedResources":true}
      """
    Then the response status is 200
    When I run twine "upload --non-interactive --repository-url {url}/api/pypi/pypi-virtual/ --username python-reader --password Python-reader-123 dist/acme_publish-1.0.0-py3-none-any.whl"
    Then the command fails
    And the server answered 404 to POST "/artifactory/api/pypi/pypi-virtual/"
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme-publish/" as admin
    Then the response status is 404
