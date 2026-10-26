@cran
Feature: Native CRAN package installation
  R 4.6.1 installs unchanged published CRAN source bytes through the
  repository's PACKAGES metadata and source archive paths.

  Scenario Outline: R installs from a <class> CRAN repository
    Given the registry is running
    And the R client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"cran"}
      """
    Then the response status is 200
    Given the published assertthat CRAN source is under "cran"
    When I deploy the file "cran/assertthat_0.2.1.tar.gz" to "/artifactory/<repo>/src/contrib/assertthat_0.2.1.tar.gz" as admin
    Then the response status is 201
    When I GET "/artifactory/<repo>/src/contrib/PACKAGES" as admin until the response status is 200
    Then the response body contains "Package: assertthat"
    When I install assertthat from CRAN repository "<repo>" with R
    Then the command succeeds
    And the server answered 200 to GET "/artifactory/<repo>/src/contrib/assertthat_0.2.1.tar.gz"

    Examples:
      | class     | repo       |
      | local     | cran-local |
      | federated | cran-fed   |

  Scenario: R installs from a remote CRAN mirror and reuses cached bytes offline
    Given the registry is running
    And the R client can reach the registry
    And the published assertthat CRAN upstream is available
    When I PUT "/artifactory/api/repositories/cran-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"cran","url":"{upstream}"}
      """
    Then the response status is 200
    When I install assertthat from CRAN repository "cran-remote" with R
    Then the command succeeds
    Given the generic upstream stops
    When I clear the R library
    And I install assertthat from CRAN repository "cran-remote" with R
    Then the command succeeds

  Scenario: R installs through the first readable CRAN virtual member
    Given the registry is running
    And the R client can reach the registry
    When I PUT "/artifactory/api/repositories/cran-member" as admin with body:
      """
      {"rclass":"local","packageType":"cran"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/cran-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"cran","repositories":["cran-member"]}
      """
    Then the response status is 200
    Given the published assertthat CRAN source is under "cran"
    When I deploy the file "cran/assertthat_0.2.1.tar.gz" to "/artifactory/cran-member/src/contrib/assertthat_0.2.1.tar.gz" as admin
    Then the response status is 201
    When I GET "/artifactory/cran-member/src/contrib/PACKAGES" as admin until the response status is 200
    Then the response body contains "Package: assertthat"
    When I install assertthat from CRAN repository "cran-virtual" with R
    Then the command succeeds
    And the server answered 200 to GET "/artifactory/cran-virtual/src/contrib/assertthat_0.2.1.tar.gz"

  Scenario: R installs an archive fetched through a same-origin CRAN redirect
    Given the registry is running
    And the R client can reach the registry
    And the published assertthat CRAN upstream is available
    And the CRAN upstream redirects its source archive
    When I PUT "/artifactory/api/repositories/cran-redirect" as admin with body:
      """
      {"rclass":"remote","packageType":"cran","url":"{upstream}"}
      """
    Then the response status is 200
    When I install assertthat from CRAN repository "cran-redirect" with R
    Then the command succeeds
    And the generic upstream received 1 GET requests for "/src/contrib/assertthat_0.2.1.tar.gz"
    And the generic upstream received 1 GET requests for "/mirrors/assertthat_0.2.1.tar.gz"

  Scenario: malformed remote CRAN metadata is not published
    Given the registry is running
    And the published assertthat CRAN upstream is available
    And the CRAN upstream corrupts its index
    When I PUT "/artifactory/api/repositories/cran-corrupt" as admin with body:
      """
      {"rclass":"remote","packageType":"cran","url":"{upstream}"}
      """
    Then the response status is 200
    When I GET "/artifactory/cran-corrupt/src/contrib/PACKAGES" as admin
    Then the response status is 502
