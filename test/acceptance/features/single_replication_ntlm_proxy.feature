@replication @replication_ntlm_proxy
Feature: Single replication pushes through a configured NTLM proxy
  The owning application selects a descriptor-configured NTLMv2 proxy, by
  platform default or explicit reference, for its push target. An independent
  proxy verifier checks proof, domain and workstation; the separately owned
  peer's storage proves the transferred bytes, properties and deletes.

  Scenario Outline: Ordinary push replication authenticates its NTLM proxy
    Given two fixture-owned replication instances are running on "<backend>"
    And an NTLM proxy protects the replication peer
    And the "<selection>" NTLM proxy is configured for replication
    Then ordinary "push" replication transfers scoped bytes, properties and deletes between the instances
    And the NTLM proxy verified every replication tunnel

    Examples:
      | backend  | selection |
      | sqlite   | default   |
      | sqlite   | explicit  |
      | postgres | default   |
      | postgres | explicit  |
