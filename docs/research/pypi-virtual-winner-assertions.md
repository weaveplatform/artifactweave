# PyPI virtual winner assertion reliability

## Confirmed test defect

[#1712](https://github.com/weaveplatform/artifactweave/issues/1712) repairs the
existing `TestAccPypiVirtualReadsAndWarehouse` assertion. The required
[PR #1711 Linux packages job](https://github.com/weaveplatform/artifactweave/actions/runs/44373109714/job/135045401094)
rejected a correct Simple JSON response because an upload timestamp contained
`3.11`. This is a test reliability defect, not a reproduced PyPI merger or
Generic/Maven cache-clock defect.

The test searched the entire page for the losing member's `other-path`,
`broken`, or `3.11` strings. The captured response has exactly one winning
`demo-1.0-py3-none-any.whl`, the first member's URL and digest,
`requires-python` equal to `>=3.9`, and no yanked state. An unrelated version's
upload time, `2026-11-18T19:03:23.114077Z`, contains the final substring. Removing
only upload-time fields makes the old predicate accept the same four file
records. The captured CI response SHA-256 is
`f70fa5eb37b4672837e41332973b41023e9c6db3f6c40b3c14b8d73dc66d8cca`.

## Deterministic real-server loop

On merged-main runtime source
`fb67c046de93fee4da1968487a365b05ad8c4cc0`, keep the original assertion but seed
the read fixture with the existing production `item.Service.Deploy` over its
actual SQL/blob stores and PyPI format settings. Set that fixture service's
`Now` before uploads to `2026-11-18T19:03:23.114077Z`. The normal HTTP item kit
constructs a fresh service per write; no production clock hook is added or
changed to control it. Other test fixtures retain their HTTP upload controls.

```sh
GOWORK=off GOFLAGS=-p=2 go test ./test/server/artifacts/pypi \
  -run '^TestAccPypiVirtualReadsAndWarehouse$' -count=1 -v
```

The old assertion deterministically fails the JSON subtest on a valid
response, while HTML passes. The response still has one correct winner and
the expected four files. The retained controlled-response replay JSON (including its trailing newline)
has SHA-256
`1a419937351f661d1eb4a1312fa83a6bcc608c81ec94ceb15266285bf193ae5f`;
its only matches for `3.11` are upload times. Removing just those fields again
clears the old predicate without changing any file identity or owner.
The separately retained compact JSON without that newline hashes to
`b16bbab33f7e7c9272d8e975059a3376921a0a2c1c7f3af6d3a7c39e689972f9`.
These identify the retained replay encodings, not unrecorded wire bytes.

Before publication the branch normally integrated user-merged main
`df78dd3916d156de6b665ff8fd50d9cc02f669be`, including the SQLite file-path and
remote-cache changes. The same fixed-clock old predicate still fails only the
JSON subtest there, with one valid winner and four files. Final test and
producer identities are qualified to this integrated tree; the earlier
`fb67c046` results remain separately labeled. This PR's own diff remains one
test file and this research note, with no production changes. The final
documentation-only main update `3144f8a97449bd6849da2d042a964bd801504892` was
also integrated before publication; it changes neither runtime/dependency
identities nor this test.

## Correction and retained controls

Parse HTML anchors and JSON file records, then require the exact expected
four-file set and exactly one duplicate-filename winner. Check that winner's
complete first-member URL, SHA-256, Python requirement and non-yanked state.
The JSON check also requires the fixed upload timestamp, so the trigger stays
present and the regression cannot pass through wall-clock luck.

The [Simple Repository API specification](https://packaging.python.org/en/latest/specifications/simple-repository-api/)
defines HTML yanking by attribute presence and JSON yanking through an optional
boolean/reason field; absence does not mark the file yanked. Upload time is an
independent optional file attribute. The parser therefore treats an absent
JSON yanked field as non-yanked and inspects HTML's `data-yanked` presence.

The losing federated copy remains seeded with its distinct digest/path,
`>=3.11` requirement and `broken` yanked reason. Remote contribution, platform
and version union, no upstream URL leakage, conditional ETag/HEAD reads, root
index, exact file/range bytes, core metadata, Warehouse ownership/shape and
missing/write refusals remain checked. No runtime source, response shape,
client pin or repository policy changes.

After this separate test repair is merged, PR #1711 must integrate it normally
and run full CI at its new exact head. Retrying that PR's unchanged head does
not establish the assertion fix. No Artifactory trial, capture or new inferred
vendor behavior is needed for this local fixture defect.
