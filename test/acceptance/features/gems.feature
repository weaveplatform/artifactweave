@gems
Feature: Native RubyGems and Bundler repositories
  A pinned, published RubyGem resolves through the RubyGems client source URL.

  Scenario Outline: gem and Bundler install from a <class> repository
    Given the registry is running
    And the Ruby client can reach the registry
    And the pinned rake RubyGem is available
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"gems"}
      """
    Then the response status is 200
    When I deploy the file "rake-12.3.1.gem" to "/artifactory/<repo>/gems/rake-12.3.1.gem" as admin
    Then the response status is 201
    Given the RubyGems source is "<repo>"
    When I run Ruby "bundle" "install"
    Then the command succeeds
    And the file "Gemfile.lock" contains "rake (12.3.1)"
    When I run Ruby "gem" "install rake -v 12.3.1 --source {gemsource} --clear-sources --install-dir /work/gem-installed --no-document"
    Then the command succeeds
    And the file "gem-installed/gems/rake-12.3.1/README.rdoc" exists

    Examples:
      | class     | repo       |
      | local     | gems-local |
      | federated | gems-fed   |

  Scenario: gem and Bundler install through a virtual repository
    Given the registry is running
    And the Ruby client can reach the registry
    And the pinned rake RubyGem is available
    When I PUT "/artifactory/api/repositories/gems-local" as admin with body:
      """
      {"rclass":"local","packageType":"gems"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/gems-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"gems","repositories":["gems-local"]}
      """
    Then the response status is 200
    When I deploy the file "rake-12.3.1.gem" to "/artifactory/gems-local/gems/rake-12.3.1.gem" as admin
    Then the response status is 201
    Given the RubyGems source is "gems-virtual"
    When I run Ruby "bundle" "install"
    Then the command succeeds
    And the file "Gemfile.lock" contains "rake (12.3.1)"
    When I run Ruby "gem" "install rake -v 12.3.1 --source {gemsource} --clear-sources --install-dir /work/gem-installed --no-document"
    Then the command succeeds

  Scenario: gem and Bundler install from a remote cache
    Given the registry is running
    And the Ruby client can reach the registry
    And a RubyGems upstream serves the pinned rake gem
    When I PUT "/artifactory/api/repositories/gems-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"gems","url":"{upstream}"}
      """
    Then the response status is 200
    Given the RubyGems source is "gems-remote"
    When I run Ruby "bundle" "install"
    Then the command succeeds
    And the file "Gemfile.lock" contains "rake (12.3.1)"
    When I run Ruby "gem" "install rake -v 12.3.1 --source {gemsource} --clear-sources --install-dir /work/gem-installed --no-document"
    Then the command succeeds
