@jf
Feature: JFrog CLI round trip
  P3's exit criterion: the JFrog CLI uploads, searches, downloads, copies,
  moves, sets and deletes properties on, and deletes a tree, unchanged. The
  tree mirrors the seeds in pkg/aql/aqltest, so the AQL the CLI sends here
  is what make capture-aql saves as that package's fixtures.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/libs-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And I PUT "/artifactory/api/repositories/libs-copy" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And a file "org/acme/app-1.0.jar" with content:
      """
      the application
      """
    And a file "org/acme/app-1.0.pom" with content:
      """
      <project/>
      """
    And a file "org/acme/lib/util-2.0.jar" with content:
      """
      a utility library
      """
    And a file "readme.md" with content:
      """
      read me
      """
    And a file "big.bin" of 65536 bytes

  Scenario: upload, search, download, copy, move, properties and delete
    When I run jf "rt u org/* libs-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u readme.md libs-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u big.bin libs-local/big/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    # The second upload of the same 64 KiB is a checksum deploy that hits.
    When I run jf "rt u big.bin libs-local/again/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 201 to PUT "/artifactory/libs-local/again/big.bin"
    And the server never answered 404 to PUT "/artifactory/libs-local/again/big.bin"

    When I run jf "rt s libs-local/org/acme/*.jar --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "libs-local/org/acme/app-1.0.jar"
    And the output contains "libs-local/org/acme/lib/util-2.0.jar"
    And the output does not contain "app-1.0.pom"

    When I run jf "rt dl libs-local/org/ out/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "org/acme/app-1.0.jar" and "out/org/acme/app-1.0.jar" are identical
    And the files "org/acme/lib/util-2.0.jar" and "out/org/acme/lib/util-2.0.jar" are identical
    When I run jf "rt dl libs-local/big/big.bin split/ --flat --split-count=2 --min-split=16 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "big.bin" and "split/big.bin" are identical
    And the server answered 206 to GET "/artifactory/libs-local/big/big.bin"

    When I run jf "rt sp libs-local/org/acme/*.jar release=1.0 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt s libs-local/ --props release=1.0 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "libs-local/org/acme/app-1.0.jar"
    And the output does not contain "readme.md"
    When I run jf "rt delp libs-local/org/acme/*.jar release --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt s libs-local/ --props release=1.0 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output does not contain "app-1.0.jar"

    When I run jf "rt cp libs-local/org/acme/app-1.0.jar libs-copy/app/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/libs-copy/app/app-1.0.jar" as admin
    Then the response status is 200
    And the response body contains "the application"
    When I run jf "rt mv libs-local/readme.md libs-copy/docs/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/libs-copy/docs/readme.md" as admin
    Then the response status is 200
    When I GET "/artifactory/libs-local/readme.md" as admin
    Then the response status is 404

    When I run jf "rt del libs-local/org/acme/lib/ --quiet --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt s libs-local/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output does not contain "util-2.0.jar"
    And the output contains "libs-local/org/acme/app-1.0.jar"

  # The file-spec shapes of pkg/aql/aqltest/testdata, one command each, so
  # make capture-aql refreshes every fixture from the CLI's own queries.
  Scenario: the query shapes the fixtures capture
    Given I PUT "/artifactory/api/repositories/remote" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"http://127.0.0.1:9/"}
      """
    When I run jf "rt u org/acme/app-1.0.jar libs-local/org/acme/ --flat --target-props build.name=app;release=1.0 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u org/acme/app-1.0.pom libs-local/org/acme/ --flat --target-props build.name=app --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u org/acme/lib/util-2.0.jar libs-local/org/acme/lib/ --flat --target-props release=2.0;owner=team-a --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt u readme.md libs-local/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds

    # search_recursive
    When I run jf "rt s libs-local/org/acme/*.jar --url {url} --user admin --password {password}"
    Then the command succeeds
    # search_props_sort
    When I run jf "rt s libs-local/* --props build.name=app --sort-by created --sort-order desc --limit 10 --url {url} --user admin --password {password}"
    Then the command succeeds
    # search_root_offset_limit
    When I run jf "rt s libs-local/* --sort-by name --offset 1 --limit 2 --url {url} --user admin --password {password}"
    Then the command succeeds
    # download_exclusions: each pattern becomes an $or of negations, so the
    # pom alone stays behind.
    When I run jf "rt dl libs-local/org/acme* dl/ --exclusions *.pom --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "org/acme/app-1.0.jar" and "dl/org/acme/app-1.0.jar" are identical
    And the files "org/acme/lib/util-2.0.jar" and "dl/org/acme/lib/util-2.0.jar" are identical
    And the file "dl/org/acme/app-1.0.pom" does not exist
    # download_remote: the CLI names the remote by its own key, the query
    # finds the cached jar in remote-cache, and the CLI downloads it from there.
    Given the remote "remote" caches the file "org/acme/app-1.0.jar" at "org/acme/app-1.0.jar"
    When I run jf "rt dl remote/*.jar remote-dl/ --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "org/acme/app-1.0.jar" and "remote-dl/org/acme/app-1.0.jar" are identical
    And the server answered 200 to GET "/artifactory/remote-cache/org/acme/app-1.0.jar"
    # set_props_with_props
    When I run jf "rt sp libs-local/* checked=yes --props release=* --url {url} --user admin --password {password}"
    Then the command succeeds
    # copy_move_file
    When I run jf "rt cp libs-local/org/acme/app-1.0.jar libs-copy/ --url {url} --user admin --password {password}"
    Then the command succeeds
    # The --build shapes: the jar and the pom become app/1's artifacts, as
    # they are in pkg/aql/aqltest's build seeds.
    When I run jf "rt u org/acme/app-1.0.* libs-local/org/acme/ --flat --build-name=app --build-number=1 --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt bp app 1 --url {url} --user admin --password {password}"
    Then the command succeeds
    # search_build, search_build_properties
    When I run jf "rt s libs-local/ --build=app/1 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the output contains "libs-local/org/acme/app-1.0.pom"
    # download_build
    When I run jf "rt dl libs-local/ build-dl/ --build=app/1 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "org/acme/app-1.0.pom" and "build-dl/org/acme/app-1.0.pom" are identical
    # delete_any
    When I run jf "rt del libs-local/org/* --quiet --url {url} --user admin --password {password}"
    Then the command succeeds
