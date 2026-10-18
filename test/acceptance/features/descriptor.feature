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
    And the response body contains "Created: localRepositories.desc-local, virtualRepositories.desc-virtual"
    When I GET "/artifactory/api/repositories/desc-local" as admin
    Then the response status is 200
    And the JSON field "description" equals "from the descriptor"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local:
          type: generic
          description: from the descriptor
      """
    Then the response status is 200
    And the response body contains "Unchanged: localRepositories.desc-local"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local: null
      """
    Then the response status is 200
    And the response body contains "Deleted: localRepositories.desc-local"
    When I GET "/artifactory/api/repositories/desc-local" as admin
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
    And the response body contains "Created: repoLayouts.tf-layout"
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
    And the response body contains "Updated: repoLayouts.tf-layout"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """

      repoLayouts:
        tf-layout: ~
      """
    Then the response status is 200
    And the response body contains "Deleted: repoLayouts.tf-layout"

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
