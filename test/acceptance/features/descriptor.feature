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
      sumoLogicConfig:
        enabled: true
      """
    Then the response status is 400
    And the response body contains "sections not supported: sumoLogicConfig"
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
      gcConfig:
        cronExp: '0 0 /6 * * ?'
      trashcanConfig:
        enabled: true
        retentionPeriodDays: 7
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
    And the XML field "gcConfig/cronExp" equals "0 0 /6 * * ?"
    And the XML field "trashcanConfig/enabled" equals "true"
    And the XML field "trashcanConfig/retentionPeriodDays" equals "7"
    And the response body does not contain "private-fixture"
    When I GET "/artifactory/api/system/configuration"
    Then the response status is 401

  Scenario: Backup definitions apply, reapply, update and delete with matching XML and CLI summaries
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        backup-skip-a: {type: generic}
        backup-skip-b: {type: generic}
      backups:
        nightly:
          key: nightly
          cronExp: "0 0 2 ? * MON-FRI"
          enabled: true
          retentionPeriodHours: 336
          excludedRepositories: [backup-skip-a, backup-skip-b]
          createArchive: false
          excludeNewRepositories: true
          sendMailOnError: false
          precalculate: true
          exportMissionControl: false
      """
    Then the response status is 200
    And the response body is "13 changes to config merged successfully"
    When I GET "/artifactory/api/system/configuration" as admin
    Then the XML field "backups/backup[key=nightly]/cronExp" equals "0 0 2 ? * MON-FRI"
    And the XML field "backups/backup[key=nightly]/retentionPeriodHours" equals "336"
    And the XML field "backups/backup[key=nightly]/sendMailOnError" equals "false"
    And the XML field "backups/backup[key=nightly]/precalculate" equals "true"
    And the response body contains "<repositoryRef>backup-skip-b</repositoryRef>"
    And the response body does not contain "excludeBuilds"
    Given a file "backup.yaml" with content:
      """
      backups:
        nightly:
          cronExp: "0 0 2 ? * MON-FRI"
          retentionPeriodHours: 336
      """
    When I apply descriptor file "backup.yaml" to the server store
    Then the output contains "Unchanged: backups.nightly"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      backups:
        nightly:
          enabled: false
          excludedRepositories: [backup-skip-a]
      """
    Then the response status is 200
    When the registry restarts on the same data
    And I GET "/artifactory/api/system/configuration" as admin
    Then the XML field "backups/backup[key=nightly]/enabled" equals "false"
    And the XML field "backups/backup[key=nightly]/excludedRepositories/repositoryRef" equals "backup-skip-a"
    And the XML field "backups/backup[key=nightly]/retentionPeriodHours" equals "336"
    When I DELETE "/artifactory/api/repositories/backup-skip-a" as admin
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/backup-skip-a" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response body does not contain "<repositoryRef>backup-skip-a</repositoryRef>"
    And the XML field "backups/backup[key=nightly]/retentionPeriodHours" equals "336"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        backup-skip-c: {type: generic}
      backups:
        nightly:
          cronExp: "not a schedule"
      """
    Then the response status is 400
    And the response body contains "backups.nightly: "
    When I GET "/artifactory/api/repositories/backup-skip-c" as admin
    Then the response status is 404
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      backups:
        nightly:
          excludedRepositories: [missing-repository]
      """
    Then the response status is 400
    And the response body contains "repository missing-repository does not exist"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      backups:
        nightly: ~
      """
    Then the response status is 200
    And the response body is "1 changes to config merged successfully"
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response body does not contain "nightly"

  Scenario: The system message applies and reapplies through PATCH and config apply without an XML element
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      systemMessageConfig:
        enabled: true
        message: "Maintenance tonight [https://status.invalid, status]"
        title: Notice
        titleColor: 99c27c
      """
    Then the response status is 200
    And the response body is "4 changes to config merged successfully"
    When the registry restarts on the same data
    Given a file "message.yaml" with content:
      """
      systemMessageConfig:
        enabled: true
        title: Notice
      """
    When I apply descriptor file "message.yaml" to the server store
    Then the output contains "Unchanged: systemMessageConfig"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      systemMessageConfig: ~
      """
    Then the response status is 400
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response status is 200
    And the response body does not contain "systemMessage"
    And the response body does not contain "Maintenance tonight"

  Scenario: Mail settings deliver a test mail and a quota warning, and fail truthfully when disabled, wrong or deleted
    Given an SMTP server receives mail
    When I PATCH the SMTP server's mail settings as admin with password "fixture"
    Then the response status is 200
    When I send a test mail to "operator@example.invalid" from the server store
    Then the SMTP server receives a message to "operator@example.invalid" with subject "[CFG-07] Test" containing "Your Artifactory base URL is: https://registry.invalid/artifactory/"
    When I PUT "/artifactory/api/security/users/mail-quota-owner" as admin with body:
      """
      {"email":"quota-owner@example.invalid","password":"quota-owner-password","admin":false}
      """
    Then the response status is 201
    When I POST "/access/api/v1/projects" as admin with body:
      """
      {"project_key":"mailq","display_name":"Mail quota","storage_quota_bytes":100,"storage_quota_email_notification":true}
      """
    Then the response status is 201
    When I PUT "/access/api/v1/projects/mailq/users/mail-quota-owner" as admin with body:
      """
      {"roles":["Project Admin"]}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/mailq-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic","projectKey":"mailq"}
      """
    Then the response status is 200
    When the registry restarts on the same data
    And I upload 75 quota bytes to "/artifactory/mailq-generic/item.bin"
    Then the SMTP server receives a message to "quota-owner@example.invalid" with subject "[CFG-07] Project storage quota warning: mailq" containing "Artifactory: https://registry.invalid/artifactory/"
    When I PATCH the SMTP server's mail settings as admin with password "wrong-mail-password"
    Then the response status is 200
    And sending a test mail to "operator@example.invalid" from the server store fails with "mail authentication: mail delivery failed"
    And the output does not contain "wrong-mail-password"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      mailServer:
        enabled: false
      """
    Then the response status is 200
    And sending a test mail to "operator@example.invalid" from the server store fails with "Mail server configuration must be enabled with a defined host and port."
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      mailServer: ~
      """
    Then the response status is 200
    And the response body is "1 changes to config merged successfully"
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response body does not contain "mailServer"
    And sending a test mail to "operator@example.invalid" from the server store fails with "Mail server configuration must be enabled with a defined host and port."
    And the SMTP server has received 2 messages
