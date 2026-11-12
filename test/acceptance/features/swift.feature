@swift
Feature: Swift package registry metadata follows stored source archives
  Local and one-node federated Swift repositories expose the same release
  through the native registry API and can rebuild their package index.

  Scenario Outline: a <class> source archive appears in the Swift registry
    Given the registry is running
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"swift"}
      """
    Then the response status is 200
    Given a file "widget/Package.swift" with content:
      """
      // swift-tools-version:5.9
      import PackageDescription
      let package = Package(name: "Widget", targets: [.target(name: "Widget")])
      """
    And a file "widget/Sources/Widget/Widget.swift" with content:
      """
      public struct Widget { public init() {} }
      """
    And a zip file "Widget-1.0.0.zip" of the files under "widget"
    When I deploy the file "Widget-1.0.0.zip" to "/artifactory/<repo>/acme/Widget/Widget-1.0.0.zip" as admin
    Then the response status is 201
    When I GET "/artifactory/api/swift/<repo>/acme/Widget" as admin
    Then the response status is 200
    And the response body contains "1.0.0"
    When I GET "/artifactory/api/swift/<repo>/acme/Widget/1.0.0/Package.swift" as admin
    Then the response status is 200
    And the response body contains "import PackageDescription"
    When I POST "/artifactory/api/swift/<repo>/reindex" as admin
    Then the response status is 200

    Examples:
      | class     | repo       |
      | local     | swift-local |
      | federated | swift-fed   |

  Scenario: a Swift registry error carries the API version SwiftPM reads
    SwiftPM shows an error's problem details only when its Content-Version
    is 1, the version of the registry's other answers.

    Given the registry is running
    When I PUT "/artifactory/api/repositories/swift-errors" as admin with body:
      """
      {"rclass":"local","packageType":"swift"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/swift/swift-errors/acme/Missing" as admin
    Then the response status is 404
    And the response header "Content-Version" contains "1"
    And the response header "Content-Type" contains "application/problem+json"
    And the JSON field "detail" equals "Swift package not found"
