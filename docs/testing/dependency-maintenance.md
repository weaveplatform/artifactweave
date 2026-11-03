# Dependency references

Change a dependency's canonical pin, then run `make deps-sync`. This updates
current client labels, both client inventories, secondary Dockerfile version
references, generated recipes and their evidence fingerprints in one operation.
CI runs the same drift checks for every profile, including documentation changes.

Dependabot PRs receive the companion commit automatically after their first
Quality gate finishes, including when stale references made that gate fail.
The repository App commits the generated changes to the dependency branch,
triggering a new gate. Auto-merge checks the new head's result. Acceptance client
updates still require an eligible review and manual merge. The scheduled Go and
JFrog provider refresh jobs run the generator before opening their PRs.

`dependency-references.yml` also reconciles the current default branch after
every push, including dependency merges. It regenerates from the merged tree
and opens an App-authored `deps/references/<base-sha>` repair PR only when a
declared output is stale. Manual dispatch provides recovery. The auto-merge
workflow calls this reconciliation after its merge step too, covering workflow
dependency merges made with `GITHUB_TOKEN`, which suppresses push workflows.
The App-created repair triggers CI and follows the existing review and merge
rules; it does not push directly to protected main or bypass approval. Main
can report drift until that repair merges. There is no manual regeneration step.

## Sources and outputs

[The catalog](../../tools/depsync/catalog.json) connects each reference to a
canonical pin in a Dockerfile, client setup action, workflow, Go directive or
deployment tool script. It contains selectors and label templates, not another
copy of the version numbers. The two inventory templates live beside it.

- Docker `FROM` references supply the image tag and digest. Explicit install
  commands or download URLs supply separately installed client versions. Eclipse
  uses the complete release directory (version and build timestamp) as its
  canonical artifact identity; both architecture filenames follow its version.
- Recipe `client` fields are generated from those pins; the remaining recipe
  instructions remain authored in `docs/client-recipes/src`. The sbt launcher
  prerequisite is another generated reference; prose prerequisites refer to the
  client label instead of repeating its version.
- Build assertions, archive filenames and installation paths follow the same
  pin where the catalog declares a secondary reference. Native sbt fixture
  launcher settings and standalone Hub/Xet version assertions follow these pins too. Workflow-installed
  Hugging Face and Conan clients use one version variable per client.
- Every recipe and acceptance Dockerfile must be registered. A missing or
  ambiguous pin, unknown template variable, new unregistered client, or changed
  secondary-reference structure fails visibly.
- A bundled client's precise patch version cannot always be inferred from an
  image tag. For example, `maven:3` and `.NET SDK:10.0` do not state the complete
  Maven or SDK version. Current labels identify the immutable image and native
  CI records its runtime version; the updater never copies an old observed
  version onto a newly updated image.

Digests and release archive checksums remain authoritative dependency inputs.
The updater does not invent a checksum or claim that an updated client passed
before its tests run. Historical qualification reports and research retain
their original versions and tested commits. Changes to client behavior still
require updating the recipe instructions and native tests.

To add a new client, add its canonical selectors and recipe/image templates to
the catalog, then run `make deps-sync`. A new current reference belongs in the
catalog or should link to the generated inventory instead of copying a version.

## Automation boundary

The privileged `workflow_run` job checks out only the default branch. It builds
the trusted recipe renderer before obtaining the App write token, reads PR git
blobs as data into a temporary directory, and runs no PR scripts, hooks, Go
dependencies or template code. Only open, same-repository dependency PRs from
the expected bot and branch are eligible. Automation/catalog changes are not
accepted as dependency inputs. Symlink inputs and path traversal are rejected.

Before publishing, it checks the PR's state and exact head again. The generated
commit has that head as its sole parent, and the ref update forbids force; a
concurrent update cannot be overwritten. Terraform README generation runs
first, avoiding competing writers. If that job advances the branch, the next
gate run synchronizes the remaining references.

Post-merge repairs use an immutable main SHA as their parent. A concurrent main
update defers publication to the next run. Retries reuse the same repair and
never force-push; an interrupted branch creation is recovered only if its tree
and parent match the expected repair. Superseded generated-only bot repairs are
closed after a replacement is published, or when main already needs no repair.
An unchanged merged tree produces no commit, preventing regeneration loops.

These boundaries follow GitHub's guidance for
[privileged workflow events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_run)
and [untrusted code](https://docs.github.com/en/actions/reference/security/secure-use#mitigating-the-risks-of-untrusted-code-checkout).

Run the offline regression tests with:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools/depsync -p '*_test.py'
python3 tools/depsync/sync.py --check
GOWORK=off go run ./tools/clientrecipes -check
```
