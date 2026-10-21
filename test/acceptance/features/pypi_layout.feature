@pypi
Feature: PyPI layout enforcement reports incompatible distribution names
  The optional Artifactory layout policy compares file names to content metadata.

  Scenario: twine rejects a renamed wheel with the enforce-layout diagnostic
    Given the registry is running with environment "ARTIFACTWEAVE_PYPI_ENFORCE_LAYOUT=true"
    And the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-local" as admin with body:
      """
      {"rclass":"local","packageType":"pypi"}
      """
    Then the response status is 200
    Given a Python wheel "acme" at "1.0.0" in "dist"
    And a copy of "dist/acme-1.0.0-py3-none-any.whl" at "renamed/other-1.0.0-py3-none-any.whl"
    When I run twine "upload --verbose --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} renamed/other-1.0.0-py3-none-any.whl"
    Then the command fails
    And the server answered 403 to POST "/artifactory/api/pypi/pypi-local/"
    And the output contains "Action prevented due to Enforce Layout policy"
    And the output contains "other-1.0.0-py3-none-any.whl"
    And the output contains "acme/1.0.0"
    When I GET "/artifactory/api/pypi/pypi-local/simple/acme/" as admin
    Then the response status is 404
    When I run twine "upload --repository-url {url}/api/pypi/pypi-local/ --username admin --password {password} dist/acme-1.0.0-py3-none-any.whl"
    Then the command succeeds
