@nix
Feature: Nix stores each store path in its binary-cache folder
  Artifactory 7.x keeps a store path's narinfo and NAR in one folder,
  binary-cache/<store hash>, where jf nix looks for them with AQL
  (jfrog-cli-artifactory artifactory/commands/nix/command.go). nix copy
  uploads them to the paths of a plain binary cache, and the repository
  stores them in that folder (#1296).

  Scenario: nix copy stores a store path's narinfo and NAR in its binary-cache folder
    Given the registry is running
    And the Nix client can reach the registry
    And the Nix signing key is installed
    When I PUT "/artifactory/api/repositories/nix-layout" as admin with body:
      """
      {"rclass":"local","packageType":"nix","primaryKeyPairRef":"nix-signing"}
      """
    Then the response status is 200
    When I publish the controlled Nix path to "nix-layout"
    Then the command succeeds
    When I GET "/artifactory/api/storage/nix-layout/binary-cache/fyli4q2yj6vh01bs0jxqbvj5hir5zvqc" as admin
    Then the response status is 200
    And the response body contains "/fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
    And the response body contains ".nar.xz"
    When I GET "/artifactory/api/storage/nix-layout/nar" as admin
    Then the response status is 404
    When I POST "/artifactory/api/search/aql" as admin with body:
      """
      items.find({"repo":"nix-layout","$or":[{"$and":[{"path":"binary-cache/fyli4q2yj6vh01bs0jxqbvj5hir5zvqc"},{"name":{"$match":"*.nar.xz"}}]}]})
      """
    Then the response status is 200
    And the JSON field "range.total" equals "1"
    And the JSON field "results.0.path" equals "binary-cache/fyli4q2yj6vh01bs0jxqbvj5hir5zvqc"
    When I substitute the controlled Nix path from "nix-layout" with the "trusted" key
    Then the command succeeds
    And the output contains "native nix fixture"
