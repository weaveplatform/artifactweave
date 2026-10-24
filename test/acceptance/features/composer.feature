@composer
Feature: Composer resolves native packages through the approved repository classes

  Scenario Outline: Composer 2 installs a hosted package from <class>
    Given the registry is running
    And the Composer client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"composer"}
      """
    Then the response status is 200
    Given a Composer archive with message "hosted <class>"
    When I deploy the file "composer/widget.zip" to "/artifactory/<repo>/acme/widget/1.0.0.zip" as admin
    Then the response status is 201
    When I GET "/artifactory/api/composer/<repo>/p2/acme/widget.json" as admin
    Then the response status is 200
    Given a Composer project for repository "<repo>"
    When I run Composer "install --no-interaction --prefer-dist --no-plugins --no-scripts"
    Then the command succeeds
    And the file "composer-project/vendor/acme/widget/src/Widget.php" contains "hosted <class>"
    When I POST "/artifactory/api/composer/<repo>/reindex" as admin with body:
      """
      ""
      """
    Then the response status is 200

    Examples:
      | class     | repo    |
      | local     | php-l   |
      | federated | php-f   |

  Scenario: Composer 1 resolves a local repository during its support window
    Given the registry is running
    And the Composer 1 client can reach the registry
    When I PUT "/artifactory/api/repositories/php-v1" as admin with body:
      """
      {"rclass":"local","packageType":"composer"}
      """
    Then the response status is 200
    Given a Composer archive with message "Composer one"
    When I deploy the file "composer/widget.zip" to "/artifactory/php-v1/acme/widget/1.0.0.zip" as admin
    Then the response status is 201
    Given a Composer project for repository "php-v1"
    When I run Composer 1 "install --no-interaction --prefer-dist --no-plugins --no-scripts"
    Then the command succeeds
    And the file "composer-project/vendor/acme/widget/src/Widget.php" contains "Composer one"

  Scenario: Composer 2 selects the first virtual member
    Given the registry is running
    And the Composer client can reach the registry
    When I PUT "/artifactory/api/repositories/php-first" as admin with body:
      """
      {"rclass":"local","packageType":"composer"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/php-second" as admin with body:
      """
      {"rclass":"local","packageType":"composer"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/php-all" as admin with body:
      """
      {"rclass":"virtual","packageType":"composer","repositories":["php-first","php-second"]}
      """
    Then the response status is 200
    Given a Composer archive with message "first member"
    When I deploy the file "composer/widget.zip" to "/artifactory/php-first/acme/widget/1.0.0.zip" as admin
    Then the response status is 201
    Given a Composer archive with message "second member"
    When I deploy the file "composer/widget.zip" to "/artifactory/php-second/acme/widget/1.0.0.zip" as admin
    Then the response status is 201
    Given a Composer project for repository "php-all"
    When I run Composer "install --no-interaction --prefer-dist --no-plugins --no-scripts"
    Then the command succeeds
    And the file "composer-project/vendor/acme/widget/src/Widget.php" contains "first member"

  Scenario: Composer 2 uses a remote registry and its offline cache alias
    Given the registry is running
    And the Composer client can reach the registry
    And a Composer archive with message "remote member"
    And a Composer upstream serves that archive
    When I PUT "/artifactory/api/repositories/php-r" as admin with body:
      """
      {"rclass":"remote","packageType":"composer","url":"{upstream}/composer","composerRegistryUrl":"{upstream}/composer"}
      """
    Then the response status is 200
    Given a Composer project for repository "php-r"
    When I run Composer "install --no-interaction --prefer-dist --no-plugins --no-scripts"
    Then the command succeeds
    And the file "composer-project/vendor/acme/widget/src/Widget.php" contains "remote member"
    When I clear the Composer installation
    Given a Composer project for repository "php-r-cache"
    When I run Composer "install --no-interaction --prefer-dist --no-plugins --no-scripts"
    Then the command succeeds
    And the file "composer-project/vendor/acme/widget/src/Widget.php" contains "remote member"
