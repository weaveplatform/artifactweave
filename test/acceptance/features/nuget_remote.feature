@nuget @nuget_remote
Feature: Native NuGet restores signed packages through a private v3 remote
  Signed archive bytes, dependency identities and signature verification survive
  the proxy and a second restore uses a fresh client cache during an outage.

  Scenario Outline: <dotnet> verifies cold and warm remote packages
    Given the registry is running
    And the <dotnet> client can reach the registry
    And a private NuGet v3 upstream serves signed packages with dependencies
    When I PUT "/artifactory/api/repositories/nuget-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nuget","url":"{upstream}","v3FeedUrl":"{upstream}/v3/index.json","username":"upstream","password":"upstream-fixture","forceNugetAuthentication":true,"retrievalCachePeriodSecs":30}
      """
    Then the response status is 200
    Given a file "NuGet.Config" with expanded content:
      """
      <configuration>
        <config><add key="signatureValidationMode" value="require" /></config>
        <packageSources><clear /><add key="aw" value="{url}/api/nuget/v3/nuget-remote/index.json" allowInsecureConnections="true" /></packageSources>
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
    When I run verified dotnet "restore consumer/consumer.csproj --packages /work/cold --no-http-cache"
    Then the command succeeds
    And the files "signed/Acme.Signed.1.0.0.nupkg" and "cold/acme.signed/1.0.0/acme.signed.1.0.0.nupkg" are identical
    And the files "signed/Acme.Dependency.1.0.0.nupkg" and "cold/acme.dependency/1.0.0/acme.dependency.1.0.0.nupkg" are identical
    And the NuGet lockfile "consumer/packages.lock.json" pins "Acme.Signed" to the package "unsigned/Acme.Signed.1.0.0.nupkg"
    When I run verified dotnet "nuget verify cold/acme.signed/1.0.0/acme.signed.1.0.0.nupkg --all"
    Then the command succeeds
    Given the signed NuGet package "signed/Acme.Signed.1.0.0.nupkg" is tampered into "tampered.nupkg"
    When I run verified dotnet "nuget verify tampered.nupkg --all"
    Then the command fails
    And the output contains "NU3008"
    When the generic upstream stops
    And I run verified dotnet "restore consumer/consumer.csproj --packages /work/warm --no-http-cache --force"
    Then the command succeeds
    And the files "signed/Acme.Signed.1.0.0.nupkg" and "warm/acme.signed/1.0.0/acme.signed.1.0.0.nupkg" are identical
    And the files "signed/Acme.Dependency.1.0.0.nupkg" and "warm/acme.dependency/1.0.0/acme.dependency.1.0.0.nupkg" are identical
    When I run verified dotnet "nuget verify warm/acme.signed/1.0.0/acme.signed.1.0.0.nupkg --all"
    Then the command succeeds

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
