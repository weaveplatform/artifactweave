@terraform @terraform_getters
Feature: Both native clients consume additional module sources through the registry
  Controlled upstreams prove artifactweave behavior, not live Artifactory parity.
  Each client performs HTTPS discovery and selects a version from registry
  coordinates. A new HOME and module cache are used after the upstream stops.

  Scenario Outline: native discovery and version selection preserve source and sibling identity
    Given the module getter registry is running on "<backend>"
    Then "<client>" installs additional module getters through a "<selection>" registry cold and offline

    Examples:
      | client    | selection | backend  |
      | Terraform | remote    | sqlite   |
      | OpenTofu  | remote    | sqlite   |
      | Terraform | virtual   | sqlite   |
      | OpenTofu  | virtual   | sqlite   |
      | Terraform | remote    | postgres |
      | OpenTofu  | remote    | postgres |
      | Terraform | virtual   | postgres |
      | OpenTofu  | virtual   | postgres |
