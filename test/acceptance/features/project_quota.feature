@project-quota
Feature: Project storage quota notifications
  A committed 75 percent transition delivers to a current project administrator.
  Delivery retries retain the occurrence identity, and falling below the
  threshold rearms a different occurrence. Disabled notifications send nothing.

  Scenario: Native quota warnings retry, rearm and respect disabled notifications
    Given the registry is running
    And a loopback quota mail receiver loses the first acknowledgement
    When I PUT "/artifactory/api/security/users/quota-owner" as admin with body:
      """
      {"email":"quota-owner@example.invalid","password":"quota-owner-password","admin":false}
      """
    Then the response status is 201
    When I POST "/access/api/v1/projects" as admin with body:
      """
      {"project_key":"alpha","display_name":"Alpha","storage_quota_bytes":100,"storage_quota_email_notification":true}
      """
    Then the response status is 201
    When I PUT "/access/api/v1/projects/alpha/users/quota-owner" as admin with body:
      """
      {"roles":["Project Admin"]}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/alpha-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic","projectKey":"alpha"}
      """
    Then the response status is 200
    When I upload 75 quota bytes to "/artifactory/alpha-generic/item.bin"
    Then the quota warning is retried with the same identity for "alpha"
    When I upload 80 quota bytes to "/artifactory/alpha-generic/item.bin"
    Then no additional quota mail arrives
    When I DELETE "/artifactory/alpha-generic/item.bin" as admin
    Then the response status is 204
    When I upload 75 quota bytes to "/artifactory/alpha-generic/item.bin"
    Then a new quota warning arrives for "alpha"
    When I PUT "/access/api/v1/projects/alpha" as admin with body:
      """
      {"storage_quota_email_notification":false}
      """
    Then the response status is 200
    When I DELETE "/artifactory/alpha-generic/item.bin" as admin
    Then the response status is 204
    When I upload 75 quota bytes to "/artifactory/alpha-generic/item.bin"
    Then no additional quota mail arrives
    When I GET "/artifactory/alpha-generic/item.bin" as admin
    Then the response status is 200
