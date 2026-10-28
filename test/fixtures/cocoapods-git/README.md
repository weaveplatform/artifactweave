# Controlled CocoaPods Git fixtures

Run `python3 test/fixtures/cocoapods-git/generate.py` with native Git to regenerate
these small smart-HTTP pack fixtures. No network or Go encoder is used. Fixed
identities and timestamps make the objects reproducible.

`root.pack` includes a main-branch commit, an annotated `1.0.0` tag, an Objective-C
source, and `.gitmodules` with relative `../dep.git` at `Vendor`. Its gitlink pins
`submodule.pack`, which contains `Dep.m`. `refs.json` records their object IDs.
The root and submodule source files are original test data for this repository.
`updated.pack` moves the same annotated tag to a new commit with changed source
bytes, allowing refresh tests to distinguish the two generations.

Tests construct the Git pkt-line advertisements and NAK upload response around
these packs, then exercise the actual remote HTTP transport and native `pod`
client. Pack decoder adversarial fixtures are separately built by hand.
