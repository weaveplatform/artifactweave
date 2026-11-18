@pypi @pypi_remote
Feature: Python clients install through remote Python indexes
  The only client index is artifactweave. Both wheels and source distributions
  install into new environments with client caches disabled during cold retrieval
  and after the upstream has stopped. The fixture is not vendor parity evidence.

  Scenario Outline: pip and uv install from cold and offline remote repositories
    Given the Python remote registry is running with JSON "<json>"
    And the Python client can reach the registry
    And a Python upstream serves a wheel and sdist in "<form>"
    And the Python installation verifier is available
    When I PUT "/artifactory/api/repositories/pypi-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"pypi","url":"{upstream}","pyPIRegistryUrl":"{upstream}","pyPIRepositorySuffix":"simple","offline":false}
      """
    Then the response status is 200
    When I run python "-m venv --without-pip cold-env"
    Then the command succeeds
    When I install the remote Python packages with "<client>" into "cold-env"
    Then the command succeeds
    When I run python "check_install.py cold-env dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run python "check_install.py cold-env dist/acme_source-1.0.0.tar.gz acme_source 1.0.0"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/files/acme_smoke-1.0.0-py3-none-any.whl"
    And the generic upstream received 1 GET requests for "/files/acme_source-1.0.0.tar.gz"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/pypi-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I run python "-m venv --without-pip offline-env"
    Then the command succeeds
    When I install the remote Python packages with "<client>" into "offline-env"
    Then the command succeeds
    When I run python "check_install.py offline-env dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run python "check_install.py offline-env dist/acme_source-1.0.0.tar.gz acme_source 1.0.0"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/files/acme_smoke-1.0.0-py3-none-any.whl"
    And the generic upstream received 1 GET requests for "/files/acme_source-1.0.0.tar.gz"

    Examples:
      | client | form | json  |
      | pip    | html | false |
      | pip    | json | true  |
      | uv     | html | false |
      | uv     | json | true  |

  @pypi_cache_policy
  Scenario Outline: pip cold and offline installs keep source policy distinct from canonical cache paths
    Given the Python remote registry is running with JSON "false"
    And the Python client can reach the registry
    And a Python upstream serves a wheel and sdist in "html"
    And the Python installation verifier is available
    When I PUT "/artifactory/api/repositories/pypi-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"pypi","url":"{upstream}","pyPIRegistryUrl":"{upstream}","pyPIRepositorySuffix":"simple","includesPattern":".pypi/**,files/**"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pypi-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"pypi","repositories":["pypi-remote"]}
      """
    Then the response status is 200
    When I run python "-m venv --without-pip cold-env"
    Then the command succeeds
    When I run pip "--isolated --python cold-env install --retries 0 --no-deps --no-cache-dir --index-url {authurl}/api/pypi/<repository>/simple/ acme-smoke==1.0.0 acme-source==1.0.0"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run python "check_install.py cold-env dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run python "check_install.py cold-env dist/acme_source-1.0.0.tar.gz acme_source 1.0.0"
    Then the command succeeds
    When I GET "/artifactory/api/storage/pypi-remote-cache/acme-smoke/1.0.0/acme_smoke-1.0.0-py3-none-any.whl" as admin
    Then the response status is 200
    And the generic upstream received 1 GET requests for "/files/acme_smoke-1.0.0-py3-none-any.whl"
    And the generic upstream received 1 GET requests for "/files/acme_source-1.0.0.tar.gz"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/pypi-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When I run python "-m venv --without-pip offline-env"
    Then the command succeeds
    When I run pip "--isolated --python offline-env install --retries 0 --no-deps --no-cache-dir --index-url {authurl}/api/pypi/<repository>/simple/ acme-smoke==1.0.0 acme-source==1.0.0"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I run python "check_install.py offline-env dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/files/acme_smoke-1.0.0-py3-none-any.whl"
    And the generic upstream received 1 GET requests for "/files/acme_source-1.0.0.tar.gz"

    Examples:
      | repository   |
      | pypi-remote  |
      | pypi-virtual |
