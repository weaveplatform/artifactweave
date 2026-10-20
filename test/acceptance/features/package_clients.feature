Feature: The package clients reach artifactweave
  The real clients P7's and P8's exit criteria use (decision 0015), proven
  against what artifactweave serves before each phase's format code:
  generic repositories laid out the way each client reads them, and Maven
  deploys through item paths. npm, Maven, the Python tools and dotnet run in
  containers pinned by digest (test/acceptance/clients); Go is the runner's
  toolchain, and helm and terraform are pinned binaries.

  Background:
    Given the registry is running

  @go
  Scenario: go mod download from a generic repository laid out as a module proxy
    Given the Go toolchain is available
    When I PUT "/artifactory/api/repositories/go-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given a Go module "example.com/acme/lib" at "v1.0.0" laid out as a module proxy under "goproxy"
    When I deploy the files under "goproxy" to "/artifactory/go-generic" as admin
    And the registry is fronted by TLS
    And the Go proxy is "{tlsauthurl}/go-generic"
    And I run go "mod download -json example.com/acme/lib@v1.0.0"
    Then the command succeeds
    And the output contains "v1.0.0.zip"
    And the output contains "GoModSum"

  @npm
  Scenario: npm installs a package from a static packument in a generic repository
    Given the npm client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given an npm package "acme-smoke" at "1.0.0" with its tarball at "/artifactory/npm-generic/tarballs/acme-smoke-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-smoke.json" to "/artifactory/npm-generic/acme-smoke" as admin
    Then the response status is 201
    When I deploy the file "npm/acme-smoke-1.0.0.tgz" to "/artifactory/npm-generic/tarballs/acme-smoke-1.0.0.tgz" as admin
    Then the response status is 201
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/npm-generic/
      //{host}/artifactory/npm-generic/:_auth={basicauth}
      """
    When I run npm "install acme-smoke@1.0.0"
    Then the command succeeds
    And the file "node_modules/acme-smoke/package.json" contains "1.0.0"
    And the file "package-lock.json" contains "sha512-"

  @maven
  Scenario: mvn deploys a release through item paths and a fresh build resolves it
    Given the Maven client can reach the registry
    When I PUT "/artifactory/api/repositories/mvn-local" as admin with body:
      """
      {"rclass":"local","packageType":"maven"}
      """
    Then the response status is 200
    Given the Maven project "lib" in "lib"
    And a file "settings.xml" with expanded content:
      """
      <settings>
        <servers>
          <server>
            <id>aw</id>
            <username>admin</username>
            <password>{password}</password>
          </server>
        </servers>
      </settings>
      """
    # package then deploy:deploy, not install: the job's cached local
    # repository must hold only plugins, never this artifact.
    When I run mvn "-f lib/pom.xml -s settings.xml -Dmaven.repo.local={m2} package deploy:deploy -DaltDeploymentRepository=aw::{url}/mvn-local"
    Then the command succeeds
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<release>1.0.0</release>"
    # Under client-checksums a checksum file serves what the client sent:
    # mvn sends a SHA-1 and an MD5 after each file. The metadata's are the
    # server's own.
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar.sha1" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/1.0.0/lib-1.0.0.pom.md5" as admin
    Then the response status is 200
    When I GET "/artifactory/mvn-local/org/acme/smoke/lib/maven-metadata.xml.sha1" as admin
    Then the response status is 200
    # A fresh local repository, chained to the cache for plugins only, so the
    # artifact can come from nowhere but artifactweave.
    When I run mvn "-s settings.xml -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2} org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact=org.acme.smoke:lib:1.0.0 -DremoteRepositories=aw::default::{url}/mvn-local"
    Then the command succeeds
    And the file "fresh/org/acme/smoke/lib/1.0.0/lib-1.0.0.jar" exists

  # pip reads the index from the scenario's pip.conf and the project's page
  # from the folder's HTML listing, which is a simple index page to it; uv
  # installs the same wheel by its item path. twine checks the wheel the
  # scenario built before any of them reads it.
  @pypi
  Scenario: pip and uv fetch a wheel through item paths in a generic repository
    Given the Python client can reach the registry
    When I PUT "/artifactory/api/repositories/pypi-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given a Python wheel "acme-smoke" at "1.0.0" in "dist"
    When I run twine "check --strict dist/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    When I deploy the file "dist/acme_smoke-1.0.0-py3-none-any.whl" to "/artifactory/pypi-generic/simple/acme-smoke/acme_smoke-1.0.0-py3-none-any.whl" as admin
    Then the response status is 201
    Given a file ".config/pip/pip.conf" with expanded content:
      """
      [global]
      index-url = {authurl}/pypi-generic/simple
      """
    When I run pip "download --no-deps --dest downloaded acme-smoke==1.0.0"
    Then the command succeeds
    And the files "dist/acme_smoke-1.0.0-py3-none-any.whl" and "downloaded/acme_smoke-1.0.0-py3-none-any.whl" are identical
    When I run uv "pip install --no-deps --target site {authurl}/pypi-generic/simple/acme-smoke/acme_smoke-1.0.0-py3-none-any.whl"
    Then the command succeeds
    And the file "site/acme_smoke/__init__.py" contains "hello from artifactweave"

  # dotnet nuget push needs NuGet's publish service, which a generic
  # repository does not have, so the smoke restores instead: from a service
  # index naming only a flat container, the one resource NuGet's restore
  # needs. allowInsecureConnections lets NuGet use the binary's plain HTTP,
  # which it otherwise refuses for restore. The project targets the SDK's
  # own framework, whose reference pack ships with the SDK, so nothing else
  # is fetched.
  @nuget
  Scenario Outline: <dotnet> restores a package from a flat container laid out in a generic repository
    Given the <dotnet> client can reach the registry
    When I PUT "/artifactory/api/repositories/nuget-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given a NuGet package "Acme.Smoke" at "1.0.0" in "nupkgs"
    And a file "index.json" with expanded content:
      """
      {"version":"3.0.0","resources":[{"@id":"{url}/nuget-generic/flat/","@type":"PackageBaseAddress/3.0.0"}]}
      """
    And a file "versions.json" with content:
      """
      {"versions":["1.0.0"]}
      """
    When I deploy the file "index.json" to "/artifactory/nuget-generic/index.json" as admin
    Then the response status is 201
    When I deploy the file "versions.json" to "/artifactory/nuget-generic/flat/acme.smoke/index.json" as admin
    Then the response status is 201
    When I deploy the file "nupkgs/Acme.Smoke.1.0.0.nupkg" to "/artifactory/nuget-generic/flat/acme.smoke/1.0.0/acme.smoke.1.0.0.nupkg" as admin
    Then the response status is 201
    Given a file "NuGet.Config" with expanded content:
      """
      <?xml version="1.0" encoding="utf-8"?>
      <configuration>
        <packageSources>
          <clear />
          <add key="aw" value="{url}/nuget-generic/index.json" allowInsecureConnections="true" />
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
      | dotnet  |
      | .NET    |
      | .NET 11 |

  # helm makes the chart and the index itself, offline: helm create, helm
  # package and helm repo index with the repository's URL, so the index's
  # urls are absolute.
  @helm
  Scenario: helm pulls a chart through a hand-made index and by its URL from a generic repository
    Given helm is available
    When I PUT "/artifactory/api/repositories/helm-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    When I run helm "create acme"
    Then the command succeeds
    When I run helm "package acme --destination charts"
    Then the command succeeds
    When I run helm "repo index charts --url {url}/helm-generic"
    Then the command succeeds
    When I deploy the files under "charts" to "/artifactory/helm-generic" as admin
    And I run helm "repo add aw {url}/helm-generic --username admin --password {password}"
    Then the command succeeds
    # helm pull writes into an existing --destination only, so the first
    # pull lands in the scenario's directory and the second unpacks, which
    # creates its directory.
    When I run helm "pull aw/acme --version 0.1.0"
    Then the command succeeds
    And the files "charts/acme-0.1.0.tgz" and "acme-0.1.0.tgz" are identical
    When I run helm "pull {url}/helm-generic/acme-0.1.0.tgz --username admin --password {password} --untar --untardir by-url"
    Then the command succeeds
    And the file "by-url/acme/Chart.yaml" contains "name: acme"

  # terraform downloads a module source whose URL ends in .zip and unpacks
  # it, without the X-Terraform-Get indirection; the credentials ride in the
  # URL. The apply proves the module it unpacked is the one deployed.
  @terraform
  Scenario: terraform init installs a module from a zip in a generic repository
    Given terraform is available
    When I PUT "/artifactory/api/repositories/tf-generic" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    Then the response status is 200
    Given a file "module/main.tf" with content:
      """
      output "greeting" {
        value = "hello from artifactweave"
      }
      """
    And a zip file "greeting-1.0.0.zip" of the files under "module"
    When I deploy the file "greeting-1.0.0.zip" to "/artifactory/tf-generic/modules/greeting-1.0.0.zip" as admin
    Then the response status is 201
    Given a file "root/main.tf" with expanded content:
      """
      module "greeting" {
        source = "{authurl}/tf-generic/modules/greeting-1.0.0.zip"
      }

      output "greeting" {
        value = module.greeting.greeting
      }
      """
    When I run terraform "-chdir=root init"
    Then the command succeeds
    And the file "root/.terraform/modules/greeting/main.tf" exists
    When I run terraform "-chdir=root apply -auto-approve"
    Then the command succeeds
    And the output contains "hello from artifactweave"
