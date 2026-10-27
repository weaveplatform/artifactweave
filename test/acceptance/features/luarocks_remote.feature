@luarocks @luarocks_remote
Feature: Native LuaRocks install through a remote cache
  A fresh LuaRocks client resolves the upstream manifest and source rock through
  artifactweave, and another fresh client installs from verified server cache
  after the upstream is unavailable.

  Scenario: pinned remote install survives upstream outage
    Given the registry is running
    And the LuaRocks client can reach the registry
    And a LuaRocks project for repository "rocks"
    When I run luarocks "pack hello-1.0-1.rockspec"
    Then the command succeeds
    Given a LuaRocks upstream serves the packed project
    When I PUT "/artifactory/api/repositories/rocks" as admin with body:
      """
      {"rclass":"remote","packageType":"luarocks","url":"{upstream}/rocks/"}
      """
    Then the response status is 200
    When I run luarocks "install hello 1.0-1 --only-server={authurl}/api/luarocks/rocks --tree /work/remote-cold"
    Then the command succeeds
    And the file "remote-cold/share/lua/5.5/hello.lua" exists
    And the generic upstream received 1 GET requests for "/rocks/hello-1.0-1.src.rock"
    When I POST "/artifactory/api/repositories/rocks" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I run luarocks "install hello 1.0-1 --only-server={authurl}/api/luarocks/rocks --tree /work/remote-offline"
    Then the command succeeds
    And the file "remote-offline/share/lua/5.5/hello.lua" exists
