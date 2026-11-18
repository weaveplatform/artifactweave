@nuget @nuget_virtual
Feature: Native NuGet restores signed dependencies through one virtual v3 feed
  Scenario Outline: <dotnet> restores a remote parent and its local transitive dependency
    Given the registry is running
    And the <dotnet> client can reach the registry
    And a private NuGet v3 upstream serves signed packages with dependencies
    And the signed NuGet dependency is available only from a local member
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget"}
      """
    Then the response status is 200
    When I deploy the file "signed/Acme.Dependency.1.0.0.nupkg" to "/artifactory/nuget-local/Acme.Dependency.1.0.0.nupkg" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/nuget-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nuget","url":"{upstream}","v3FeedUrl":"{upstream}/v3/index.json","username":"upstream","password":"upstream-fixture","forceNugetAuthentication":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/nuget-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"nuget","repositories":["nuget-local","nuget-remote"],"forceNugetAuthentication":true}
      """
    Then the response status is 200
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration>
        <config><add key="signatureValidationMode" value="require" /></config>
        <packageSources><clear /><add key="aw" value="{url}/api/nuget/v3/nuget-virtual/index.json" allowInsecureConnections="true" /></packageSources>
        <packageSourceCredentials><aw><add key="Username" value="admin" /><add key="ClearTextPassword" value="{password}" /></aw></packageSourceCredentials>
        <trustedSigners><author name="fixture"><certificate fingerprint="{nugetSigner}" hashAlgorithm="SHA256" allowUntrustedRoot="false" /></author></trustedSigners>
      </configuration>
      """
    And a file "consumer/consumer.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk">
        <PropertyGroup><TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework><NuGetAudit>false</NuGetAudit><RestorePackagesWithLockFile>true</RestorePackagesWithLockFile></PropertyGroup>
        <ItemGroup><PackageReference Include="Acme.Signed" Version="1.0.0" /></ItemGroup>
      </Project>
      """
    When I run verified dotnet "restore consumer/consumer.csproj --packages /work/fresh --no-http-cache"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the files "signed/Acme.Signed.1.0.0.nupkg" and "fresh/acme.signed/1.0.0/acme.signed.1.0.0.nupkg" are identical
    And the files "signed/Acme.Dependency.1.0.0.nupkg" and "fresh/acme.dependency/1.0.0/acme.dependency.1.0.0.nupkg" are identical
    And the NuGet lockfile "consumer/packages.lock.json" pins "Acme.Signed" to the package "unsigned/Acme.Signed.1.0.0.nupkg"
    And the NuGet lockfile "consumer/packages.lock.json" pins "Acme.Dependency" to the package "unsigned/Acme.Dependency.1.0.0.nupkg"
    When I run verified dotnet "nuget verify fresh/acme.signed/1.0.0/acme.signed.1.0.0.nupkg --all"
    Then the command succeeds
    When I run verified dotnet "nuget verify fresh/acme.dependency/1.0.0/acme.dependency.1.0.0.nupkg --all"
    Then the command succeeds
    And the generic upstream received 0 GET requests for "/content/acme.dependency.signed.nupkg"
    When I GET "/artifactory/api/nuget/v3/nuget-virtual/index.json" as admin
    Then the response status is 200
    And the response body contains "/api/nuget/v3/nuget-virtual/"
    And the response body does not contain "/api/nuget/v3/nuget-remote/"
    And the response body does not contain "/api/nuget/v3/nuget-local/"
    When I GET "/artifactory/api/nuget/v3/nuget-virtual/registration-semver2/acme.signed/page/1.0.0/1.0.0.json" as admin
    Then the response status is 200
    And the response body contains "/nuget-virtual/registration-semver2/acme.dependency/index.json"
    And the response body contains "/nuget-virtual/flatcontainer/acme.signed/1.0.0/acme.signed.1.0.0.nupkg"
    When I GET "/artifactory/api/nuget/v3/nuget-virtual/a.pdb/invalid/a.pdb" as admin
    Then the response status is 501
    When I GET "/artifactory/api/nuget/nuget-virtual/" as admin
    Then the response status is 200

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 10 |

  Scenario Outline: <dotnet> restores local and private remote packages through a virtual v2 feed
    Given the registry is running
    And the <dotnet> client can reach the registry
    And a NuGet package "Acme.Legacy" at "1.0.0" in "nupkgs"
    And a NuGet package "Acme.Legacy" at "2.0.0-beta.1" in "nupkgs"
    And a NuGet package "Acme.Local" at "1.0.0" in "local"
    And a private NuGet v2 upstream serves packages from "nupkgs"
    When I PUT "/artifactory/api/repositories/nuget-local" as admin with body:
      """
      {"rclass":"local","packageType":"nuget"}
      """
    Then the response status is 200
    When I deploy the files under "local" to "/artifactory/nuget-local" as admin
    When I PUT "/artifactory/api/repositories/nuget-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nuget","url":"{upstream}","feedContextPath":"custom/feed","downloadContextPath":"custom/download","v3FeedUrl":"","username":"upstream","password":"private-fixture","forceNugetAuthentication":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/nuget-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"nuget","repositories":["nuget-local","nuget-remote"],"forceNugetAuthentication":true}
      """
    Then the response status is 200
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration><packageSources><clear/><add key="aw" value="{url}/api/nuget/nuget-virtual" protocolVersion="2" allowInsecureConnections="true"/></packageSources><packageSourceCredentials><aw><add key="Username" value="admin"/><add key="ClearTextPassword" value="{password}"/></aw></packageSourceCredentials></configuration>
      """
    And a file "app/app.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup><ItemGroup><PackageReference Include="Acme.Legacy" Version="[2.0.0-beta.1,3.0.0)"/><PackageReference Include="Acme.Local" Version="1.0.0"/></ItemGroup></Project>
      """
    When I run dotnet "restore app/app.csproj --packages v2-fresh --no-http-cache"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the files "nupkgs/Acme.Legacy.2.0.0-beta.1.nupkg" and "v2-fresh/acme.legacy/2.0.0-beta.1/acme.legacy.2.0.0-beta.1.nupkg" are identical
    And the files "local/Acme.Local.1.0.0.nupkg" and "v2-fresh/acme.local/1.0.0/acme.local.1.0.0.nupkg" are identical
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-virtual/FindPackagesById()"
    And the server answered 200 to GET "/artifactory/api/nuget/nuget-virtual/Download/Acme.Legacy/2.0.0-beta.1"
    And the generic upstream received 1 GET requests for "/custom/download/Acme.Legacy/2.0.0-beta.1"
    When I GET "/artifactory/api/nuget/nuget-virtual/Packages()" as admin
    Then the response status is 200
    And the response body contains "/api/nuget/nuget-virtual/Download/"
    And the response body does not contain "/api/nuget/nuget-remote/"
    When I PUT "/artifactory/api/nuget/nuget-virtual/" as admin with body:
      """
      malformed deferred upload
      """
    Then the response status is 501

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 10 |
