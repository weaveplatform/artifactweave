@jf @generic_remote @ntlm_proxy
Feature: Native remote downloads authenticate the configured NTLM proxy
  The controlled proxy verifies NTLMv2 proof, domain and workstation on one
  connection; independent origin records prove the native client received bytes.
  The client is required in this CI-owned scenario, including local executions.

  Scenario: JFrog CLI downloads cold upstream files through one NTLM connection
    Given the registry is running
    And the NTLM proxy native client is available
    And a generic upstream serves "/first.bin" with content "first NTLM native bytes"
    And a generic upstream serves "/second.bin" with content "second NTLM native bytes"
    And an NTLM proxy protects the generic upstream
    When I PATCH "/artifactory/api/system/configuration" as admin with expanded YAML:
      """
      proxies:
        ntlm:
          host: "{proxyhost}"
          port: {proxyport}
          username: proxy-user
          password: proxy-fixture-password
          domain: CORP
          ntHost: ARTIFACTWEAVE
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/remote" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{upstream}","proxy":"ntlm"}
      """
    Then the response status is 200
    When I run jf "rt direct-download remote/first.bin first/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the file "first/first.bin" contains "first NTLM native bytes"
    And the generic upstream received 1 GET requests for "/first.bin"
    When I run jf "rt direct-download remote/second.bin second/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the file "second/second.bin" contains "second NTLM native bytes"
    And the generic upstream received 1 GET requests for "/second.bin"
    And the NTLM proxy verified one domain and workstation exchange
    And the server answered no request with 500 since the last command
