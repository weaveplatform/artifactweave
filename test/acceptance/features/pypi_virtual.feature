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
