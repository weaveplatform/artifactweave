@cocoapods
Feature: Native CocoaPods CDN repositories
  A pinned pod client resolves a package and its archive through ArtifactWeave.

  Scenario: install a local pod through the CDN index
    Given the registry is running
    And the CocoaPods client can reach the registry
    When I PUT "/artifactory/api/repositories/pods-local" as admin with body:
      """
      {"rclass":"local","packageType":"cocoapods"}
      """
    Then the response status is 200
    Given a CocoaPods project for repository "pods-local"
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    When I deploy the file "HelloPod-1.0.0.tar.gz" to "/artifactory/pods-local/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I GET "/artifactory/api/pods/pods-local/all_pods.txt" as admin
    Then the response status is 200
    And the response body contains "HelloPod"
    When I run pod "repo add-cdn pods-local {platform}/artifactory/api/pods/pods-local"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists

  Scenario: install an HTTP-source remote pod from a cold cache and after an outage
    Given the registry is running
    And the CocoaPods client can reach the registry
    And a CocoaPods project for repository "pods-remote"
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    When I PUT "/artifactory/api/repositories/pods-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","hardFail":true,"externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**"]}
      """
    Then the response status is 200
    When I run pod "repo add-cdn pods-remote {platform}/artifactory/api/pods/pods-remote"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists
    And the generic upstream received 1 GET requests for "/HelloPod-1.0.0.tar.gz"
    When I POST "/artifactory/api/repositories/pods-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    And the CocoaPods client cache is cleared
    When I run pod "repo add-cdn pods-remote {platform}/artifactory/api/pods/pods-remote"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists

  Scenario: install a static Ruby podspec archive through the CDN index
    Given the registry is running
    And the CocoaPods client can reach the registry
    When I PUT "/artifactory/api/repositories/pods-local" as admin with body:
      """
      {"rclass":"local","packageType":"cocoapods"}
      """
    Then the response status is 200
    Given a CocoaPods project for repository "pods-local"
    And a static Ruby CocoaPods archive named "HelloPod" version "1.0.0"
    When I deploy the file "HelloPod-1.0.0.tar.gz" to "/artifactory/pods-local/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I run pod "repo add-cdn pods-local {platform}/artifactory/api/pods/pods-local"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists

  Scenario: install an HTTP-source pod after a same-origin archive redirect
    Given the registry is running
    And the CocoaPods client can reach the registry
    And a CocoaPods project for repository "pods-redirect"
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    And the CocoaPods upstream redirects the source archive "HelloPod-1.0.0.tar.gz"
    When I PUT "/artifactory/api/repositories/pods-redirect" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","hardFail":true,"externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**"]}
      """
    Then the response status is 200
    When I run pod "repo add-cdn pods-redirect {platform}/artifactory/api/pods/pods-redirect"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists
    And the generic upstream received 1 GET requests for "/moved/HelloPod-1.0.0.tar.gz"

  Scenario: install an HTTP-source pod after an approved cross-origin redirect
    Given a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    And the CocoaPods upstream redirects the source archive "HelloPod-1.0.0.tar.gz" to an approved origin
    And the CocoaPods registry starts with its archive origin approved
    And the CocoaPods client can reach the registry
    And a CocoaPods project for repository "pods-cross-origin"
    When I PUT "/artifactory/api/repositories/pods-cross-origin" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","username":"source-user","password":"source-pass","externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**","{pod_source}/approved/**"],"hardFail":true}
      """
    Then the response status is 200
    When I run pod "repo add-cdn pods-cross-origin {platform}/artifactory/api/pods/pods-cross-origin"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists
    And the approved CocoaPods source received the archive without upstream credentials

  Scenario: install a pod through a local-backed virtual repository
    Given the registry is running
    And the CocoaPods client can reach the registry
    When I PUT "/artifactory/api/repositories/pods-member" as admin with body:
      """
      {"rclass":"local","packageType":"cocoapods"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pods-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"cocoapods","repositories":["pods-member"]}
      """
    Then the response status is 200
    Given a CocoaPods project for repository "pods-virtual"
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    When I deploy the file "HelloPod-1.0.0.tar.gz" to "/artifactory/pods-member/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I run pod "repo add-cdn pods-virtual {platform}/artifactory/api/pods/pods-virtual"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists

  Scenario: install a pod through a one-node federated repository
    Given the registry is running
    And the CocoaPods client can reach the registry
    When I PUT "/artifactory/api/repositories/pods-fed" as admin with body:
      """
      {"rclass":"federated","packageType":"cocoapods"}
      """
    Then the response status is 200
    Given a CocoaPods project for repository "pods-fed"
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    When I deploy the file "HelloPod-1.0.0.tar.gz" to "/artifactory/pods-fed/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I run pod "repo add-cdn pods-fed {platform}/artifactory/api/pods/pods-fed"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists

  Scenario: install an HTTP-source pod through a remote-backed virtual repository
    Given the registry is running
    And the CocoaPods client can reach the registry
    And a CocoaPods project for repository "pods-virtual"
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    When I PUT "/artifactory/api/repositories/pods-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","hardFail":true,"externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**"]}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pods-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"cocoapods","repositories":["pods-remote"]}
      """
    Then the response status is 200
    When I run pod "repo add-cdn pods-virtual {platform}/artifactory/api/pods/pods-virtual"
    Then the command succeeds
    When I run pod "install --no-repo-update --verbose"
    Then the command succeeds
    And the file "Pods/HelloPod/Sources/HelloPod.m" exists

  Scenario: delete restore and reindex reconcile the local CDN view
    Given the registry is running
    When I PUT "/artifactory/api/repositories/pods-life" as admin with body:
      """
      {"rclass":"local","packageType":"cocoapods"}
      """
    Then the response status is 200
    Given a CocoaPods archive named "HelloPod" version "1.0.0"
    When I deploy the file "HelloPod-1.0.0.tar.gz" to "/artifactory/pods-life/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I POST "/artifactory/api/cocoapods/pods-life/reindex" as admin
    Then the response status is 200
    And the queued reindex of "pods-life" completes within 20 seconds
    When I GET "/artifactory/api/pods/pods-life/all_pods_versions_b_f_0.txt" as admin
    Then the response status is 200
    And the response body contains "HelloPod/1.0.0"
    When I DELETE "/artifactory/pods-life/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 204
    When I GET "/artifactory/api/pods/pods-life/all_pods_versions_b_f_0.txt" as admin
    Then the response status is 404
    When I POST "/artifactory/api/trash/restore/pods-life/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz?to=pods-life" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I GET "/artifactory/api/pods/pods-life/all_pods_versions_b_f_0.txt" as admin until the response status is 200
    Then the response status is 200
    And the response body contains "HelloPod/1.0.0"

  Scenario: CDN indexes disclose only packages the caller can read
    Given the registry is running
    When I PUT "/artifactory/api/repositories/pods-private" as admin with body:
      """
      {"rclass":"local","packageType":"cocoapods"}
      """
    Then the response status is 200
    Given a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods archive named "SecretPod" version "1.0.0"
    When I deploy the file "HelloPod-1.0.0.tar.gz" to "/artifactory/pods-private/HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I deploy the file "SecretPod-1.0.0.tar.gz" to "/artifactory/pods-private/SecretPod/1.0.0/SecretPod-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/security/users/pods-reader" as admin with body:
      """
      {"name":"pods-reader","email":"pods-reader@example.com","password":"Pods-pass1"}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/users/pods-reader/groups" as admin with JSON:
      """
      {"remove":["readers"]}
      """
    Then the response status is 200
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"pods-readable","resources":{"artifact":{"actions":{"users":{"pods-reader":["READ"]}},"targets":{"pods-private":{"include_patterns":["HelloPod/1.0.0/HelloPod-1.0.0.tar.gz"]}}}}}
      """
    Then the response status is 201
    When I GET "/artifactory/api/pods/pods-private/all_pods.txt" as "pods-reader" with password "Pods-pass1"
    Then the response status is 200
    And the response body contains "HelloPod"
    And the response body does not contain "SecretPod"
    When I GET "/artifactory/api/pods/pods-private/Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json" as "pods-reader" with password "Pods-pass1"
    Then the response status is 200
    When I GET "/artifactory/api/pods/pods-private/all_pods_versions_b_f_0.txt" as "pods-reader" with password "Pods-pass1"
    Then the response status is 200
    And the response body contains "HelloPod/1.0.0"

  Scenario: a remote archive with the wrong upstream checksum is not cached
    Given the registry is running
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    And the CocoaPods upstream advertises a wrong archive checksum
    When I PUT "/artifactory/api/repositories/pods-corrupt" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","hardFail":true,"externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**"]}
      """
    Then the response status is 200
    When I GET "/artifactory/api/pods/pods-corrupt/sources/HelloPod/1.0.0/source.tar.gz" as admin
    Then the response status is 502
    And the generic upstream received 1 GET requests for "/HelloPod-1.0.0.tar.gz"

  Scenario: an upstream index fault does not become a successful CDN shard
    Given the registry is running
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    And the CocoaPods upstream fails its version index
    When I PUT "/artifactory/api/repositories/pods-fault" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","hardFail":true,"externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**"]}
      """
    Then the response status is 200
    When I GET "/artifactory/api/pods/pods-fault/all_pods_versions_b_f_0.txt" as admin
    Then the response status is 503

  Scenario: zapping remote CDN metadata discovers a newly published version
    Given the registry is running
    And a CocoaPods archive named "HelloPod" version "1.0.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.0.0.tar.gz"
    When I PUT "/artifactory/api/repositories/pods-refresh" as admin with body:
      """
      {"rclass":"remote","packageType":"cocoapods","url":"{upstream}","podsCdnUrl":"{upstream}","hardFail":true,"externalDependenciesEnabled":true,"externalDependenciesPatterns":["{upstream}/**"]}
      """
    Then the response status is 200
    When I GET "/artifactory/api/pods/pods-refresh/all_pods_versions_b_f_0.txt" as admin
    Then the response status is 200
    And the response body contains "HelloPod/1.0.0"
    Given a CocoaPods archive named "HelloPod" version "1.1.0"
    And a CocoaPods HTTP upstream serves the archive "HelloPod-1.1.0.tar.gz"
    When I POST "/artifactory/api/zap/pods-refresh-cache/cdn" as admin
    Then the response status is 200
    When I GET "/artifactory/api/pods/pods-refresh/all_pods_versions_b_f_0.txt" as admin
    Then the response status is 200
    And the response body contains "HelloPod/1.1.0"
    When I GET "/artifactory/api/pods/pods-refresh/Specs/b/f/0/HelloPod/1.1.0/HelloPod.podspec.json" as admin
    Then the response status is 200
