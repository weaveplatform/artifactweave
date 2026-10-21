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

  Scenario: twine needs Delete permission to replace changed bytes of the same version
    When I PUT "/artifactory/api/security/users/python-writer" as admin with body:
      """
      {"name":"python-writer","email":"writer@example.invalid","password":"Python-writer-123"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/python-publish" as admin with body:
      """
      {"name":"python-publish","repositories":["pypi-local"],"includesPattern":"**","principals":{"users":{"python-writer":["r","w"]}}}
      """
    Then the response status is 200
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username python-writer --password Python-writer-123 dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username python-writer --password Python-writer-123 dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    Given a Python wheel "acme-smoke" at "1.0.0" in "replacement" with module "GREETING = 'a different build'"
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username python-writer --password Python-writer-123 replacement/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command fails
    And the server answered 403 to POST "/artifactory/api/pypi/pypi-local/"
    When I run pip "download --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ --dest original acme-smoke==1.0.0"
    Then the command succeeds
    And the files "dist/acme_smoke-1.0.0-py3-none-any.whl" and "original/acme_smoke-1.0.0-py3-none-any.whl" are identical
    When I PUT "/artifactory/api/security/permissions/python-publish" as admin with body:
      """
      {"name":"python-publish","repositories":["pypi-local"],"includesPattern":"**","principals":{"users":{"python-writer":["r","w","d"]}}}
      """
    Then the response status is 200
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username python-writer --password Python-writer-123 replacement/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    When I run pip "download --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ --dest replaced acme-smoke==1.0.0"
    Then the command succeeds
    And the files "replacement/acme_smoke-1.0.0-py3-none-any.whl" and "replaced/acme_smoke-1.0.0-py3-none-any.whl" are identical

  Scenario: twine cannot publish below the repository upload root
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/folder/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command fails
    And the server answered 405 to POST "/artifactory/api/pypi/pypi-local/folder/"
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme-smoke/" as admin
    Then the response status is 404

  Scenario: pinned Twine rejects skip-existing for a private repository before uploading
    When I run twine "upload --skip-existing --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command fails
    And the output contains "UnsupportedConfiguration"
    And the output contains "--skip-existing"
    And the server never answered 200 to POST "/artifactory/api/pypi/pypi-local/"
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme-smoke/" as admin
    Then the response status is 404

  @jf
  Scenario: JFrog uv publishes artifact and dependency build records
    Given the JFrog CLI is available
    And the Python installation verifier is available
    And a file "pyproject.toml" with expanded content:
      """
      [project]
      name = "uv-consumer"
      version = "1.0.0"
      requires-python = ">=3.15"
      dependencies = ["acme-smoke==1.0.0"]
      [tool.uv]
      package = false
      [[tool.uv.index]]
      name = "aw"
      url = "{url}/api/pypi/pypi-local/simple/"
      publish-url = "{url}/api/pypi/pypi-local/"
      default = true
      """
    When I run jf Python "config add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf Python "uv publish --index aw dist/acme_smoke-1.0.0-py3-none-any.whl --server-id aw --build-name pypi-uv-publish --build-number 1"
    Then the command succeeds
    When I run jf Python "rt bp pypi-uv-publish 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/pypi-uv-publish/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.artifacts" has 1 entries
    And the response body contains "acme_smoke-1.0.0-py3-none-any.whl"
    When I run jf Python "uv sync --no-dev --server-id aw --build-name pypi-uv-install --build-number 1"
    Then the command succeeds
    When I run python "check_install.py .venv dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run jf Python "rt bp pypi-uv-install 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/pypi-uv-install/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.dependencies" has 1 entries
    And the response body contains "acme-smoke"

  @jf
  Scenario: JFrog Poetry publishes an artifact build record
    Given the JFrog CLI is available
    And a file "pyproject.toml" with content:
      """
      [project]
      name = "acme-smoke"
      version = "1.0.0"
      description = "Acceptance package"
      requires-python = ">=3.15"
      """
    When I run jf Python "config add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf Python "poetry-config --server-id-resolve aw --repo-resolve pypi-local"
    Then the command succeeds
    When I run poetry "config repositories.pypi-local {url}/api/pypi/pypi-local/"
    Then the command succeeds
    When I run jf Python "poetry publish --repository pypi-local --username admin --password {password} --build-name pypi-poetry-publish --build-number 1"
    Then the command succeeds
    When I run jf Python "rt bp pypi-poetry-publish 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/pypi-poetry-publish/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.artifacts" has 1 entries
    And the response body contains "acme_smoke-1.0.0-py3-none-any.whl"
