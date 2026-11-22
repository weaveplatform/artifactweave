@jf @generic_remote @proxy_redirects
Feature: Proxy redirect credentials during native content downloads
  Controlled proxy/origin observations prove local effects, not observed vendor behavior.

  Scenario: JFrog CLI downloads permitted redirects and refuses stale or malformed credentials
    Given the controlled proxy redirect upstream is ready
    And the registry is running
    And the JFrog CLI is available
    When I apply the proxy redirect configuration:
      """
      proxies:
        redirect-proxy:
          host: "http://[::1]"
          port: {redirect-port}
          username: proxy-user
          password: proxy-secret
          redirectedToHosts: "other.example; 127.0.0.1,example.org"
      """
    Then the response status is 200
    When I GET "/artifactory/api/system/configuration" as admin
    Then the response status is 200
    And the XML field "proxies/proxy[key=redirect-proxy]/redirectedToHosts" equals "other.example; 127.0.0.1,example.org"
    And the response body does not contain "proxy-secret"
    When I PUT "/artifactory/api/repositories/redirect-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{redirect-origin}","proxy":"redirect-proxy","username":"repo-user","password":"repo-secret","bypassHeadRequests":true,"customHttpHeaders":[{"name":"X-Origin-Secret","value":"origin-only","sensitive":true}]}
      """
    Then the response status is 200
    When I run jf "rt direct-download redirect-remote/allowed.bin allowed/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the file "allowed/allowed.bin" contains "proxy redirected bytes"
    When I run jf "rt direct-download redirect-remote/same.bin same/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the file "same/same.bin" contains "same origin bytes"
    When I apply the proxy redirect configuration:
      """
      proxies:
        redirect-proxy:
          redirectedToHosts: "unlisted.example"
      """
    Then the response status is 200
    When I run jf "rt direct-download redirect-remote/refused.bin refused/ --flat --fail-no-op --retries=0 --url {url} --user admin --password {password}"
    Then the command fails
    When I GET "/artifactory/api/storage/redirect-remote-cache/refused.bin" as admin
    Then the response status is 404
    When I apply the proxy redirect configuration:
      """
      proxies:
        redirect-proxy:
          redirectedToHosts: ""
      """
    Then the response status is 200
    When I run jf "rt direct-download redirect-remote/removed.bin removed/ --flat --fail-no-op --retries=0 --url {url} --user admin --password {password}"
    Then the command fails
    When I GET "/artifactory/api/storage/redirect-remote-cache/removed.bin" as admin
    Then the response status is 404
    When I apply the proxy redirect configuration:
      """
      proxies:
        redirect-proxy:
          redirectedToHosts: "127.0.0.1"
          password: rotated-proxy-secret
      """
    Then the response status is 200
    When I run jf "rt direct-download redirect-remote/rotated.bin rotated/ --flat --fail-no-op --retries=0 --url {url} --user admin --password {password}"
    Then the command fails
    When I GET "/artifactory/api/storage/redirect-remote-cache/rotated.bin" as admin
    Then the response status is 404
    When I apply the proxy redirect configuration:
      """
      proxies:
        redirect-proxy:
          redirectedToHosts: "*.example"
          password: proxy-secret
      """
    Then the response status is 200
    When I run jf "rt direct-download redirect-remote/malformed.bin malformed/ --flat --fail-no-op --retries=0 --url {url} --user admin --password {password}"
    Then the command fails
    When I GET "/artifactory/api/storage/redirect-remote-cache/malformed.bin" as admin
    Then the response status is 404
    And the proxy redirect credential observations are correct
