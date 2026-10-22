@nuget @nuget_exit
Feature: NuGet snapshot retention and JFrog build records
  Both SDK lines resolve the packages they publish and report their real
  dependency bytes to Artifactory's build API.

  Scenario Outline: <dotnet> observes snapshot retention at the repository root
    Given the registry is running with environment "ARTIFACTWEAVE_NUGET_SNAPSHOT_CLEANUP_INTERVAL=1s"
    And the <dotnet> client can reach the registry
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget","maxUniqueSnapshots":2}
      """
    Then the response status is 200
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/api/nuget/v3/nuget-local/index.json" allowInsecureConnections="true" />
        </packageSources>
        <packageSourceCredentials>
          <aw><add key="Username" value="admin" /><add key="ClearTextPassword" value="{password}" /></aw>
        </packageSourceCredentials>
      </configuration>
      """
    And a NuGet package "Acme.Snap" at "1.0.0-ci.1" in "nupkgs"
    And a NuGet package "Acme.Snap" at "1.0.0-ci.2" in "nupkgs"
    And a NuGet package "Acme.Snap" at "1.0.0-ci.3" in "nupkgs"
    And a NuGet package "Acme.Snap" at "1.0.0" in "nupkgs"
    And a NuGet package "Acme.Snap" at "2.0.0-ci.1" in "nupkgs"
    When I run dotnet "nuget push nupkgs/Acme.Snap.1.0.0-ci.1.nupkg --source aw --api-key admin:{password} --no-symbols"
    Then the command succeeds
    # Name each later version in upload order. A wildcard lets the SDK
    # enumerate ci.1 again after cleanup evicts it, making it newest.
    When I run dotnet "nuget push nupkgs/Acme.Snap.1.0.0-ci.2.nupkg --source aw --api-key admin:{password} --no-symbols"
    Then the command succeeds
    When I run dotnet "nuget push nupkgs/Acme.Snap.1.0.0-ci.3.nupkg --source aw --api-key admin:{password} --no-symbols"
    Then the command succeeds
    When I run dotnet "nuget push nupkgs/Acme.Snap.1.0.0.nupkg --source aw --api-key admin:{password} --no-symbols"
    Then the command succeeds
    When I run dotnet "nuget push nupkgs/Acme.Snap.2.0.0-ci.1.nupkg --source aw --api-key admin:{password} --no-symbols"
    Then the command succeeds
    And within 10 seconds GET "/artifactory/nuget-local/Acme.Snap.1.0.0-ci.1.nupkg" as admin answers 404
    When I GET "/artifactory/api/nuget/v3/nuget-local/flatcontainer/acme.snap/index.json" as admin
    Then the response status is 200
    And the JSON field "versions" has 4 entries
    And the response body contains "1.0.0-ci.2"
    And the response body contains "1.0.0-ci.3"
    And the response body contains "2.0.0-ci.1"
    And the response body does not contain "1.0.0-ci.1"
    When I GET "/artifactory/nuget-local/Acme.Snap.1.0.0.nupkg" as admin
    Then the response status is 200
    When I run dotnet "package search Acme.Snap --format json --prerelease"
    Then the command succeeds
    And the output contains "2.0.0-ci.1"

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |

  @jf
  Scenario Outline: <dotnet> publishes and restores with JFrog native build-info
    Given the registry is running
    And the <dotnet> client can reach the registry
    And the JFrog CLI is available
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget","forceNugetAuthentication":true}
      """
    Then the response status is 200
    Given a file "NuGet.Config" with content:
      """
      <configuration><packageSources><clear /></packageSources></configuration>
      """
    And a file "pkg/Package.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk">
        <PropertyGroup>
          <TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework>
          <NuGetAudit>false</NuGetAudit>
          <PackageId>Acme.Build</PackageId>
          <Version>1.0.0</Version>
        </PropertyGroup>
      </Project>
      """
    And a file "pkg/Value.cs" with content:
      """
      public static class Value { public static int Number => 42; }
      """
    And a file "app/app.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk">
        <PropertyGroup>
          <TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework>
          <NuGetAudit>false</NuGetAudit>
        </PropertyGroup>
        <ItemGroup><PackageReference Include="Acme.Build" Version="1.0.0" /></ItemGroup>
      </Project>
      """
    When I run dotnet "pack pkg/Package.csproj --output nupkgs"
    Then the command succeeds
    When I run jf dotnet client "config add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf dotnet client "dotnet nuget push nupkgs/Acme.Build.1.0.0.nupkg --repo nuget-local --server-id aw --allow-insecure-connections --build-name nuget-publish --build-number 1"
    Then the command succeeds
    When I run jf dotnet client "rt bp nuget-publish 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/nuget-publish/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.artifacts" has 1 entries
    And the response body contains "Acme.Build.1.0.0.nupkg"
    When I run jf dotnet client "dotnet restore app/app.csproj --packages jf-v3 --no-http-cache --use-lock-file --repo-resolve nuget-local --server-id aw --allow-insecure-connections --build-name nuget-restore --build-number 1"
    Then the command succeeds
    And the files "nupkgs/Acme.Build.1.0.0.nupkg" and "jf-v3/acme.build/1.0.0/acme.build.1.0.0.nupkg" are identical
    And the NuGet lockfile "app/packages.lock.json" pins "Acme.Build" to the package "nupkgs/Acme.Build.1.0.0.nupkg"
    When I run jf dotnet client "rt bp nuget-restore 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/nuget-restore/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.dependencies" has 1 entries
    And the response body contains "Acme.Build"
    Given a file "V2.Config" with expanded content:
      """
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/api/nuget/nuget-local" protocolVersion="2" allowInsecureConnections="true" />
        </packageSources>
        <packageSourceCredentials>
          <aw><add key="Username" value="admin" /><add key="ClearTextPassword" value="{password}" /></aw>
        </packageSourceCredentials>
      </configuration>
      """
    When I run jf dotnet client "dotnet restore app/app.csproj --configfile V2.Config --packages jf-v2 --no-http-cache --force --server-id aw --build-name nuget-restore-v2 --build-number 1"
    Then the command succeeds
    And the files "nupkgs/Acme.Build.1.0.0.nupkg" and "jf-v2/acme.build/1.0.0/acme.build.1.0.0.nupkg" are identical
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-local/Download/Acme.Build/1.0.0"
    When I run jf dotnet client "rt bp nuget-restore-v2 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/nuget-restore-v2/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules.0.dependencies" has 1 entries
    And the response body contains "Acme.Build"

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
