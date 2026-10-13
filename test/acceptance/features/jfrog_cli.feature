@jf
Feature: JFrog CLI smoke
  The jf binary must work unchanged. These scenarios run only when
  ARTIFACTWEAVE_JF_BIN points at a jf binary; the CI acceptance job downloads
  a pinned release.

  Background:
    Given the registry is running
    And the JFrog CLI is available

  Scenario: jf rt ping
    When I run jf "rt ping --url {url}"
    Then the command succeeds

  Scenario: jf config add with a dummy token does not contact the server
    When I run jf "config add acceptance --url {url} --access-token dummy --interactive=false"
    Then the command succeeds
