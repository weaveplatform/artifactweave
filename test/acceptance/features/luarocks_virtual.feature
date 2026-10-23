@luarocks @luarocks_virtual
Feature: Native LuaRocks resolution through a virtual repository
  A virtual URL publishes only to its local target and resolves local-only,
  remote-only and mixed dependencies without another rocks server.

  Scenario: virtual publication and mixed install with offline cached member
    Given the registry is running
    And the LuaRocks client can reach the registry
    And a LuaRocks virtual project with a remote dependency for repository "combined"
    When I run luarocks "pack remoteonly-1.0-1.rockspec"
    Then the command succeeds
    Given a LuaRocks upstream serves the packed dependency
    When I PUT "/artifactory/api/repositories/local" as admin with body:
      """
      {"rclass":"local","packageType":"luarocks"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rocks" as admin with body:
      """
      {"rclass":"remote","packageType":"luarocks","url":"{upstream}/rocks/"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/combined" as admin with body:
      """
      {"rclass":"virtual","packageType":"luarocks","repositories":["local","rocks"],"defaultDeploymentRepo":"local"}
      """
    Then the response status is 200
    When I run luarocks "upload hello-1.0-1.rockspec"
    Then the command succeeds
    When I run luarocks "upload localonly-1.0-1.rockspec"
    Then the command succeeds
    When I GET "/artifactory/api/luarocks/local/manifest-5.4" as admin
    Then the response status is 200
    And the response body contains "hello"
    And the response body contains "localonly"
    When I run luarocks "install localonly 1.0-1 --only-server={authurl}/api/luarocks/combined --tree /work/virtual-local"
    Then the command succeeds
    And the file "virtual-local/share/lua/5.4/localonly.lua" exists
    When I run luarocks "install remoteonly 1.0-1 --only-server={authurl}/api/luarocks/combined --tree /work/virtual-remote"
    Then the command succeeds
    And the file "virtual-remote/share/lua/5.4/remoteonly.lua" exists
    When I run luarocks "install hello 1.0-1 --only-server={authurl}/api/luarocks/combined --tree /work/virtual-mixed"
    Then the command succeeds
    And the file "virtual-mixed/share/lua/5.4/hello.lua" exists
    And the file "virtual-mixed/share/lua/5.4/remoteonly.lua" exists
    When I POST "/artifactory/api/repositories/rocks" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I run luarocks "install hello 1.0-1 --only-server={authurl}/api/luarocks/combined --tree /work/virtual-offline"
    Then the command succeeds
    And the file "virtual-offline/share/lua/5.4/hello.lua" exists
    And the file "virtual-offline/share/lua/5.4/remoteonly.lua" exists
