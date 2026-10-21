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
    And a file "app/app.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk">
        <PropertyGroup>
          <TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework>
          <NuGetAudit>false</NuGetAudit>
        </PropertyGroup>
        <ItemGroup>
          <PackageReference Include="Acme.Publish" Version="1.0.0" />
        </ItemGroup>
      </Project>
      """
    And a file "Anonymous.Config" with expanded content:
      """
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/api/nuget/v3/nuget-local/index.json" allowInsecureConnections="true" />
        </packageSources>
      </configuration>
      """
    When I run dotnet "pack pkg/Package.csproj --output nupkgs"
    Then the command succeeds
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.nupkg --source aw --api-key admin:{publish-token}"
    Then the command succeeds
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.snupkg" as admin
    Then the response status is 200
    And the client PDBs in the response resolve from NuGet repository "nuget-local"
    When I run dotnet "restore app/app.csproj --configfile Anonymous.Config --packages anonymous --no-http-cache"
    Then the command fails
    And the output contains "401"
    And the server answered 401 to GET "/artifactory/api/nuget/v3/nuget-local/index.json"
    When I run dotnet "restore app/app.csproj --packages restored-v3 --no-http-cache --use-lock-file"
    Then the command succeeds
    And the files "nupkgs/Acme.Publish.1.0.0.nupkg" and "restored-v3/acme.publish/1.0.0/acme.publish.1.0.0.nupkg" are identical
    And the NuGet lockfile "app/packages.lock.json" pins "Acme.Publish" to the package "nupkgs/Acme.Publish.1.0.0.nupkg"
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.nupkg --source aw --api-key admin:{publish-token} --no-symbols"
    Then the command fails
    And the output contains "409"
    Given a NuGet package "acme.publish" at "1.0.0+renamed" in "duplicates"
    When I run dotnet "nuget push duplicates/acme.publish.1.0.0+renamed.nupkg --source aw --api-key admin:{publish-token} --no-symbols"
    Then the command fails
    And the output contains "409"
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.nupkg --source aw --api-key admin:{publish-token} --skip-duplicate"
    Then the command succeeds
    And the server answered 409 to PUT "/artifactory/api/nuget/nuget-local/"
    When I POST "/artifactory/api/nuget/nuget-local/Acme.Publish/1.0.0" as admin
    Then the response status is 200
    When I GET "/artifactory/api/nuget/v3/nuget-local/flatcontainer/acme.publish/index.json" as admin
    Then the response status is 200
    And the JSON field "versions.0" equals "1.0.0"
    When I run dotnet "nuget delete Acme.Publish 1.0.0 --source aw --api-key admin:{publish-token} --non-interactive"
    Then the command succeeds
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.nupkg" as admin
    Then the response status is 404
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.snupkg" as admin
    Then the response status is 404

    And the captured NuGet symbol URLs no longer resolve
    When I POST "/artifactory/api/nuget/nuget-local/Acme.Publish/1.0.0" as admin
    Then the response status is 404
    When I run dotnet "restore app/app.csproj --packages after-delete --no-http-cache --force"
    Then the command fails
    And the output contains "NU1101"
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.nupkg --source aw --api-key admin:{publish-token} --no-symbols"
    Then the command succeeds
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.snupkg" as admin
    Then the response status is 404
    When I run dotnet "nuget push nupkgs/Acme.Publish.1.0.0.snupkg --source aw --api-key admin:{publish-token}"
    Then the command succeeds
    When I GET "/artifactory/nuget-local/Acme.Publish.1.0.0.snupkg" as admin
    Then the response status is 200
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
            <add key="ClearTextPassword" value="{publish-token}" />
          </aw>
        </packageSourceCredentials>
      </configuration>
      """
    When I run dotnet "restore app/app.csproj --packages restored-v2 --no-http-cache --force"
    Then the command succeeds
    And the files "nupkgs/Acme.Publish.1.0.0.nupkg" and "restored-v2/acme.publish/1.0.0/acme.publish.1.0.0.nupkg" are identical
    And the NuGet lockfile "app/packages.lock.json" pins "Acme.Publish" to the package "nupkgs/Acme.Publish.1.0.0.nupkg"
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-local/Download/Acme.Publish/1.0.0"
    When I PUT "/artifactory/api/security/users/publisher" as admin with body:
      """
      {"name":"publisher","email":"publisher@example.test","password":"Publisher-Password-123!"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/nuget-publish" as admin with body:
      """
      {"name":"nuget-publish","repositories":["nuget-local"],"includesPattern":"**","principals":{"users":{"publisher":["r","w"]}}}
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
          <aw>
            <add key="Username" value="publisher" />
            <add key="ClearTextPassword" value="Publisher-Password-123!" />
          </aw>
        </packageSourceCredentials>
      </configuration>
      """
    When I run dotnet "nuget push duplicates/acme.publish.1.0.0+renamed.nupkg --source aw --api-key publisher:Publisher-Password-123! --no-symbols"
    Then the command fails
    And the output contains "409"

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
