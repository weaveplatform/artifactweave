@jf @skills
Feature: JFrog CLI Skills against a local repository
  The pinned jf client publishes and installs canonical skill bundles and
  reads the indexed list and search APIs.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/skills-local" as admin with body:
      """
      {"rclass":"local","packageType":"skills"}
      """
    Then the response status is 200
    And the JFrog CLI runs with "JFROG_SKILLS_DISABLE_QUIET_FAILURE" set to "true"
    When I run jf "c add skills --url {platform}/ --user admin --password {password} --interactive=false"
    Then the command succeeds

  Scenario: publish, list, search, install and delete a skill
    Given a file "review-skill/SKILL.md" with content:
      """
      ---
      name: review-skill
      version: 1.0.0
      description: Reviews source changes
      ---
      # Review Skill
      """
    And a file "install/.keep" with content:
      """
      present
      """
    When I run jf "agent skills publish review-skill --repo skills-local --server-id skills --skip-scan"
    Then the command succeeds
    When I run jf "agent skills list --repo skills-local --server-id skills"
    Then the command succeeds
    And the output contains "review-skill"
    When I run jf "agent skills search review --repo skills-local --server-id skills"
    Then the command succeeds
    And the output contains "review-skill"
    When I run jf "agent skills install review-skill --repo skills-local --version 1.0.0 --path install --server-id skills"
    Then the command succeeds
    And the files "review-skill/SKILL.md" and "install/review-skill/SKILL.md" are identical
    Given a file "review-skill/SKILL.md" with content:
      """
      ---
      name: review-skill
      version: 2.0.0
      description: Reviews changes and test evidence
      ---
      # Review Skill v2
      """
    When I run jf "agent skills publish review-skill --repo skills-local --server-id skills --skip-scan"
    Then the command succeeds
    When I run jf "agent skills update review-skill --repo skills-local --path install --server-id skills"
    Then the command succeeds
    And the files "review-skill/SKILL.md" and "install/review-skill/SKILL.md" are identical
    When I run jf "agent skills delete review-skill --repo skills-local --version 1.0.0 --server-id skills"
    Then the command succeeds
    When I run jf "agent skills delete review-skill --repo skills-local --version 2.0.0 --server-id skills"
    Then the command succeeds
    When I GET "/artifactory/api/skills/skills-local/api/v1/skills/review-skill" as admin
    Then the response status is 404
