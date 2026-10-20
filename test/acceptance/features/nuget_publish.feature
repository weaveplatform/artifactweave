@nuget @nuget_publish
Feature: NuGet clients publish and delete packages
  Native pushes use the PackagePublish and SymbolPackagePublish resources.
  Existing versions return 409 for skip-duplicate, deletion removes both
  files, and relisting an existing version changes nothing.

  Background:
    Given the registry is running

  Scenario Outline: <dotnet> publishes package and symbols with forced authentication
    Given the <dotnet> client can reach the registry
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget","forceNugetAuthentication":true}
      """
    Then the response status is 200
    Given the next request has header "Content-Type" "application/x-www-form-urlencoded"
    When I POST "/artifactory/api/security/token" as admin with body:
      """
      username=admin&scope=applied-permissions%2Fuser&expires_in=3600
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "publish-token"
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
            <add key="ClearTextPassword" value="{publish-token}" />
          </aw>
        </packageSourceCredentials>
      </configuration>
      """
    And a file "pkg/Package.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk">
        <PropertyGroup>
          <TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework>
          <NuGetAudit>false</NuGetAudit>
          <PackageId>Acme.Publish</PackageId>
          <Version>1.0.0</Version>
          <IncludeSymbols>true</IncludeSymbols>
          <SymbolPackageFormat>snupkg</SymbolPackageFormat>
        </PropertyGroup>
      </Project>
      """
    And a file "pkg/Value.cs" with content:
      """
      public static class Value { public static int Number => 42; }
      """
    When I run dotnet "pack pkg/Package.csproj --output nupkgs"
    Then the command succeeds
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.nupkg --source aw --api-key admin:{publish-token}"
    Then the command succeeds
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.snupkg" as admin
    Then the response status is 200
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.nupkg --source aw --api-key admin:{publish-token} --skip-duplicate"
    Then the command succeeds
    And the server answered 409 to PUT "/artifactory/api/nuget/nuget-local/"
    When I POST "/artifactory/api/nuget/nuget-local/Acme.Publish/1.0.0" as admin
    Then the response status is 200
    When I run dotnet "nuget delete Acme.Publish 1.0.0 --source aw --api-key admin:{publish-token} --non-interactive"
    Then the command succeeds
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.nupkg" as admin
    Then the response status is 404
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.snupkg" as admin
    Then the response status is 404

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
