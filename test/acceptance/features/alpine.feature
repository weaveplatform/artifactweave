@alpine
Feature: Native Alpine repository indexes and apk installs
  A signed APK and index are served at the documented branch, component and
  architecture paths. apk verifies both against the repository's public key.

  Scenario Outline: apk installs from a signed <class> repository
    Given the registry is running
    And the Alpine client can reach the registry
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"alpine","primaryKeyPairRef":"apk-sign"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/security/keypair/public/repositories/<repo>" as admin
    Then the response status is 200
    And the response body contains "BEGIN PUBLIC KEY"
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    When I deploy the file "apk/hello-1.0-r0.apk" to "/artifactory/<repo>/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 201
    When I GET "/artifactory/<repo>/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 200
    Then the response status is 200
    Given the apk repository is "<repo>"
    When I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"

    Examples:
      | class     | repo        |
      | local     | apk-local   |
      | federated | apk-fed     |

  Scenario: apk installs through a remote cache and reuses it offline
    Given the registry is running
    And the Alpine client can reach the registry
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    And an Alpine upstream serves "hello" at "1.0-r0"
    When I PUT "/artifactory/api/repositories/apk-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"alpine","url":"{upstream}"}
      """
    Then the response status is 200
    Given the apk repository is "apk-remote"
    When I GET "/artifactory/apk-remote/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 200
    When I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"
    When I GET "/artifactory/apk-remote-cache/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 200
    Given the generic upstream stops
    When I clear the apk installation root
    And I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"

  Scenario: apk installs a package fetched through a same-origin redirect
    Given the registry is running
    And the Alpine client can reach the registry
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    And an Alpine upstream serves "hello" at "1.0-r0"
    And the Alpine upstream redirects "hello" at "1.0-r0"
    When I PUT "/artifactory/api/repositories/apk-redirect" as admin with body:
      """
      {"rclass":"remote","packageType":"alpine","url":"{upstream}"}
      """
    Then the response status is 200
    Given the apk repository is "apk-redirect"
    When I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"
    And the generic upstream received 1 GET requests for "/v3.20/main/x86_64/hello-1.0-r0.apk"
    And the generic upstream received 1 GET requests for "/mirrors/hello-1.0-r0.apk"

  Scenario: invalid remote index and package bytes never enter the cache
    Given the registry is running
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    And an Alpine upstream serves "hello" at "1.0-r0"
    When I PUT "/artifactory/api/repositories/apk-bad-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"alpine","url":"{upstream}"}
      """
    Then the response status is 200
    Given the Alpine upstream publishes an invalid index
    When I GET "/artifactory/apk-bad-remote/v3.20/main/x86_64/APKINDEX.tar.gz" as admin
    Then the response status is 502
    When I GET "/artifactory/apk-bad-remote-cache/v3.20/main/x86_64/APKINDEX.tar.gz" as admin
    Then the response status is 404
    Given the Alpine upstream corrupts "hello" at "1.0-r0"
    When I GET "/artifactory/apk-bad-remote/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 502
    When I GET "/artifactory/apk-bad-remote-cache/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 404

  Scenario: apk installs through an ordered signed virtual repository
    Given the registry is running
    And the Alpine client can reach the registry
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/apk-first" as admin with body:
      """
      {"rclass":"local","packageType":"alpine","primaryKeyPairRef":"apk-sign"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/apk-second" as admin with body:
      """
      {"rclass":"local","packageType":"alpine","primaryKeyPairRef":"apk-sign"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/apk-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"alpine","repositories":["apk-first","apk-second"],"keyPair":"apk-sign"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/security/keypair/public/repositories/apk-virtual" as admin
    Then the response status is 200
    And the response body contains "BEGIN PUBLIC KEY"
    Given a signed Alpine package "hello" at "1.0-r0" with message "first member" under "apk-first"
    When I deploy the file "apk-first/hello-1.0-r0.apk" to "/artifactory/apk-first/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 201
    When I GET "/artifactory/apk-first/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 200
    Then the response status is 200
    Given a signed Alpine package "hello" at "1.0-r0" with message "second member" under "apk-second"
    When I deploy the file "apk-second/hello-1.0-r0.apk" to "/artifactory/apk-second/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 201
    When I GET "/artifactory/apk-second/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 200
    Then the response status is 200
    When I GET "/artifactory/apk-virtual/v3.20/main/x86_64/APKINDEX.tar.gz" as admin
    Then the response status is 200
    Given the apk repository is "apk-virtual"
    When I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "first member"
    When I POST "/artifactory/api/repositories/apk-virtual" as admin with body:
      """
      {"repositories":["apk-second","apk-first"]}
      """
    Then the response status is 200
    When I clear the apk installation root
    And I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "second member"

  Scenario: apk installs a remote member through a signed virtual repository offline
    Given the registry is running
    And the Alpine client can reach the registry
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    And an Alpine upstream serves "hello" at "1.0-r0"
    When I PUT "/artifactory/api/repositories/apk-remote-member" as admin with body:
      """
      {"rclass":"remote","packageType":"alpine","url":"{upstream}"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/apk-remote-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"alpine","repositories":["apk-remote-member"],"keyPair":"apk-sign"}
      """
    Then the response status is 200
    When I GET "/artifactory/apk-remote-virtual/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 200
    Given the apk repository is "apk-remote-virtual"
    When I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"
    Given the generic upstream stops
    When I clear the apk installation root
    And I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"

  Scenario: delete, restore and reindex track the signed local package set
    Given the registry is running
    And the Alpine client can reach the registry
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/apk-lifecycle" as admin with body:
      """
      {"rclass":"local","packageType":"alpine","primaryKeyPairRef":"apk-sign"}
      """
    Then the response status is 200
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    When I deploy the file "apk/hello-1.0-r0.apk" to "/artifactory/apk-lifecycle/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 201
    When I GET "/artifactory/apk-lifecycle/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 200
    Then the response status is 200
    When I POST "/artifactory/api/alpine/apk-lifecycle/reindex" as admin
    Then the response status is 200
    And the queued reindex of "apk-lifecycle" completes within 20 seconds
    Given the apk repository is "apk-lifecycle"
    When I run apk "add hello"
    Then the command succeeds
    When I DELETE "/artifactory/apk-lifecycle/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 204
    When I GET "/artifactory/apk-lifecycle/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 404
    Then the response status is 404
    When I POST "/artifactory/api/trash/restore/apk-lifecycle/v3.20/main/x86_64/hello-1.0-r0.apk?to=apk-lifecycle" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I GET "/artifactory/apk-lifecycle/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 200
    Then the response status is 200
    When I clear the apk installation root
    And I run apk "add hello"
    Then the command succeeds
    And the file "alpine-root/usr/share/hello/message.txt" contains "installed hello"

  Scenario: invalid packages and partial permissions cannot publish or disclose an index
    Given the registry is running
    When I create an Alpine signing pair "apk-sign" with alias "test"
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/apk-safety" as admin with body:
      """
      {"rclass":"local","packageType":"alpine","primaryKeyPairRef":"apk-sign"}
      """
    Then the response status is 200
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    When I deploy the file "apk/hello-1.0-r0.apk" to "/artifactory/apk-safety/v3.20/main/x86_64/wrong-1.0-r0.apk" as admin
    Then the response status is 400
    When I corrupt the Alpine package "hello" at "1.0-r0"
    And I deploy the file "apk/hello-1.0-r0.apk" to "/artifactory/apk-safety/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 400
    When I GET "/artifactory/apk-safety/v3.20/main/x86_64/APKINDEX.tar.gz" as admin
    Then the response status is 404
    Given a signed Alpine package "hello" at "1.0-r0" under "apk"
    When I deploy the file "apk/hello-1.0-r0.apk" to "/artifactory/apk-safety/v3.20/main/x86_64/hello-1.0-r0.apk" as admin
    Then the response status is 201
    When I GET "/artifactory/apk-safety/v3.20/main/x86_64/APKINDEX.tar.gz" as admin until the response status is 200
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/index-only" as admin with body:
      """
      {"name":"index-only","email":"index-only@example.com","password":"Index-pass1"}
      """
    Then the response status is 201
    # New users auto-join seeded readers, whose Anything target grants global READ.
    # Remove it so this scenario tests only the explicit index permission below.
    When I PATCH "/access/api/v2/users/index-only/groups" as admin with JSON:
      """
      {"remove":["readers"]}
      """
    Then the response status is 200
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"alpine-index-only","resources":{"artifact":{"actions":{"users":{"index-only":["READ"]}},"targets":{"apk-safety":{"include_patterns":["v3.20/main/x86_64/APKINDEX.tar.gz"]}}}}}
      """
    Then the response status is 201
    When I GET "/artifactory/apk-safety/v3.20/main/x86_64/hello-1.0-r0.apk" as "index-only" with password "Index-pass1"
    Then the response status is 403
    When I GET "/artifactory/apk-safety/v3.20/main/x86_64/APKINDEX.tar.gz" as "index-only" with password "Index-pass1"
    Then the response status is 403
    When I deploy the Alpine package "hello" at "1.0-r0" to "/artifactory/apk-safety/v3.20/main/x86_64/hello-1.0-r0.apk" as "index-only" with password "Index-pass1"
    Then the response status is 403
