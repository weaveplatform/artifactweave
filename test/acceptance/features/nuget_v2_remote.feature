@nuget @nuget_v2_remote
Feature: NuGet v2 remote feeds retain native restore and bounded query behavior
  Configured feed and download paths reach a private upstream. Upstream paging
  and package redirects remain behind the proxy, including an offline restore.

  Scenario Outline: <dotnet> restores through a private v2 remote with custom paths
    Given the registry is running
    And the <dotnet> client can reach the registry
    And a NuGet package "Acme.Legacy" at "1.0.0" in "nupkgs"
    And a NuGet package "Acme.Legacy" at "2.0.0-beta.1" in "nupkgs"
    And a private NuGet v2 upstream serves packages from "nupkgs"
    When I PUT "/artifactory/api/repositories/nuget-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nuget","url":"{upstream}","feedContextPath":"custom/feed","downloadContextPath":"custom/download","v3FeedUrl":"","username":"upstream","password":"private-fixture","forceNugetAuthentication":true,"retrievalCachePeriodSecs":30}
      """
    Then the response status is 200
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration><packageSources><clear/><add key="aw" value="{url}/api/nuget/nuget-remote" protocolVersion="2" allowInsecureConnections="true"/></packageSources><packageSourceCredentials><aw><add key="Username" value="admin"/><add key="ClearTextPassword" value="{password}"/></aw></packageSourceCredentials></configuration>
      """
    And a file "app/app.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup><ItemGroup><PackageReference Include="Acme.Legacy" Version="[2.0.0-beta.1,3.0.0)"/></ItemGroup></Project>
      """
    When I run dotnet "package search Acme.Legacy --format json"
    Then the command succeeds
    And the output contains "1.0.0"
    And the output does not contain "2.0.0-beta.1"
    When I run dotnet "package search Acme.Legacy --format json --prerelease"
    Then the command succeeds
    And the output contains "2.0.0-beta.1"
    When I run dotnet "restore app/app.csproj --packages cold --no-http-cache"
    Then the command succeeds
    And the files "nupkgs/Acme.Legacy.2.0.0-beta.1.nupkg" and "cold/acme.legacy/2.0.0-beta.1/acme.legacy.2.0.0-beta.1.nupkg" are identical
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-remote/FindPackagesById()"
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-remote/Download/Acme.Legacy/2.0.0-beta.1"
    And the generic upstream received 1 GET requests for "/custom/download/Acme.Legacy/2.0.0-beta.1"
    Given the generic upstream stops
    When I run dotnet "restore app/app.csproj --packages warm --no-http-cache --force"
    Then the command succeeds
    And the files "nupkgs/Acme.Legacy.2.0.0-beta.1.nupkg" and "warm/acme.legacy/2.0.0-beta.1/acme.legacy.2.0.0-beta.1.nupkg" are identical

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
