@pypi
Feature: PyPI clients install the exact published distributions
  Wheels and source distributions install into fresh virtual environments
  through both negotiated forms of the simple index.

  Scenario Outline: pip installs and hash-checks a wheel and a source distribution
    Given the registry is running with environment "ARTIFACTWEAVE_PYPI_SIMPLE_JSON=<json>"
    And the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    Given a Python wheel "acme-smoke" at "1.0.0" in "dist"
    And a Python sdist "acme-source" at "1.0.0" in "dist"
    And the Python installation verifier is available
    And a requirements file "wheel.txt" for "acme-smoke==1.0.0" matching "dist/acme_smoke-1.0.0-py3-none-any.whl"
    And a requirements file "source.txt" for "acme-source==1.0.0" matching "dist/acme_source-1.0.0.tar.gz"
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl dist/acme_source-1.0.0.tar.gz"
    Then the command succeeds
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl dist/acme_source-1.0.0.tar.gz"
    Then the command succeeds
    Given the next request has header "Accept" "application/vnd.pypi.simple.v1+json, text/html;q=0.1"
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme-smoke/" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "<type>"
    Given a file ".config/pip/pip.conf" with expanded content:
      """
      [global]
      index-url = {authurl}/api/pypi/pypi-local/simple/
      """
    When I run pip "download --require-hashes --no-deps --no-cache-dir --dest downloaded -r wheel.txt -r source.txt"
    Then the command succeeds
    And the files "dist/acme_smoke-1.0.0-py3-none-any.whl" and "downloaded/acme_smoke-1.0.0-py3-none-any.whl" are identical
    And the files "dist/acme_source-1.0.0.tar.gz" and "downloaded/acme_source-1.0.0.tar.gz" are identical
    When I run python "-m venv --without-pip venv"
    Then the command succeeds
    When I run pip "--python venv install --no-deps --no-cache-dir acme-smoke==1.0.0 acme-source==1.0.0"
    Then the command succeeds
    When I run python "check_install.py venv downloaded/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run python "check_install.py venv downloaded/acme_source-1.0.0.tar.gz acme_source 1.0.0"
    Then the command succeeds
    Given a file "wrong-hash.txt" with content:
      """
      acme-smoke==1.0.0 --hash=sha256:0000000000000000000000000000000000000000000000000000000000000000
      """
    When I run pip "download --require-hashes --no-deps --no-cache-dir --dest rejected -r wrong-hash.txt"
    Then the command fails
    And the output contains "DO NOT MATCH THE HASHES"

    Examples:
      | json  | type                                |
      | false | text/html                           |
      | true  | application/vnd.pypi.simple.v1+json |

  Scenario: pip skips a yanked version except for an exact pin and resolves after delete and reindex
    Given the registry is running
    And the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    Given a Python wheel "acme-smoke" at "1.0.0" in "dist"
    And a Python wheel "acme-smoke" at "2.0.0" in "dist"
    And the Python installation verifier is available
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme_smoke-1.0.0-py3-none-any.whl dist/acme_smoke-2.0.0-py3-none-any.whl"
    Then the command succeeds
    When I PUT "/artifactory/api/storage/pypi-local/acme-smoke/2.0.0/acme_smoke-2.0.0-py3-none-any.whl?properties=pypi.yanked=broken" as admin
    Then the response status is 204
    When I run python "-m venv --without-pip venv"
    Then the command succeeds
    When I run pip "--python venv install --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ acme-smoke"
    Then the command succeeds
    When I run python "check_install.py venv dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
    When I run pip "--python venv install --force-reinstall --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ acme-smoke==2.0.0"
    Then the command succeeds
    And the output contains "yanked"
    When I run python "check_install.py venv dist/acme_smoke-2.0.0-py3-none-any.whl acme_smoke 2.0.0"
    Then the command succeeds
    When I GET "/artifactory/api/pypi/pypi-local/pypi/acme-smoke/json" as admin
    Then the response status is 200
    And the JSON field "info.version" equals "1.0.0"
    And the JSON field "urls.0.filename" equals "acme_smoke-1.0.0-py3-none-any.whl"
    When I GET "/artifactory/api/pypi/pypi-local/pypi/acme-smoke/2.0.0/json" as admin
    Then the response status is 200
    And the JSON field "info.version" equals "2.0.0"
    And the JSON field "urls.0.yanked_reason" equals "broken"
    When I DELETE "/artifactory/pypi-local/acme-smoke/2.0.0/acme_smoke-2.0.0-py3-none-any.whl" as admin
    Then the response status is 204
    When I GET "/artifactory/api/pypi/pypi-local/pypi/acme-smoke/2.0.0/json" as admin
    Then the response status is 404
    When I POST "/artifactory/api/pypi/pypi-local/reindex" as admin
    Then the response status is 200
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme-smoke/" as admin
    Then the response status is 200
    And the response body contains "acme_smoke-1.0.0-py3-none-any.whl"
    And the response body does not contain "acme_smoke-2.0.0-py3-none-any.whl"
    When I run pip "--python venv install --force-reinstall --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-local/simple/ acme-smoke"
    Then the command succeeds
    When I run python "check_install.py venv dist/acme_smoke-1.0.0-py3-none-any.whl acme_smoke 1.0.0"
    Then the command succeeds
