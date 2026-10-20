@nuget @nuget_v3
Feature: NuGet clients restore from a NuGet repository
  The v3 index and both download paths resolve packages indexed by the
  NuGet format. A registration-only discovery document exercises the
  fallback NuGet uses when a source advertises no flat container.

  Background:
    Given the registry is running

  Scenario Outline: <dotnet> restores through <source>
    Given the <dotnet> client can reach the registry
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/nuget-discovery" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given a NuGet package "Acme.Smoke" at "1.0.0" in "nupkgs"
    When I deploy the file "nupkgs/Acme.Smoke.1.0.0.nupkg" to "/artifactory/nuget-local/Acme.Smoke.1.0.0.nupkg" as admin
    Then the response status is 201
    Given a file "registration-index.json" with expanded content:
      """
      {"version":"3.0.0","resources":[{"@id":"{url}/api/nuget/v3/nuget-local/registration-semver2/","@type":"RegistrationsBaseUrl/3.6.0"}]}
      """
    When I deploy the file "registration-index.json" to "/artifactory/nuget-discovery/index.json" as admin
    Then the response status is 201
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/<source>" allowInsecureConnections="true" />
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
          <PackageReference Include="Acme.Smoke" Version="1.0.0" />
        </ItemGroup>
      </Project>
      """
    When I run dotnet "restore app/app.csproj"
    Then the command succeeds
    And the files "nupkgs/Acme.Smoke.1.0.0.nupkg" and ".nuget/packages/acme.smoke/1.0.0/acme.smoke.1.0.0.nupkg" are identical
    And the file "app/obj/project.assets.json" contains "Acme.Smoke/1.0.0"

    Examples:
      | dotnet  | source                              |
      | .NET    | api/nuget/v3/nuget-local/index.json   |
      | .NET    | nuget-discovery/index.json           |
      | .NET 11 | api/nuget/v3/nuget-local/index.json   |
      | .NET 11 | nuget-discovery/index.json           |
