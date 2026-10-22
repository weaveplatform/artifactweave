@nuget @nuget_remote_symbols
Feature: Remote symbol reads preserve native PDB identities and bytes
  Symbols originate from the configured SSQP server. They are not extracted
  from locally cached packages, and cold reads require the symbol path grant.

  Scenario Outline: <dotnet> generated PDBs resolve through a private symbol remote
    Given the registry is running
    And the <dotnet> client can reach the registry
    And a file "NuGet.Config" with content:
      """
      <configuration><packageSources><clear/></packageSources></configuration>
      """
    And a file "pkg/Package.csproj" with content:
      """
      <Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net$(BundledNETCoreAppTargetFrameworkVersion)</TargetFramework><NuGetAudit>false</NuGetAudit><PackageId>Acme.Symbol</PackageId><Version>1.0.0</Version><IncludeSymbols>true</IncludeSymbols><SymbolPackageFormat>snupkg</SymbolPackageFormat></PropertyGroup></Project>
      """
    And a file "pkg/Value.cs" with content:
      """
      public static class Value { public static int Number => 629; }
      """
    When I run dotnet "pack pkg/Package.csproj --output nupkgs"
    Then the command succeeds
    Given a private symbol upstream serves PDBs from "nupkgs/Acme.Symbol.1.0.0.snupkg"
    When I PUT "/artifactory/api/repositories/nuget-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nuget","url":"{upstream}","symbolServerUrl":"{upstream}/symbols","username":"upstream","password":"private-fixture","forceNugetAuthentication":true}
      """
    Then the response status is 200
    When I run curl "--silent --show-error --fail --user admin:{password} --output cold.pdb {url}{symbol-route}"
    Then the command succeeds
    And the files "native.pdb" and "cold.pdb" are identical
    When I run curl "--silent --show-error --fail --head --user admin:{password} {url}{symbol-route}"
    Then the command succeeds
    And the output contains "Content-Length:"
    When I run curl "--silent --show-error --fail --range 0-15 --user admin:{password} --output range.pdb {url}{symbol-route}"
    Then the command succeeds
    And the files "native-prefix.pdb" and "range.pdb" are identical
    Given the generic upstream stops
    When I run curl "--silent --show-error --fail --user admin:{password} --output warm.pdb {url}{symbol-route}"
    Then the command succeeds
    And the files "native.pdb" and "warm.pdb" are identical

    Examples:
      | dotnet  |
      | .NET    |
      | .NET 11 |
