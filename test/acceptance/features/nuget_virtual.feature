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
    Then the response status is 501

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 10 |
