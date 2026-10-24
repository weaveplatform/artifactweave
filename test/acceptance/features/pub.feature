@pub
Feature: Dart pub uses native repositories
  The pinned Dart 3.10.0 client publishes and resolves controlled packages
  through the documented hosted v2 protocol.

  @pub_hosted
  Scenario Outline: publish and resolve through a hosted repository
    Given the registry is running
    And the Dart client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"pub"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "pub-token"
    Given the Dart client uses pub repository "<repo>" with token "pub-token"
    And a file "producer/pubspec.yaml" with expanded content:
      """
      name: hello_pub
      version: 1.2.3
      description: Native Dart pub acceptance package.
      environment:
        sdk: ^3.10.0
      publish_to: "{darturl}/artifactory/api/pub/<repo>"
      """
    And a file "producer/lib/hello_pub.dart" with content:
      """
      String message() => 'native pub';
      """
    And a file "producer/README.md" with content:
      """
      Native Dart pub acceptance package.
      """
    And a file "producer/CHANGELOG.md" with content:
      """
      ## 1.2.3
      Native test package.
      """
    And a file "producer/LICENSE" with content:
      """
      Test fixture only.
      """
    When I run dart in "producer" "pub publish --force"
    Then the command succeeds
    When I GET "/artifactory/api/pub/<repo>/api/packages/hello_pub" with the kept token "pub-token"
    Then the response status is 200
    And the JSON field "versions.0.version" equals "1.2.3"
    Given a file "consumer/pubspec.yaml" with content:
      """
      name: consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        hello_pub: ^1.0.0
      """
    When I run dart in "consumer" "pub get"
    Then the command succeeds
    And the file "consumer/pubspec.lock" contains "hello_pub"

    Examples:
      | class     | repo      |
      | local     | pub-local |
      | federated | pub-fed   |

  @pub_remote
  Scenario: remote redirect, verified cache and offline install
    Given the registry is running
    And the Dart client can reach the registry
    And a Dart pub upstream serves package "remote_pub" version "1.0.0"
    And the Dart pub upstream redirects the archive for "remote_pub" version "1.0.0"
    When I PUT "/artifactory/api/repositories/pub-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"pub","url":"{upstream}/pub/"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "pub-token"
    Given the Dart client uses pub repository "pub-remote" with token "pub-token"
    And a file "remote-consumer/pubspec.yaml" with content:
      """
      name: remote_consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        remote_pub: ^1.0.0
      """
    When I run dart in "remote-consumer" "pub get"
    Then the command succeeds
    And the file "remote-consumer/pubspec.lock" contains "remote_pub"
    And the generic upstream received 1 GET requests for "/pub/blobs/remote_pub-1.0.0.tar.gz"
    When I POST "/artifactory/api/repositories/pub-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    And I clear the Dart pub client cache
    And the Dart client uses pub repository "pub-remote" with token "pub-token"
    And a file "offline-consumer/pubspec.yaml" with content:
      """
      name: offline_consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        remote_pub: ^1.0.0
      """
    When I run dart in "offline-consumer" "pub get"
    Then the command succeeds
    And the file "offline-consumer/pubspec.lock" contains "remote_pub"

  @pub_refresh
  Scenario: a fresh upstream version is discovered by dart pub upgrade
    Given the registry is running
    And the Dart client can reach the registry
    And a Dart pub upstream serves package "refresh_pub" version "1.0.0"
    When I PUT "/artifactory/api/repositories/pub-refresh" as admin with body:
      """
      {"rclass":"remote","packageType":"pub","url":"{upstream}/pub/"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "pub-token"
    Given the Dart client uses pub repository "pub-refresh" with token "pub-token"
    And a file "refresh-consumer/pubspec.yaml" with content:
      """
      name: refresh_consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        refresh_pub: ^1.0.0
      """
    When I run dart in "refresh-consumer" "pub get"
    Then the command succeeds
    And the file "refresh-consumer/pubspec.lock" contains "1.0.0"
    Given a Dart pub upstream serves package "refresh_pub" version "1.1.0"
    When I POST "/artifactory/api/zap/pub-refresh-cache/api/packages/refresh_pub" as admin with body:
      """
      {}
      """
    Then the response status is 200
    When I run dart in "refresh-consumer" "pub upgrade"
    Then the command succeeds
    And the file "refresh-consumer/pubspec.lock" contains "1.1.0"

  @pub_virtual
  Scenario: virtual repository resolves ordered local and remote members
    Given the registry is running
    And the Dart client can reach the registry
    And a Dart pub upstream serves package "remote_pub" version "1.0.0"
    And a Dart pub archive "local_pub" version "2.0.0" is saved as "local_pub.tar.gz"
    When I PUT "/artifactory/api/repositories/pub-local" as admin with body:
      """
      {"rclass":"local","packageType":"pub"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pub-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"pub","url":"{upstream}/pub/"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/pub-all" as admin with body:
      """
      {"rclass":"virtual","packageType":"pub","repositories":["pub-local","pub-remote"],"defaultDeploymentRepo":"pub-local"}
      """
    Then the response status is 200
    When I deploy the file "local_pub.tar.gz" to "/artifactory/pub-local/local_pub/local_pub-2.0.0.tar.gz" as admin
    Then the response status is 201
    When I GET "/artifactory/api/pub/pub-local/api/packages/local_pub" as admin
    Then the response status is 200
    When I GET "/artifactory/api/pub/pub-all/api/packages/local_pub" as admin
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "pub-token"
    Given the Dart client uses pub repository "pub-all" with token "pub-token"
    And a file "mixed-consumer/pubspec.yaml" with content:
      """
      name: mixed_consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        local_pub: ^2.0.0
        remote_pub: ^1.0.0
      """
    When I run dart in "mixed-consumer" "pub get"
    Then the command succeeds
    And the file "mixed-consumer/pubspec.lock" contains "local_pub"
    And the file "mixed-consumer/pubspec.lock" contains "remote_pub"

  @pub_lifecycle
  Scenario: delete, restore and reindex preserve native package metadata
    Given the registry is running
    And the Dart client can reach the registry
    When I PUT "/artifactory/api/repositories/pub-life" as admin with body:
      """
      {"rclass":"local","packageType":"pub"}
      """
    Then the response status is 200
    Given a Dart pub archive "restored_pub" version "1.0.0" is saved as "restored_pub.tar.gz"
    When I deploy the file "restored_pub.tar.gz" to "/artifactory/pub-life/restored_pub/restored_pub-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I GET "/artifactory/api/pub/pub-life/api/packages/restored_pub" as admin
    Then the response status is 200
    When I DELETE "/artifactory/pub-life/restored_pub/restored_pub-1.0.0.tar.gz" as admin
    Then the response status is 204
    When I GET "/artifactory/api/pub/pub-life/api/packages/restored_pub" as admin
    Then the response status is 404
    When I POST "/artifactory/api/trash/restore/pub-life/restored_pub/restored_pub-1.0.0.tar.gz?to=pub-life" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I GET "/artifactory/api/pub/pub-life/api/packages/restored_pub" as admin
    Then the response status is 200
    When I POST "/artifactory/api/pub/pub-life/reindex" as admin
    Then the response status is 200
    When I GET "/artifactory/api/pub/pub-life/api/packages/restored_pub" as admin
    Then the response status is 200
    And the JSON field "versions.0.version" equals "1.0.0"
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "pub-token"
    Given the Dart client uses pub repository "pub-life" with token "pub-token"
    And a file "life-consumer/pubspec.yaml" with content:
      """
      name: life_consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        restored_pub: ^1.0.0
      """
    When I run dart in "life-consumer" "pub get"
    Then the command succeeds
    And the file "life-consumer/pubspec.lock" contains "restored_pub"

  @pub_integrity
  Scenario: a corrupted upstream archive cannot satisfy Dart pub
    Given the registry is running
    And the Dart client can reach the registry
    And a Dart pub upstream serves package "bad_pub" version "1.0.0"
    And the Dart pub upstream corrupts the archive for "bad_pub" version "1.0.0"
    When I PUT "/artifactory/api/repositories/pub-bad" as admin with body:
      """
      {"rclass":"remote","packageType":"pub","url":"{upstream}/pub/"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "pub-token"
    Given the Dart client uses pub repository "pub-bad" with token "pub-token"
    And a file "bad-consumer/pubspec.yaml" with content:
      """
      name: bad_consumer
      environment:
        sdk: ^3.10.0
      dependencies:
        bad_pub: ^1.0.0
      """
    When I run dart in "bad-consumer" "pub get"
    Then the command fails
    Given a Dart pub upstream serves package "bad_pub" version "1.0.0"
    When I run dart in "bad-consumer" "pub get"
    Then the command succeeds
    And the file "bad-consumer/pubspec.lock" contains "bad_pub"
