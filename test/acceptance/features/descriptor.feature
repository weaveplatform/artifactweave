@descriptor
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
      backups:
        nightly: {}
      """
    Then the response status is 400
    And the response body contains "sections not supported: backups"
    When I GET "/artifactory/api/repositories/refused-local" as admin
    Then the response status is 404

  Scenario: An admin configures and clears mail credentials through the descriptor
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      mailServer:
        enabled: false
        host: smtp.invalid
        port: 2525
        password: mail-private-fixture
      """
    Then the response status is 200
    And the response body is "4 changes to config merged successfully"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      mailServer:
        enabled: false
        host: smtp.invalid
        port: 2525
        password: mail-private-fixture
      """
    Then the response status is 200
    And the response body is "4 changes to config merged successfully"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      mailServer:
        password: null
      """
    Then the response status is 200
    And the response body is "1 changes to config merged successfully"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      mailServer:
        port: "2525"
      """
    Then the response status is 400
    And the response body contains "mailServer.port"

  Scenario Outline: A malformed <kind> boolean refuses the whole mixed descriptor
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        typed-kept: {type: generic, description: before}
      """
    Then the response status is 200
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        typed-kept: {description: after}
        typed-created: {type: generic}
      <section>:
        broken: <entry>
      """
    Then the response status is 400
    And the response body contains "<field> must be a boolean"
    When I GET "/artifactory/api/repositories/typed-kept" as admin
    Then the response status is 200
    And the JSON field "description" equals "before"
    When I GET "/artifactory/api/repositories/typed-created" as admin
    Then the response status is 404

    Examples:
      | kind             | section      | entry                                                                                   | field           |
      | proxy            | proxies      | {host: proxy.invalid, port: 3128, platformDefault: {unexpected: true}}                  | platformDefault |
      | predefined value | propertySets | {properties: {release: {predefinedValues: {ready: {defaultValue: {unexpected: true}}}}}} | defaultValue    |
      | multiple choice  | propertySets | {properties: {release: {multipleChoice: {unexpected: true}}}}                           | multipleChoice  |

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
  # descriptor GET.
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

  Scenario: Mixed PATCH and config apply share one protected XML read model
    Given a file "mixed-descriptor.yaml" with content:
      """
      localRepositories:
        xml-local:
          type: generic
          description: 'data <>&'
      remoteRepositories:
        xml-remote:
          type: generic
          url: https://upstream.invalid/repository
          password: remote-private-fixture
      virtualRepositories:
        xml-virtual:
          type: generic
          repositories: [xml-local, xml-remote]
      proxies:
        xml-proxy:
          host: proxy.invalid
          port: 3128
          password: proxy-private-fixture
      propertySets:
        xml-set:
          visible: false
          properties:
            release:
              multipleChoice: false
              closedPredefinedValues: true
              predefinedValues:
                - value: 'value <>&'
                  defaultValue: true
      repoLayouts:
        xml-layout:
          name: xml-layout
          artifactPathPattern: '[module]/[baseRev]/[module]-[baseRev].[ext]'
          descriptorPathPattern: ''
          distinctiveDescriptorPathPattern: false
      mailServer:
        enabled: false
        host: smtp.invalid
        password: mail-private-fixture
      security:
        anonAccessEnabled: true
      downloadRedirectConfig:
        fileMinimumSize: 0
      """
    When I PATCH descriptor file "mixed-descriptor.yaml" as admin
    Then the response status is 200
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response status is 200
    And the response header "Content-Type" contains "application/xml"
    And the XML field "localRepositories/localRepository[key=xml-local]/description" equals "data <>&"
    And the XML field "proxies/proxy[key=xml-proxy]/key" equals "xml-proxy"
    And the XML field "propertySets/propertySet[name=xml-set]/properties/property/predefinedValues/predefinedValue/value" equals "value <>&"
    And the XML field "repoLayouts/repoLayout[name=xml-layout]/name" equals "xml-layout"
    And the XML field "mailServer/port" equals "25"
    And the response body does not contain "private-fixture"
    And the response body does not contain "<password>"
    When I apply descriptor file "mixed-descriptor.yaml" to the server store
    Then the output contains "Unchanged:"
    And the output does not contain "Created:"
    And the output does not contain "Updated:"
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response status is 200
    And the XML field "localRepositories/localRepository[key=xml-local]/description" equals "data <>&"
    And the XML field "proxies/proxy[key=xml-proxy]/host" equals "proxy.invalid"
    And the XML field "propertySets/propertySet[name=xml-set]/name" equals "xml-set"
    And the XML field "repoLayouts/repoLayout[name=xml-layout]/name" equals "xml-layout"
    And the XML field "security/anonAccessEnabled" equals "true"
    And the XML field "downloadRedirectConfig/fileMinimumSize" equals "0"
    And the response body does not contain "private-fixture"
    When I GET "/artifactory/api/system/configuration"
    Then the response status is 401
