@opkg
Feature: Native Opkg feeds and client installs
  A controlled IPK built with the OpenWrt archive layout is resolved through
  Packages.gz by the pinned opkg 0.7.0 client.

  Scenario Outline: opkg installs from a <class> feed
    Given the registry is running
    And the Opkg client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"opkg"}
      """
    Then the response status is 200
    Given an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    When I deploy the file "ipk/alpha_1.0_all.ipk" to "/artifactory/<repo>/feed/alpha_1.0_all.ipk" as admin
    Then the response status is 201
    When I GET "/artifactory/<repo>/feed/Packages.gz" as admin until the response status is 200
    Then the response status is 200
    Given the opkg feed is "<repo>"
    When I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0 variant 0"

    Examples:
      | class     | repo       |
      | local     | opkg-local |
      | federated | opkg-fed   |

  Scenario: opkg installs from a remote feed and reuses cached bytes offline
    Given the registry is running
    And the Opkg client can reach the registry
    Given an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    And an Opkg upstream serves "alpha" version "1.0"
    When I PUT "/artifactory/api/repositories/opkg-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"opkg","url":"{upstream}"}
      """
    Then the response status is 200
    Given the opkg feed is "opkg-remote"
    When I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0 variant 0"
    Given the generic upstream stops
    When I clear the opkg installation root
    And I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0 variant 0"

  Scenario: opkg downloads an authentic OpenWrt package from an independent remote feed
    Given the registry is running
    And the Opkg client can reach the registry
    And an independent OpenWrt Opkg upstream is available
    When I PUT "/artifactory/api/repositories/opkg-openwrt" as admin with body:
      """
      {"rclass":"remote","packageType":"opkg","url":"{upstream}"}
      """
    Then the response status is 200
    Given the opkg feed is "opkg-openwrt"
    When I run opkg "update"
    Then the command succeeds
    When I run opkg "download 464xlat"
    Then the command succeeds
    And the file "464xlat_13_x86_64.ipk" exists

  Scenario: opkg follows a same-origin upstream package redirect
    Given the registry is running
    And the Opkg client can reach the registry
    And an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    And an Opkg upstream serves "alpha" version "1.0"
    And the Opkg upstream redirects alpha version "1.0"
    When I PUT "/artifactory/api/repositories/opkg-redirect" as admin with body:
      """
      {"rclass":"remote","packageType":"opkg","url":"{upstream}"}
      """
    Then the response status is 200
    Given the opkg feed is "opkg-redirect"
    When I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0 variant 0"

  Scenario: a metadata refresh discovers a new upstream package
    Given the registry is running
    And the Opkg client can reach the registry
    And an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    And an Opkg upstream serves "alpha" version "1.0"
    When I PUT "/artifactory/api/repositories/opkg-refresh" as admin with body:
      """
      {"rclass":"remote","packageType":"opkg","url":"{upstream}"}
      """
    Then the response status is 200
    Given the opkg feed is "opkg-refresh"
    When I run opkg "update"
    Then the command succeeds
    Given the Opkg upstream adds beta version "1.0"
    When I POST "/artifactory/api/zap/opkg-refresh-cache/feed" as admin with body:
      """
      """
    Then the response status is 200
    When I clear the opkg installation root
    And I run opkg "update"
    Then the command succeeds
    When I run opkg "install beta"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/beta.txt" contains "beta 1.0 variant 0"

  Scenario: an upstream MD5 update cannot override the published SHA256
    Given the registry is running
    And an independent OpenWrt Opkg upstream is available
    And the OpenWrt upstream changes the IPK and its MD5 but not SHA256
    When I PUT "/artifactory/api/repositories/opkg-sha-safe" as admin with body:
      """
      {"rclass":"remote","packageType":"opkg","url":"{upstream}"}
      """
    Then the response status is 200
    When I GET "/artifactory/opkg-sha-safe/feed/464xlat_13_x86_64.ipk" as admin
    Then the response status is 502
    When I GET "/artifactory/opkg-sha-safe-cache/feed/464xlat_13_x86_64.ipk" as admin
    Then the response status is 404

  Scenario: opkg installs from the first member of a virtual feed
    Given the registry is running
    And the Opkg client can reach the registry
    When I PUT "/artifactory/api/repositories/opkg-first" as admin with body:
      """
      {"rclass":"local","packageType":"opkg"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/opkg-second" as admin with body:
      """
      {"rclass":"local","packageType":"opkg"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/opkg-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"opkg","repositories":["opkg-first","opkg-second"]}
      """
    Then the response status is 200
    Given an Opkg package "alpha" version "1.0" variant "0" under "first"
    When I deploy the file "first/alpha_1.0_all.ipk" to "/artifactory/opkg-first/feed/alpha_1.0_all.ipk" as admin
    Then the response status is 201
    When I GET "/artifactory/opkg-first/feed/Packages" as admin until the response status is 200
    Then the response status is 200
    Given an Opkg package "alpha" version "1.0" variant "1" under "second"
    When I deploy the file "second/alpha_1.0_all.ipk" to "/artifactory/opkg-second/feed/alpha_1.0_all.ipk" as admin
    Then the response status is 201
    When I GET "/artifactory/opkg-second/feed/Packages" as admin until the response status is 200
    Then the response status is 200
    Given the opkg feed is "opkg-virtual"
    When I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0 variant 0"
    When I POST "/artifactory/api/repositories/opkg-virtual" as admin with body:
      """
      {"repositories":["opkg-second","opkg-first"]}
      """
    Then the response status is 200
    When I clear the opkg installation root
    And I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds
    And the file "opkg-root/usr/share/artifactweave/alpha.txt" contains "alpha 1.0 variant 1"

  Scenario: delete, restore and explicit reindex rebuild the feed
    Given the registry is running
    And the Opkg client can reach the registry
    When I PUT "/artifactory/api/repositories/opkg-life" as admin with body:
      """
      {"rclass":"local","packageType":"opkg"}
      """
    Then the response status is 200
    Given an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    When I deploy the file "ipk/alpha_1.0_all.ipk" to "/artifactory/opkg-life/feed/alpha_1.0_all.ipk" as admin
    Then the response status is 201
    When I GET "/artifactory/opkg-life/feed/Packages" as admin until the response status is 200
    Then the response status is 200
    When I PUT "/artifactory/api/storage/opkg-life/feed/alpha_1.0_all.ipk?properties=opkg.name=wrong" as admin
    Then the response status is 204
    When I POST "/artifactory/api/opkg/reindex/opkg-life?writeProps=0" as admin
    Then the response status is 200
    When I GET "/artifactory/api/storage/opkg-life/feed/alpha_1.0_all.ipk?properties=opkg.name" as admin
    Then the response body contains "wrong"
    When I POST "/artifactory/api/opkg/reindex/opkg-life" as admin
    Then the response status is 200
    When I GET "/artifactory/api/storage/opkg-life/feed/alpha_1.0_all.ipk?properties=opkg.name" as admin
    Then the response body contains "alpha"
    And the response body does not contain "wrong"
    When I DELETE "/artifactory/opkg-life/feed/alpha_1.0_all.ipk" as admin
    Then the response status is 204
    When I GET "/artifactory/opkg-life/feed/Packages" as admin until the response status is 404
    Then the response status is 404
    When I POST "/artifactory/api/trash/restore/opkg-life/feed/alpha_1.0_all.ipk?to=opkg-life" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I GET "/artifactory/opkg-life/feed/Packages.gz" as admin until the response status is 200
    Then the response status is 200
    Given the opkg feed is "opkg-life"
    When I run opkg "update"
    Then the command succeeds
    When I run opkg "install alpha"
    Then the command succeeds

  Scenario: a virtual feed excludes packages from unreadable members
    Given the registry is running
    When I PUT "/artifactory/api/repositories/opkg-visible" as admin with body:
      """
      {"rclass":"local","packageType":"opkg"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/opkg-hidden" as admin with body:
      """
      {"rclass":"local","packageType":"opkg"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/opkg-filtered" as admin with body:
      """
      {"rclass":"virtual","packageType":"opkg","repositories":["opkg-hidden","opkg-visible"]}
      """
    Then the response status is 200
    Given an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    And an Opkg package "beta" version "1.0" variant "0" under "ipk"
    When I deploy the file "ipk/alpha_1.0_all.ipk" to "/artifactory/opkg-visible/feed/alpha_1.0_all.ipk" as admin
    Then the response status is 201
    When I deploy the file "ipk/beta_1.0_all.ipk" to "/artifactory/opkg-hidden/feed/beta_1.0_all.ipk" as admin
    Then the response status is 201
    When I GET "/artifactory/opkg-visible/feed/Packages" as admin until the response status is 200
    Then the response status is 200
    When I GET "/artifactory/opkg-hidden/feed/Packages" as admin until the response status is 200
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/opkg-reader" as admin with body:
      """
      {"name":"opkg-reader","email":"opkg-reader@example.com","password":"Opkg-pass1"}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/users/opkg-reader/groups" as admin with JSON:
      """
      {"remove":["readers"]}
      """
    Then the response status is 200
    # Virtual repositories cannot be permission targets. A path-scoped ANY
    # grant permits index and alpha reads but never beta's package bytes.
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"opkg-readable-members","resources":{"artifact":{"actions":{"users":{"opkg-reader":["READ"]}},"targets":{"ANY":{"include_patterns":["feed/Packages","feed/alpha_1.0_all.ipk"]}}}}}
      """
    Then the response status is 201
    When I GET "/artifactory/opkg-filtered/feed/Packages" as "opkg-reader" with password "Opkg-pass1"
    Then the response status is 200
    And the response body contains "alpha_1.0_all.ipk"
    And the response body does not contain "beta_1.0_all.ipk"
    When I GET "/artifactory/opkg-filtered/feed/beta_1.0_all.ipk" as "opkg-reader" with password "Opkg-pass1"
    Then the response status is 403

  Scenario: invalid IPK path and remote metadata are refused
    Given the registry is running
    When I PUT "/artifactory/api/repositories/opkg-safe" as admin with body:
      """
      {"rclass":"local","packageType":"opkg"}
      """
    Then the response status is 200
    Given an Opkg package "alpha" version "1.0" variant "0" under "ipk"
    When I deploy the file "ipk/alpha_1.0_all.ipk" to "/artifactory/opkg-safe/feed/wrong_1.0_all.ipk" as admin
    Then the response status is 400
    When I GET "/artifactory/opkg-safe/feed/Packages" as admin
    Then the response status is 404
    And an Opkg upstream serves "alpha" version "1.0"
    When I PUT "/artifactory/api/repositories/opkg-bad" as admin with body:
      """
      {"rclass":"remote","packageType":"opkg","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Opkg upstream publishes an invalid index
    When I GET "/artifactory/opkg-bad/feed/Packages.gz" as admin
    Then the response status is 502
    When I GET "/artifactory/opkg-bad-cache/feed/Packages.gz" as admin
    Then the response status is 404
