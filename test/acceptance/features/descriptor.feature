Feature: Configuration descriptor
  Artifactory's YAML descriptor applied as a patch (decision 0009), as
  configuration-as-code pipelines and the artifactory_* configuration
  resources send it.

  Background:
    Given the registry is running

  Scenario: An admin creates, reapplies and deletes repositories through the descriptor
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local:
          type: generic
          description: from the descriptor
      virtualRepositories:
        desc-virtual:
          type: generic
          repositories:
            - desc-local
      """
    Then the response status is 200
    And the response body is "4 changes to config merged successfully"
    When I GET "/artifactory/api/repositories/desc-local" as admin
    Then the response status is 200
    And the JSON field "description" equals "from the descriptor"
    When I GET "/artifactory/api/repositories/desc-virtual" as admin
    Then the response status is 200
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local:
          type: generic
          description: from the descriptor
      """
    Then the response status is 200
    And the response body is "2 changes to config merged successfully"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local: null
      """
    Then the response status is 200
    And the response body is "1 changes to config merged successfully"
    When I GET "/artifactory/api/repositories/desc-local" as admin
    Then the response status is 404

  Scenario: Repository entries take the spellings of JFrog's YAML samples
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        spelt-local:
          type: generic
          description: spelt
          downloadRedirect:
            enabled: true
      remoteRepositories:
        spelt-remote:
          type: generic
          url: https://example.test/generic
          remoteRepoLayout: simple-default
      """
    Then the response status is 200
    And the response body is "6 changes to config merged successfully"
    When I GET "/artifactory/api/repositories/spelt-local" as admin
    Then the JSON field "downloadRedirect" equals "true"
    When I GET "/artifactory/api/repositories/spelt-remote" as admin
    Then the JSON field "remoteRepoLayoutRef" equals "simple-default"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        spelt-local:
          description: null
      """
    Then the response status is 200
    When I GET "/artifactory/api/repositories/spelt-local" as admin
    Then the JSON field "description" equals ""
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        spelt-local:
          blackedOut: null
      """
    Then the response status is 400
    And the JSON field "errors.0.message" exists

  Scenario: A section that is not applied is refused and changes nothing
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        refused-local:
          type: generic
      mailServer:
        host: smtp.example
      """
    Then the response status is 400
    And the response body contains "sections not supported: mailServer"
    When I GET "/artifactory/api/repositories/refused-local" as admin
    Then the response status is 404

  Scenario: An empty descriptor is refused rather than deleting every repository
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      # nothing
      """
    Then the response status is 400
    And the JSON field "errors.0.message" exists

  # The bodies are what the artifactory_repository_layout resource (provider
  # 12.11.14) marshals with yaml.v3 on create and update, and its delete body
  # verbatim. The resource's read and its delete's lookup go through the XML
  # descriptor GET, which is not served yet.
  Scenario: The Terraform repository layout resource creates, updates and deletes a layout
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      repoLayouts:
          tf-layout:
              name: tf-layout
              artifactPathPattern: '[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]'
              descriptorPathPattern: ""
              distinctiveDescriptorPathPattern: false
              fileIntegrationRevisionRegExp: SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))
              folderIntegrationRevisionRegExp: SNAPSHOT
      """
    Then the response status is 200
    And the response body contains "changes to config merged successfully"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      repoLayouts:
          tf-layout:
              name: tf-layout
              artifactPathPattern: '[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]'
              descriptorPathPattern: '[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).pom'
              distinctiveDescriptorPathPattern: true
              fileIntegrationRevisionRegExp: Foo|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))
              folderIntegrationRevisionRegExp: Foo
      """
    Then the response status is 200
    And the response body contains "changes to config merged successfully"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """

      repoLayouts:
        tf-layout: ~
      """
    Then the response status is 200
    And the response body contains "changes to config merged successfully"

  Scenario: A layout entry naming another layout is refused
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      repoLayouts:
        tf-layout:
          name: other-layout
          artifactPathPattern: "[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"
      """
    Then the response status is 400
    And the response body contains "repoLayouts.tf-layout: name other-layout does not match the entry key"
