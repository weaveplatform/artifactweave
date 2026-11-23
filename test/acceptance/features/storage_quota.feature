@global-storage-quota
Feature: Global storage quota
  quotaConfig is measured against the volume that holds the hot blob store.
  At the limit new deployments are refused with 7.x's 413 while reads and
  deletions go on; at the warning level administrators are mailed once per
  crossing, with retries and restarts keeping the occurrence's identity.

  Background:
    Given the registry is running
    And a loopback quota mail receiver loses the first acknowledgement
    When I PUT "/artifactory/api/security/users/storage-admin" as admin with body:
      """
      {"email":"storage-admin@example.invalid","password":"storage-admin-password","admin":true}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/users/storage-reader" as admin with body:
      """
      {"email":"storage-reader@example.invalid","password":"storage-reader-password","admin":false}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/quota-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    When I upload 10 quota bytes to "/artifactory/quota-generic/kept.bin"

  Scenario: The limit refuses deployments only, and the monitor reports it once
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        enabled: true
        diskSpaceLimitPercentage: 1
        diskSpaceWarningPercentage: 1
      """
    Then the response status is 200
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response status is 200
    And the XML field "quotaConfig/enabled" equals "true"
    And the XML field "quotaConfig/diskSpaceLimitPercentage" equals "1"
    And the XML field "quotaConfig/diskSpaceWarningPercentage" equals "1"
    When I upload 10 bytes to "/artifactory/quota-generic/refused.bin" and the disk quota refuses it
    And I GET "/artifactory/quota-generic/refused.bin" as admin
    Then the response status is 404
    When I GET "/artifactory/quota-generic/kept.bin" as admin
    Then the response status is 200
    And no additional quota mail arrives
    When the registry process restarts over its data
    Then the storage quota limit is retried with the same identity
    # A stop that cuts a delivery short after the server's acknowledgement
    # hands the occurrence back, and the next start may resend that same
    # message; settle the delivery before stopping again.
    And no additional quota mail arrives
    When the registry process restarts over its data
    Then no additional quota mail arrives
    When I DELETE "/artifactory/quota-generic/kept.bin" as admin
    Then the response status is 204
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        diskSpaceLimitPercentage: 100
      """
    Then the response status is 200
    When I upload 10 quota bytes to "/artifactory/quota-generic/kept.bin"
    Then no additional quota mail arrives
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        enabled: false
        diskSpaceLimitPercentage: 1
      """
    Then the response status is 200
    When I upload 10 quota bytes to "/artifactory/quota-generic/after.bin"
    Then no additional quota mail arrives

  Scenario: A warning retries, survives a restart and rearms below the threshold
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        enabled: true
        diskSpaceLimitPercentage: 100
        diskSpaceWarningPercentage: 1
      """
    Then the response status is 200
    When I upload 10 quota bytes to "/artifactory/quota-generic/first.bin"
    Then the storage quota warning is retried with the same identity
    When I upload 10 quota bytes to "/artifactory/quota-generic/second.bin"
    Then no additional quota mail arrives
    When the registry process restarts over its data
    Then no additional quota mail arrives
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        diskSpaceWarningPercentage: 100
      """
    Then the response status is 200
    When I upload 10 quota bytes to "/artifactory/quota-generic/below.bin"
    And I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        diskSpaceWarningPercentage: 1
      """
    Then the response status is 200
    When I upload 10 quota bytes to "/artifactory/quota-generic/rearmed.bin"
    Then a storage quota warning mail arrives with a new identity

  Scenario: The global limit and a project quota each refuse on their own
    When I POST "/access/api/v1/projects" as admin with body:
      """
      {"project_key":"alpha","display_name":"Alpha","storage_quota_bytes":100}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/alpha-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic","projectKey":"alpha"}
      """
    Then the response status is 200
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        enabled: true
        diskSpaceLimitPercentage: 1
      """
    Then the response status is 200
    When I upload 10 bytes to "/artifactory/alpha-generic/global.bin" and the disk quota refuses it
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        enabled: false
      """
    Then the response status is 200
    When I PUT "/artifactory/alpha-generic/project.bin" as admin with body:
      """
      0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789X
      """
    Then the response status is 403
    When I upload 100 quota bytes to "/artifactory/alpha-generic/project.bin"

  Scenario: Enabling the quota needs whole percentages
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      quotaConfig:
        enabled: true
        diskSpaceLimitPercentage: 101
      """
    Then the response status is 400
    And the response body contains "quotaConfig.diskSpaceLimitPercentage must be a whole percentage from 0 to 100"
