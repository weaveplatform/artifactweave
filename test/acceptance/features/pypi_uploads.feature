@pypi
Feature: PyPI clients publish distributions and report builds
  Legacy multipart uploads use content metadata for their destination and
  leave packages immediately available to pip's simple-index reads.

  Background:
    Given the registry is running
    And the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    Given a Python wheel "acme-smoke" at "1.0.0" in "dist"

  Scenario: twine publishes and repeats an identical wheel
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    When I GET "/artifactory/pypi-local/acme-smoke/1.0.0/acme_smoke-1.0.0-py3-none-any.whl" as admin
    Then the response status is 200
    When I run pip "download --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ --dest downloaded acme-smoke==1.0.0"
    Then the command succeeds
    And the files "dist/acme_smoke-1.0.0-py3-none-any.whl" and "downloaded/acme_smoke-1.0.0-py3-none-any.whl" are identical

  @jf
  Scenario: JFrog twine and pip publish artifact and dependency build records
    Given the JFrog CLI is available
    When I run jf Python "config add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf Python "pip-config --server-id-resolve aw --server-id-deploy aw --repo-resolve pypi-local --repo-deploy pypi-local"
    Then the command succeeds
    When I run jf Python "twine upload dist/acme_smoke-1.0.0-py3-none-any.whl --build-name pypi-publish --build-number 1"
    Then the command succeeds
    When I run jf Python "rt bp pypi-publish 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/pypi-publish/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.artifacts" has 1 entries
    And the response body contains "acme_smoke-1.0.0-py3-none-any.whl"
    When I run jf Python "pip install --user --no-deps --no-cache-dir acme-smoke==1.0.0 --build-name pypi-install --build-number 1"
    Then the command succeeds
    When I run jf Python "rt bp pypi-install 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/pypi-install/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.dependencies" has 1 entries
    And the JSON field "buildInfo.modules.0.dependencies.0.id" equals "acme-smoke:1.0.0"
