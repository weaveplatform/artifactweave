@nuget @nuget_search
Feature: NuGet clients discover packages through the v3 feed
  Searches use current indexed metadata, and a SemVer2-aware client can
  include prereleases without exposing them in a stable-only search.

  Background:
    Given the registry is running

  Scenario Outline: <dotnet> searches a local NuGet feed
    Given the <dotnet> client can reach the registry
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget"}
      """
    Then the response status is 200
    Given a NuGet package "Acme.Search" at "1.0.0" in "nupkgs"
    And a NuGet package "Acme.Search" at "2.0.0-beta.1" in "nupkgs"
    When I deploy the file "nupkgs/Acme.Search.1.0.0.nupkg" to "/artifactory/nuget-local/Acme.Search.1.0.0.nupkg" as admin
    Then the response status is 201
    When I deploy the file "nupkgs/Acme.Search.2.0.0-beta.1.nupkg" to "/artifactory/nuget-local/Acme.Search.2.0.0-beta.1.nupkg" as admin
    Then the response status is 201
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/api/nuget/v3/nuget-local/index.json" allowInsecureConnections="true" />
        </packageSources>
        <packageSourceCredentials>
          <aw>
            <add key="Username" value="admin" />
            <add key="ClearTextPassword" value="{password}" />
          </aw>
        </packageSourceCredentials>
      </configuration>
      """
    When I run dotnet "package search Acme.Search --format json --take 10"
    Then the command succeeds
    And the output contains "Acme.Search"
    And the output contains "1.0.0"
    And the output does not contain "2.0.0-beta.1"
    When I run dotnet "package search Acme.Search --format json --prerelease"
    Then the command succeeds
    And the output contains "2.0.0-beta.1"
    When I run dotnet "package search Acme.Search --format json --exact-match"
    Then the command succeeds
    And the output contains "Acme.Search"
    And the output contains "1.0.0"
    And the output does not contain "2.0.0-beta.1"
    When I GET "/artifactory/api/nuget/v3/nuget-local/autocomplete?q=ACME.SE&semVerLevel=2.0.0" as admin
    Then the response status is 200
    And the JSON field "data.0" equals "Acme.Search"
    When I GET "/artifactory/api/nuget/v3/nuget-local/autocomplete?id=acme.search&prerelease=true&semVerLevel=2.0.0" as admin
    Then the response status is 200
    And the JSON field "data.1" equals "2.0.0-beta.1"

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
