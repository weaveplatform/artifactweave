@nuget @nuget_v2
Feature: NuGet clients restore and search through the v2 feed
  The legacy Atom feed exposes the metadata and canonical download URL
  NuGet.Protocol needs, including prereleases during version resolution.

  Scenario Outline: <dotnet> searches and restores a prerelease through v2
    Given the registry is running
    And the <dotnet> client can reach the registry
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget","forceNugetAuthentication":true}
      """
    Then the response status is 200
    Given a NuGet package "Acme.Legacy" at "1.0.0" in "nupkgs"
    And a NuGet package "Acme.Legacy" at "2.0.0-beta.1" in "nupkgs"
    When I deploy the file "nupkgs/Acme.Legacy.1.0.0.nupkg" to "/artifactory/nuget-local/Acme.Legacy.1.0.0.nupkg" as admin
    Then the response status is 201
    When I deploy the file "nupkgs/Acme.Legacy.2.0.0-beta.1.nupkg" to "/artifactory/nuget-local/Acme.Legacy.2.0.0-beta.1.nupkg" as admin
    Then the response status is 201
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/api/nuget/nuget-local" protocolVersion="2" allowInsecureConnections="true" />
        </packageSources>
        <packageSourceCredentials>
          <aw>
            <add key="Username" value="admin" />
            <add key="ClearTextPassword" value="{password}" />
          </aw>
        </packageSourceCredentials>
      </configuration>
      """
    And a file "app/app.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk">
        <PropertyGroup>
          <TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework>
          <NuGetAudit>false</NuGetAudit>
        </PropertyGroup>
        <ItemGroup>
          <PackageReference Include="Acme.Legacy" Version="[2.0.0-beta.1,3.0.0)" />
        </ItemGroup>
      </Project>
      """
    When I run dotnet "package search Acme.Legacy --format json"
    Then the command succeeds
    And the output contains "Acme.Legacy"
    And the output contains "1.0.0"
    And the output does not contain "2.0.0-beta.1"
    When I run dotnet "package search Acme.Legacy --format json --prerelease"
    Then the command succeeds
    And the output contains "2.0.0-beta.1"
    When I run dotnet "restore app/app.csproj"
    Then the command succeeds
    And the files "nupkgs/Acme.Legacy.2.0.0-beta.1.nupkg" and ".nuget/packages/acme.legacy/2.0.0-beta.1/acme.legacy.2.0.0-beta.1.nupkg" are identical
    And the file "app/obj/project.assets.json" contains "Acme.Legacy/2.0.0-beta.1"
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-local/FindPackagesById()"
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-local/Download/Acme.Legacy/2.0.0-beta.1"
    When I GET "/artifactory/api/nuget/nuget-local/GetUpdates()" as admin
    Then the response status is 404
    When I GET "/artifactory/api/nuget/nuget-local/package/Acme.Legacy/2.0.0-beta.1" as admin
    Then the response status is 404

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
