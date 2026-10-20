@pypi
Feature: PyPI simple indexes resolve native pip downloads
  A wheel deployed through item paths is immediately installable through
  the PyPI protocol, with either HTML or opt-in JSON negotiation.

  Scenario Outline: pip downloads a wheel through the simple index
    Given the registry is running with environment "ARTIFACTWEAVE_PYPI_SIMPLE_JSON=<json>"
    And the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    Given a Python wheel "acme-smoke" at "1.0.0" in "dist"
    When I deploy the file "dist/acme_smoke-1.0.0-py3-none-any.whl" to "/artifactory/pypi-local/uploads/acme_smoke-1.0.0-py3-none-any.whl" as admin
    Then the response status is 201
    Given a file ".config/pip/pip.conf" with expanded content:
      """
      [global]
      index-url = {authurl}/api/pypi/pypi-local/simple/
      """
    When I run pip "download --no-deps --no-cache-dir --dest downloaded acme-smoke==1.0.0"
    Then the command succeeds
    And the files "dist/acme_smoke-1.0.0-py3-none-any.whl" and "downloaded/acme_smoke-1.0.0-py3-none-any.whl" are identical

    Examples:
      | json  |
      | false |
      | true  |
