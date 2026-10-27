@aieditorextensions
Feature: VS Code installs from a native AI editor extensions remote repository
  The official VS Code 1.143.1 client queries the gallery and downloads the
  unchanged Microsoft Hex Editor 1.11.1 assets through an explicit anonymous
  read grant. A fresh profile can install from the remote cache offline.

  Scenario: pinned VS Code installs through the permission-controlled remote cache
    Given the registry is running
    And the VS Code client can reach the registry
    And the official AI editor Marketplace fixture serves Hex Editor
    When I PUT "/artifactory/api/repositories/editor-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"aieditorextensions","url":"{upstream}/_apis/public/gallery"}
      """
    Then the response status is 200
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"ai-editor-anonymous-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"editor-remote":{"include_patterns":["extensions/ms-vscode/hexeditor/**"]}}}}}
      """
    Then the response status is 201
    When I install Hex Editor with VS Code profile "cold"
    Then the command succeeds
    And the output contains "successfully installed"
    And the AI editor gallery assets use the client-reachable authority
    And the AI editor upstream received 1 gallery queries
    When I POST "/artifactory/api/repositories/editor-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the AI editor upstream stops
    When I install Hex Editor with VS Code profile "warm"
    Then the command succeeds
    And the output contains "successfully installed"
    And the AI editor upstream received 1 gallery queries
