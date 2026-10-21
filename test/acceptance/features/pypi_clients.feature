@pypi
Feature: uv and Poetry use both forms of the Python simple index
  Each client publishes a distribution and installs its exact bytes into a fresh environment.

  Scenario Outline: uv and Poetry publish and install through the negotiated simple index
    Given the registry is running with environment "ARTIFACTWEAVE_PYPI_SIMPLE_JSON=<json>"
    And the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    Given a Python wheel "acme-smoke" at "1.0.0" in "dist"
    And the Python installation verifier is available
    When I run uv "publish --publish-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    When I run python "-m venv --without-pip uv-env"
    Then the command succeeds
    When I run uv "pip install --python uv-env/bin/python --no-deps --no-cache --index-url {authurl}/api/pypi/pypi-local/simple/ acme-smoke==1.0.0"
    Then the command succeeds
    When I run python "check_install.py uv-env dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds

    When I DELETE "/artifactory/pypi-local/acme-smoke/1.0.0/acme_smoke-1.0.0-py3-none-any.whl" as admin
    Then the response status is 204
    Given a file "pyproject.toml" with content:
      """
      [project]
      name = "acme-smoke"
      version = "1.0.0"
      description = "Acceptance package"
      authors = [{name = "Artifactweave"}]
      requires-python = ">=3.8"
      """
    When I run poetry "config repositories.local {url}/api/pypi/pypi-local/"
    Then the command succeeds
    When I run poetry "publish --repository local --username admin --password {password}"
    Then the command succeeds
    When I run pip "download --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ --dest downloaded acme-smoke==1.0.0"
    Then the command succeeds
    And the files "dist/acme_smoke-1.0.0-py3-none-any.whl" and "downloaded/acme_smoke-1.0.0-py3-none-any.whl" are identical

    Given a file "pyproject.toml" with expanded content:
      """
      [project]
      name = "poetry-consumer"
      version = "1.0.0"
      requires-python = ">=3.15"
      [tool.poetry]
      package-mode = false
      [[tool.poetry.source]]
      name = "local"
      url = "{authurl}/api/pypi/pypi-local/simple/"
      priority = "primary"
      """
    When I run poetry "add --source local acme-smoke==1.0.0"
    Then the command succeeds
    When I run python "check_install.py .venv dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds

    Examples:
      | json  |
      | false |
      | true  |
