@luarocks
Feature: Native LuaRocks upload and install on a local repository
  A rockspec and source rock uploaded by LuaRocks appear in the manifest and
  can be installed by the same client at a pinned or latest version.

  Scenario: native upload, pinned install and latest install
    Given the registry is running
    And the LuaRocks client can reach the registry
    When I PUT "/artifactory/api/repositories/rocks" as admin with body:
      """
      {"rclass":"local","packageType":"luarocks"}
      """
    Then the response status is 200
    Given a LuaRocks project for repository "rocks"
    When I run luarocks "upload hello-1.0-1.rockspec"
    Then the command succeeds
    When I GET "/artifactory/api/luarocks/rocks/manifest-5.4" as admin
    Then the response status is 200
    And the response body contains "hello"
    And the response body contains "src"
    When I run luarocks "install hello 1.0-1 --tree /work/installed-pinned"
    Then the command succeeds
    When I run luarocks "install hello --tree /work/installed-latest"
    Then the command succeeds
    And the file "installed-pinned/share/lua/5.4/hello.lua" exists
    And the file "installed-latest/share/lua/5.4/hello.lua" exists

  Scenario: native upload and install on a one-node federated repository
    Given the registry is running
    And the LuaRocks client can reach the registry
    When I PUT "/artifactory/api/repositories/rocks-fed" as admin with body:
      """
      {"rclass":"federated","packageType":"luarocks"}
      """
    Then the response status is 200
    Given a LuaRocks project for repository "rocks-fed"
    When I run luarocks "upload hello-1.0-1.rockspec"
    Then the command succeeds
    When I GET "/artifactory/api/luarocks/rocks-fed/manifest-5.4" as admin
    Then the response status is 200
    And the response body contains "hello"
    When I run luarocks "install hello 1.0-1 --tree /work/installed-fed"
    Then the command succeeds
    And the file "installed-fed/share/lua/5.4/hello.lua" exists
