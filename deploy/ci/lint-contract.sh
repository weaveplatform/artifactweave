#!/usr/bin/env bash
# The reference-deployment contract (decision 0030): every Terraform root
# under deploy/ is checked against the rules for its kind, which its path
# gives it, and each finding names the rule it breaks.
#
#   lint-contract.sh [--verbose] [repo]  lint a repository (default: this one)
#   lint-contract.sh --self-test         check the linter against fixtures it
#                                        generates
#
# | Path                                        | Kind      | Rules    |
# |---------------------------------------------|-----------|----------|
# | deploy/platform/terraform/<cloud>/<name>    | scenario  | S1-S11   |
# | deploy/platform/terraform/modules/<c>/<cap> | module    | M1-M6 S6 |
# | deploy/artifactweave/terraform/<name>       | app layer | A1-A4    |
# | deploy/ci/terraform/<cloud>                 | bootstrap | B1       |
# | deploy/                                     | global    | G1 G2    |
#
# Roots listed in deploy/ci/contract-pending.txt report their findings as
# warnings until they are ported to the contract; every other finding fails.
# The checks read the sources with grep and awk only, so they run before
# check.sh fetches any tool. They rely on terraform fmt's layout (top-level
# blocks unindented, their attributes two spaces in), which check.sh's fmt
# step enforces. There is no set -e: a grep that finds nothing is an answer
# here, and -e would end the run at the first one inside a substitution.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
emdash=$'\xe2\x80\x94'

usage() {
  echo "usage: $0 [--verbose] [repo] | --self-test" >&2
  exit 2
}

# ---------------------------------------------------------------- readers

# h2 prints a Markdown file's H2 headings, skipping fenced code.
h2() { awk '/^```/ { f = !f; next } !f && /^## / { print }' "$1"; }

# section prints the lines under a heading (exact text, any level) up to the
# next heading of the same or a higher level.
section() {
  awk -v h="$2" '
    /^```/ { f = !f; if (on) print; next }
    !f && /^#+ / {
      n = index($0, " ") - 1
      if (on && n <= level) on = 0
      if ($0 == h) { on = 1; level = n; next }
    }
    on { print }' "$1"
}

# line_of prints the first line number holding the fixed string, or nothing.
line_of() { awk -v s="$2" 'index($0, s) { print NR; exit }' "$1" 2>/dev/null; }

# tf lists a root's top-level .tf files, one per line.
tf() { ls "$1"/*.tf 2>/dev/null || true; }

# variables prints "name has_type has_description has_default" for each
# variable block in the files given.
variables() {
  [ $# -gt 0 ] || return 0
  awk '
    /^variable "/ { match($0, /"[^"]+"/); n = substr($0, RSTART + 1, RLENGTH - 2); t = d = df = 0; on = 1; next }
    on && /^  type[ ]*=/ { t = 1 }
    on && /^  description[ ]*=/ { d = 1 }
    on && /^  default[ ]*=/ { df = 1 }
    on && /^}/ { print n, t, d, df; on = 0 }' "$@"
}

# outputs prints "file<TAB>name<TAB>description line" for each output block.
outputs() {
  [ $# -gt 0 ] || return 0
  awk '
    /^output "/ { match($0, /"[^"]+"/); n = substr($0, RSTART + 1, RLENGTH - 2); d = ""; on = 1; next }
    on && /^  description[ ]*=/ { d = $0 }
    on && /^}/ { print FILENAME "\t" n "\t" d; on = 0 }' "$@"
}

# module_sources prints the source of each module block in the files given.
module_sources() {
  [ $# -gt 0 ] || return 0
  awk '
    /^module "/ { on = 1; next }
    on && /^  source[ ]*=/ { match($0, /"[^"]*"/); print substr($0, RSTART + 1, RLENGTH - 2) }
    on && /^}/ { on = 0 }' "$@"
}

# providers prints "file<TAB>name<TAB>attribute" for every top-level
# attribute of each provider block, and "file<TAB>name<TAB>" for the block.
providers() {
  [ $# -gt 0 ] || return 0
  awk '
    /^provider "/ { match($0, /"[^"]+"/); p = substr($0, RSTART + 1, RLENGTH - 2); on = 1; print FILENAME "\t" p "\t"; next }
    on && /^  [a-z_]+[ ]*=/ { a = $0; sub(/^  /, "", a); print FILENAME "\t" p "\t" a }
    on && /^}/ { on = 0 }' "$@"
}

# required_providers prints "source<TAB>version" for each required_providers
# entry, whether it is written on one line or several.
required_providers() {
  [ $# -gt 0 ] || return 0
  awk '
    function count(s, c, t) { t = s; return gsub(c, "", t) }
    function value(k, s, v) {
      if (!match(s, k "[ ]*=[ ]*\"[^\"]*\"")) return ""
      v = substr(s, RSTART, RLENGTH); sub(/^[^"]*"/, "", v); sub(/"$/, "", v); return v
    }
    /required_providers[ ]*=?[ ]*\{/ { on = 1; depth = 1; next }
    on {
      o = count($0, "\\{"); c = count($0, "\\}")
      if (depth == 1 && o > 0) buf = ""
      buf = buf " " $0
      depth += o - c
      if (depth == 1 && (o > 0 || c > 0)) { print value("source", buf) "\t" value("version", buf); buf = "" }
      if (depth <= 0) on = 0
    }' "$@"
}

# test_variables prints each name set in a variables block of the test
# files given.
test_variables() {
  [ $# -gt 0 ] || return 0
  awk '
    function count(s, c, t) { t = s; return gsub(c, "", t) }
    !on && /^[ \t]*variables[ \t]*\{/ { on = 1; depth = 1; next }
    on {
      if (depth == 1 && match($0, /^[ \t]*[A-Za-z_][A-Za-z0-9_]*[ \t]*=/)) {
        n = substr($0, RSTART, RLENGTH); gsub(/[ \t=]/, "", n); print n
      }
      depth += count($0, "\\{") - count($0, "\\}")
      if (depth <= 0) on = 0
    }' "$@"
}

# trivy_ids prints the #trivy:ignore IDs in the files given.
trivy_ids() {
  [ $# -gt 0 ] || return 0
  grep -h -o 'trivy:ignore:[A-Za-z0-9-]*' "$@" 2>/dev/null | sed 's/^trivy:ignore://' | sort -u || true
}

# ------------------------------------------------------------------- lint

lint() {
  local repo=$1 verbose=$2
  local found pending deny roots r kind cloud n status=0
  found=$(mktemp)
  deny=$(mktemp)
  pending=$(mktemp)

  # v records one finding: the root (or deploy for a global one), the
  # rule's ID and what is wrong.
  v() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$found"; }

  # The deny-list of S6 and S8: every value in lab.mk and the lab's fixed
  # names.
  if [ -f "$repo/deploy/ci/lab.mk" ]; then
    sed -n -E 's/^LAB_[A-Z0-9_]+[ ]*[?:]?=[ ]*//p' "$repo/deploy/ci/lab.mk" | sed -E 's/[ ]+$//' | grep -v '^$' >>"$deny" || true
  fi
  printf '%s\n' artifactweave-conformance artifactweave-deployer artifactweave-bootstrap-rg \
    artifactweave-scenarios-rg artifactweave-scenario-boundary /artifactweave-scenarios/ \
    weaveplatform.run >>"$deny"

  if [ -f "$repo/deploy/ci/contract-pending.txt" ]; then
    sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$repo/deploy/ci/contract-pending.txt" | grep -v '^$' >"$pending" || true
  fi

  roots=$(cd "$repo" && find deploy -name '*.tf' -not -path '*/.terraform/*' -exec dirname {} \; 2>/dev/null | sort -u)

  for r in $roots; do
    n=$(echo "$r" | awk -F/ '{ print NF }')
    kind=unknown
    cloud=
    case $r in
    deploy/platform/terraform/modules/*)
      [ "$n" = 6 ] && kind=module && cloud=$(echo "$r" | cut -d/ -f5)
      ;;
    deploy/platform/terraform/*)
      cloud=$(echo "$r" | cut -d/ -f4)
      case $cloud in aws | gcp | azure | multicloud) [ "$n" = 5 ] && kind=scenario ;; esac
      ;;
    deploy/artifactweave/terraform/*) [ "$n" = 4 ] && kind=app ;;
    deploy/ci/terraform/*) [ "$n" = 4 ] && kind=bootstrap && cloud=$(echo "$r" | cut -d/ -f4) ;;
    esac
    case $kind in
    scenario) lint_scenario "$repo" "$r" "$cloud" ;;
    module) lint_module "$repo" "$r" "$cloud" ;;
    app) lint_app "$repo" "$r" ;;
    bootstrap) lint_bootstrap "$repo" "$r" "$cloud" ;;
    *) v "$r" layout "a Terraform root where no contract profile applies (decision 0030, Layout)" ;;
    esac
  done
  lint_global "$repo"

  report "$roots" "$found" "$pending" "$verbose" || status=1
  rm -f "$found" "$deny" "$pending"
  return $status
}

# owner prints the root a path lies in, or deploy when it lies in none.
owner() {
  local p=$1 r
  for r in $roots; do
    case $p in "$r"/*) echo "$r" && return ;; esac
  done
  echo deploy
}

lint_global() {
  local repo=$1 f rel
  [ -f "$repo/deploy/.gitignore" ] || v deploy G1 "deploy/.gitignore is missing"
  while IFS= read -r f; do
    rel=${f#"$repo"/}
    [ "$rel" = deploy/.gitignore ] && continue
    v "$(owner "$rel")" G1 "$rel: use deploy/.gitignore instead of a .gitignore per root"
  done < <(find "$repo/deploy" -name .gitignore -not -path '*/.terraform/*')
  while IFS= read -r f; do
    rel=${f#"$repo"/}
    v "$(owner "$rel")" G2 "$rel line $(line_of "$f" "$emdash"): an em-dash"
  done < <(LC_ALL=C grep -rl -F "$emdash" --include='*.md' "$repo/deploy" 2>/dev/null || true)
}

# require_files records rule for each file missing from the root.
require_files() {
  local d=$1 r=$2 rule=$3 f
  shift 3
  for f in "$@"; do
    [ -e "$d/$f" ] || v "$r" "$rule" "$f is missing"
  done
  return 0
}

# readme_headings records rule unless the README's H2 headings are the
# template's, in order.
readme_headings() {
  local readme=$1 template=$2 r=$3 rule=$4 want got
  [ -f "$readme" ] || return 0
  want=$(h2 "$template" | tr '\n' '|')
  got=$(h2 "$readme" | tr '\n' '|')
  [ "$want" = "$got" ] || v "$r" "$rule" "README.md's H2 headings are not ${template##*/}'s, in order: want ${want%|}, got ${got%|}"
}

# denied records rule for each lab literal in the files given.
denied() {
  local r=$1 rule=$2 f hit
  shift 2
  for f in "$@"; do
    [ -f "$f" ] || continue
    while IFS= read -r hit; do
      v "$r" "$rule" "${f##*/} names the lab's $hit"
    done < <(grep -o -F -f "$deny" "$f" 2>/dev/null | sort -u)
  done
}

lint_scenario() {
  local repo=$1 r=$2 cloud=$3 d files f n t ds src x vars ci_vars mk_vars id sec
  d=$repo/$r
  files=$(tf "$d")

  # S1: files.
  require_files "$d" "$r" S1 versions.tf variables.tf main.tf outputs.tf harness.tf README.md \
    .terraform.lock.hcl Makefile ci-vars.sh
  ls "$d"/tests/*.tftest.hcl >/dev/null 2>&1 || v "$r" S1 "tests/*.tftest.hcl is missing"
  for f in backend.tf .gitignore credentials.sh up.sh smoke.sh compose.yaml; do
    [ -e "$d/$f" ] && v "$r" S1 "$f is not allowed"
  done
  for f in "$d"/*.tfvars "$d"/*.tfvars.*; do
    [ -e "$f" ] && v "$r" S1 "${f##*/} is not allowed"
  done

  [ -n "$files" ] || return 0
  # shellcheck disable=SC2086 # file lists are word-split on purpose
  {
    # S2: composition.
    while IFS= read -r t; do
      case $t in random_id | random_string | azurerm_resource_group) ;;
      *) v "$r" S2 "declares a $t resource; capabilities come from modules" ;;
      esac
    done < <(grep -h -o -E '^resource "[^"]+"' $files | cut -d'"' -f2 | sort -u)
    grep -h -q -E '^data "' $files && v "$r" S2 "has a data block; a scenario looks nothing up"
    while IFS= read -r src; do
      [ -n "$src" ] || continue
      case $src in
      ../../modules/*/*)
        x=$(echo "$src" | cut -d/ -f4)
        if [ "$cloud" != multicloud ] && [ "$x" != "$cloud" ]; then
          v "$r" S2 "module source $src is another cloud's module"
        fi
        ;;
      *) v "$r" S2 "module source $src is not ../../modules/<cloud>/<capability>" ;;
      esac
    done < <(module_sources $files)

    # S3: providers.
    while IFS=$'\t' read -r f n x; do
      [ "${f##*/}" = versions.tf ] || { [ -z "$x" ] && v "$r" S3 "provider \"$n\" is in ${f##*/}, not versions.tf"; }
      case $x in alias*) v "$r" S3 "provider \"$n\" has an alias" ;; esac
    done < <(providers $files)
    x=$(sed -n -E 's/^[ ]*required_version[ ]*=[ ]*">=[ ]*1\.([0-9]+).*/\1/p' "$d/versions.tf" 2>/dev/null | head -1)
    if [ -z "$x" ] || [ "$x" -lt 9 ]; then
      v "$r" S3 "versions.tf's required_version is not \">= 1.9\""
    fi
    if providers $files | grep -q $'\tazurerm\t$'; then
      providers $files | grep -q -E $'\tazurerm\tstorage_use_azuread[ ]*=[ ]*true' ||
        v "$r" S3 "provider \"azurerm\" lacks storage_use_azuread = true"
      providers $files | grep -q -E $'\tazurerm\tresource_provider_registrations[ ]*=[ ]*"none"' ||
        v "$r" S3 "provider \"azurerm\" lacks resource_provider_registrations = \"none\""
    fi

    # S4: inputs.
    vars=$(variables "$d/variables.tf" 2>/dev/null || true)
    while read -r n t ds _; do
      [ -n "$n" ] || continue
      [ "$t" = 1 ] || v "$r" S4 "variable \"$n\" has no type"
      [ "$ds" = 1 ] || v "$r" S4 "variable \"$n\" has no description"
      case $n in
      scenario_labels | conformance_* | bootstrap_resource_group | register_resource_providers | \
        boundary_policy_name | scenario_role_path | primary_* | secondary_*)
        v "$r" S4 "variable \"$n\" exists only for the harness or the lab"
        ;;
      esac
      case $cloud:$n in
      aws:location | aws:locations | gcp:region | gcp:regions | azure:region | azure:regions)
        v "$r" S4 "variable \"$n\" is not $cloud's word for placement"
        ;;
      multicloud:region | multicloud:regions | multicloud:location | multicloud:locations)
        v "$r" S4 "variable \"$n\" names no cloud; a multicloud scenario places with aws_region, gcp_location and azure_location"
        ;;
      esac
    done <<<"$vars"
    required() { # name [name ...]: one of them is declared, and has no default
      local m l
      for m in "$@"; do
        l=$(echo "$vars" | awk -v m="$m" '$1 == m')
        [ -n "$l" ] || continue
        [ "$(echo "$l" | cut -d' ' -f4)" = 0 ] || v "$r" S4 "variable \"$m\" is required and must have no default"
        return 0
      done
      v "$r" S4 "variable \"$1\" is missing"
    }
    required name
    case $cloud in
    aws) required region regions && required app_role_arn ;;
    gcp) required location locations && required app_service_account ;;
    azure) required location locations && required app_principal_id ;;
    # Every cloud-specific input names its cloud, so one root holds all three.
    multicloud)
      required aws_region && required gcp_location && required azure_location
      required app_role_arn && required app_service_account && required app_principal_id
      ;;
    esac
    case $cloud in
    gcp | multicloud) echo "$vars" | awk '$1 == "project_id" && $4 == 0 { ok = 1 } END { exit !ok }' ||
      v "$r" S3 "the account scope, variable \"project_id\", must be a required input" ;;
    esac
    case $cloud in
    azure | multicloud) echo "$vars" | awk '$1 == "subscription_id" && $4 == 0 { ok = 1 } END { exit !ok }' ||
      v "$r" S3 "the account scope, variable \"subscription_id\", must be a required input" ;;
    esac

    # S5: outputs.
    while IFS=$'\t' read -r f n ds; do
      case ${f##*/} in
      outputs.tf)
        case $n in expires_at | mirror_members) v "$r" S5 "output \"$n\" is for the harness: move it to harness.tf" ;; esac
        ;;
      harness.tf)
        echo "$ds" | grep -q -E '^  description[ ]*=[ ]*"For the test harness:' ||
          v "$r" S5 "harness.tf's output \"$n\" description does not begin \"For the test harness:\""
        ;;
      *) v "$r" S5 "output \"$n\" is in ${f##*/}, not outputs.tf or harness.tf" ;;
      esac
    done < <(outputs $files)
    for n in artifactweave_config storage_host; do
      outputs "$d/outputs.tf" 2>/dev/null | cut -f2 | grep -q -x "$n" || v "$r" S5 "outputs.tf lacks output \"$n\""
    done
    outputs "$d/harness.tf" 2>/dev/null | cut -f2 | grep -q -x expires_at || v "$r" S5 "harness.tf lacks output \"expires_at\""

    # S6: lab values.
    denied "$r" S6 $files

    # S11: moved blocks.
    for f in $files; do
      [ "${f##*/}" = moved.tf ] && continue
      grep -q -E '^moved[ ]*\{' "$f" && v "$r" S11 "${f##*/} has a moved block; moved blocks live in moved.tf"
    done
    if [ -f "$d/moved.tf" ]; then
      awk '/^moved[ ]*\{/ { if (prev !~ /^[ ]*(#|\/\/)/) bad = 1 } /[^ ]/ { prev = $0 } END { exit bad }' "$d/moved.tf" ||
        v "$r" S11 "a moved block in moved.tf lacks a comment naming the state it keeps"
    fi
  }

  # S7: ci-vars.sh.
  ci_vars=
  if [ -f "$d/ci-vars.sh" ]; then
    grep -v -E '^[[:space:]]*(#.*)?$' "$d/ci-vars.sh" | grep -v -E '^export TF_VAR_[A-Za-z0-9_]+=.+$' >/dev/null &&
      v "$r" S7 "ci-vars.sh has a line that is neither a comment nor export TF_VAR_<name>=…"
    ci_vars=$(sed -n -E 's/^export TF_VAR_([A-Za-z0-9_]+)=.*/\1/p' "$d/ci-vars.sh" | sort -u)
    for n in $ci_vars; do
      echo "$vars" | awk -v m="$n" '$1 == m { ok = 1 } END { exit !ok }' ||
        v "$r" S7 "ci-vars.sh sets TF_VAR_$n, but variables.tf declares no \"$n\""
    done
    # shellcheck disable=SC2016 # the line as written, unexpanded
    grep -q -x -F 'export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"' "$d/ci-vars.sh" ||
      v "$r" S7 "ci-vars.sh does not set TF_VAR_name=\"\${NAME_PREFIX}\${INSTANCE}\""
    case $cloud in
    gcp) x=labels ;;
    multicloud) x='(tags|labels)' ;;
    *) x=tags ;;
    esac
    grep -q -E "^export TF_VAR_$x=.*SCENARIO_LABELS" "$d/ci-vars.sh" ||
      v "$r" S7 "ci-vars.sh does not pass \$SCENARIO_LABELS as the $cloud scenario's tags or labels"
    if echo "$vars" | awk '$1 == "force_destroy" { ok = 1 } END { exit !ok }'; then
      grep -q -x -F 'export TF_VAR_force_destroy=true' "$d/ci-vars.sh" ||
        v "$r" S7 "ci-vars.sh does not set TF_VAR_force_destroy=true"
    fi
  fi

  # S8: Makefile.
  mk_vars=
  if [ -f "$d/Makefile" ]; then
    grep -v -E '^[[:space:]]*(#.*)?$' "$d/Makefile" | tail -1 | grep -q -E '^include .*deploy/ci/scenario\.mk$' ||
      v "$r" S8 "the Makefile does not end by including deploy/ci/scenario.mk"
    denied "$r" S8 "$d/Makefile"
    mk_vars=$(grep -v -E '^[[:space:]]*#' "$d/Makefile" | grep -o -E '(-var[ =]+|TF_VAR_)[A-Za-z_][A-Za-z0-9_]*' |
      sed -E 's/^(-var[ =]+|TF_VAR_)//' | sort -u || true)
    for n in $mk_vars; do
      echo "$vars" | awk -v m="$n" '$1 == m { ok = 1 } END { exit !ok }' ||
        v "$r" S8 "the Makefile sets $n, but variables.tf declares no \"$n\""
    done
  fi

  # S9: README.
  f=$d/README.md
  if [ -f "$f" ]; then
    readme_headings "$f" "$repo/deploy/platform/SCENARIO-TEMPLATE.md" "$r" S9
    x=$(line_of "$f" '<!-- BEGIN_CONFIG_EXAMPLE -->')
    n=$(line_of "$f" '<!-- END_CONFIG_EXAMPLE -->')
    if [ -z "$x" ] || [ -z "$n" ] || [ "$x" -gt "$n" ]; then
      v "$r" S9 "README.md lacks the BEGIN_CONFIG_EXAMPLE and END_CONFIG_EXAMPLE markers"
    fi
    grep -q -F x-release-please-version "$f" || v "$r" S9 "README.md has no git source line marked x-release-please-version"
    x=$(awk '$0 == "## Reference" { print NR; exit }' "$f")
    n=$(line_of "$f" '<!-- BEGIN_TF_DOCS -->')
    if [ -z "$x" ] || [ -z "$n" ] || [ "$n" -lt "$x" ]; then
      v "$r" S9 "README.md's BEGIN_TF_DOCS is not under ## Reference"
    fi
    sec=$(section "$f" '### Accepted findings')
    # shellcheck disable=SC2046,SC2086 # file lists are word-split on purpose
    for id in $(trivy_ids $files $(scenario_module_files "$d" $files)); do
      echo "$sec" | grep -q -F -- "$id" || v "$r" S9 "README.md's Accepted findings do not list $id"
    done
    sec=$(section "$f" '## How this scenario is tested')
    for n in $ci_vars $mk_vars; do
      echo "$sec" | grep -q -E "(\`$n\`|TF_VAR_$n([^A-Za-z0-9_]|$))" ||
        v "$r" S9 "README.md's How this scenario is tested does not list the variable $n"
    done
  fi

  # S10: tests.
  if ls "$d"/tests/*.tftest.hcl >/dev/null 2>&1; then
    grep -q -E '^run "readme_example"' "$d"/tests/*.tftest.hcl ||
      v "$r" S10 "no test has run \"readme_example\""
    grep -q -E '^[[:space:]]*mock_data[[:space:]]' "$d"/tests/*.tftest.hcl &&
      v "$r" S10 "a test has a mock_data block; a scenario looks nothing up"
  fi
  return 0
}

# scenario_module_files lists the .tf files of the local modules a scenario
# calls.
scenario_module_files() {
  local d=$1 src
  shift
  [ $# -gt 0 ] || return 0
  while IFS= read -r src; do
    case $src in ../../modules/*) tf "$d/$src" ;; esac
  done < <(module_sources "$@")
}

lint_module() {
  local repo=$1 r=$2 d files vars n f sec
  d=$repo/$r
  files=$(tf "$d")

  # M1: files.
  require_files "$d" "$r" M1 versions.tf variables.tf main.tf outputs.tf README.md
  ls "$d"/tests/*.tftest.hcl >/dev/null 2>&1 || v "$r" M1 "tests/*.tftest.hcl is missing"
  # A module's versions.tf declares what it needs and configures nothing.
  if [ -f "$d/versions.tf" ]; then
    grep -q -E '^(resource|data|module|provider|variable|output|locals)[ ]' "$d/versions.tf" &&
      v "$r" M1 "versions.tf holds more than the terraform block"
    grep -q -E '^  required_providers[ ]*\{' "$d/versions.tf" ||
      v "$r" M1 "versions.tf has no required_providers"
  fi
  [ -n "$files" ] || return 0
  # shellcheck disable=SC2086 # file lists are word-split on purpose
  {
    # M2: no provider or module blocks.
    grep -q -E '^provider "' $files && v "$r" M2 "has a provider block"
    grep -q -E '^module "' $files && v "$r" M2 "has a module block; modules never call modules"
    # M4: jsonencode policies.
    grep -q -F aws_iam_policy_document $files && v "$r" M4 "uses aws_iam_policy_document; write policies with jsonencode"
    # S6 reaches modules too: no lab literal in any .tf under deploy/platform.
    denied "$r" S6 $files
  }

  # M3: every variable is set by a test.
  vars=$(variables "$d/variables.tf" 2>/dev/null | cut -d' ' -f1)
  set -- "$d"/tests/*.tftest.hcl
  if [ -e "$1" ]; then
    f=$(test_variables "$@" | sort -u)
    for n in $vars; do
      echo "$f" | grep -q -x -F "$n" || v "$r" M3 "variable \"$n\" is set by no test run"
    done
  fi

  # M5 and M6: README.
  f=$d/README.md
  if [ -f "$f" ]; then
    readme_headings "$f" "$repo/deploy/platform/MODULE-TEMPLATE.md" "$r" M5
    sec=$(section "$f" '## Accepted findings')
    # shellcheck disable=SC2086 # file lists are word-split on purpose
    for n in $(trivy_ids $files); do
      echo "$sec" | grep -q -F -- "$n" || v "$r" M5 "README.md's Accepted findings do not list $n"
    done
    section "$f" '## Usage' | grep -q -F x-release-please-version ||
      v "$r" M6 "README.md's Usage has no git source line marked x-release-please-version"
  fi
  return 0
}

lint_app() {
  local repo=$1 r=$2 d files run src ver name pin
  d=$repo/$r
  files=$(tf "$d")
  run=$repo/test/terraform/run.sh

  # A1: files.
  require_files "$d" "$r" A1 versions.tf variables.tf main.tf outputs.tf README.md .terraform.lock.hcl
  # A2: README.
  [ -f "$d/README.md" ] && readme_headings "$d/README.md" "$repo/deploy/artifactweave/TEMPLATE.md" "$r" A2
  # A3: exact pins, matching run.sh.
  # shellcheck disable=SC2086 # file lists are word-split on purpose
  while IFS=$'\t' read -r src ver; do
    case $src in jfrog/*) ;; *) continue ;; esac
    name=${src#jfrog/}
    if ! echo "$ver" | grep -q -E '^[0-9]+\.[0-9]+\.[0-9]+$'; then
      v "$r" A3 "provider $src is not pinned to one exact version (\"$ver\")"
      continue
    fi
    pin=$(sed -n -E "s/^$(echo "$name" | tr '[:lower:]-' '[:upper:]_')_VERSION=//p" "$run" 2>/dev/null | head -1)
    [ "$pin" = "$ver" ] || v "$r" A3 "provider $src is pinned to $ver, but test/terraform/run.sh mirrors ${pin:-no version of it}"
  done < <([ -n "$files" ] && required_providers $files)
  # A4: run.sh drives it.
  grep -q -F "$r)" "$run" 2>/dev/null || v "$r" A4 "test/terraform/run.sh has no case for $r"
  return 0
}

lint_bootstrap() {
  local repo=$1 r=$2 cloud=$3 d want
  d=$repo/$r
  if [ "$cloud" = gcp ]; then want='prefix[ ]*=[ ]*"bootstrap/gcp"'; else want="key[ ]*=[ ]*\"bootstrap/$cloud\\.tfstate\""; fi
  if [ ! -f "$d/backend.tf" ]; then
    v "$r" B1 "backend.tf is missing"
  elif ! grep -q -E "^[ ]*$want" "$d/backend.tf"; then
    v "$r" B1 "backend.tf's state key does not name $cloud's bootstrap"
  fi
  return 0
}

# by_rule sorts rule IDs by letter, then number: S2 before S10.
by_rule() { sed -E 's/^([A-Za-z]+)([0-9]*)$/\1 \2 &/' | sort -k1,1 -k2,2n | cut -d' ' -f3; }

# report prints the findings and returns 1 when any is a failure.
report() {
  local roots=$1 found=$2 pending=$3 verbose=$4 r ids count fails=0 warns=0
  for r in deploy $roots; do
    count=$(awk -F'\t' -v r="$r" '$1 == r' "$found" | wc -l | tr -d ' ')
    # A global finding is no root's, so the list cannot hold it back.
    if [ "$r" != deploy ] && grep -q -x -F "$r" "$pending"; then
      if [ "$count" = 0 ]; then
        echo "WARN $r: meets the contract; remove it from deploy/ci/contract-pending.txt"
        warns=$((warns + 1))
        continue
      fi
      ids=$(awk -F'\t' -v r="$r" '$1 == r { print $2 }' "$found" | sort -u | by_rule | tr '\n' ' ')
      echo "WARN $r: pending, $count findings (${ids% })"
      warns=$((warns + 1))
      [ "$verbose" = 1 ] && awk -F'\t' -v r="$r" '$1 == r { print "     [" $2 "] " $3 }' "$found"
      continue
    fi
    [ "$count" = 0 ] && continue
    awk -F'\t' -v r="$r" '$1 == r { print "FAIL [" $2 "] " r ": " $3 }' "$found"
    fails=$((fails + count))
  done
  while IFS= read -r r; do
    echo "$roots" | grep -q -x -F "$r" || {
      echo "WARN $r: listed in deploy/ci/contract-pending.txt but not a root; remove it"
      warns=$((warns + 1))
    }
  done <"$pending"
  echo "contract: $(echo "$roots" | grep -c .) roots, $fails failing findings, $warns warnings"
  [ "$fails" = 0 ]
}

# -------------------------------------------------------------- self-test

# The fixture is a small repository built in a temporary directory, never
# committed: check.sh treats any directory holding *.tf as a root. It has one
# passing scenario and module per cloud, an app-layer root and the three
# bootstraps, and reads the real templates, so the fixture READMEs follow
# them. Each case breaks one rule in a copy and expects that rule to fail.

indent() { sed 's/^/  /'; }

w() { # path, content from stdin
  mkdir -p "$(dirname "$1")"
  cat >"$1"
}

# readme writes a README following template, with the content each rule
# looks for under the heading it belongs to.
readme() {
  local template=$1 out=$2 ids=$3 vars=$4 h n
  {
    echo "# fixture"
    echo
    while IFS= read -r h; do
      echo "$h"
      echo
      case $h in
      "## Prerequisites" | "## Usage")
        echo '```hcl'
        echo 'source = "git::https://example.invalid//m?ref=v0.0.0" # x-release-please-version'
        echo '```'
        ;;
      "## The artifactweave configuration")
        printf '%s\n' '<!-- BEGIN_CONFIG_EXAMPLE -->' '```yaml' 'blob: {}' '```' '<!-- END_CONFIG_EXAMPLE -->'
        ;;
      "## Security")
        echo "### Accepted findings"
        echo
        for n in $ids; do echo "- $n"; done
        ;;
      "## Accepted findings")
        for n in $ids; do echo "- $n"; done
        ;;
      "## How this scenario is tested")
        for n in $vars; do echo "- \`$n\`"; done
        ;;
      "## Reference")
        printf '%s\n' '<!-- BEGIN_TF_DOCS -->' '<!-- END_TF_DOCS -->'
        ;;
      esac
      echo
    done < <(h2 "$template")
  } >"$out"
}

fixture() {
  local f=$1 real=$2 cloud prov placement principal labels scope tm m s b a n
  mkdir -p "$f/deploy/platform" "$f/deploy/artifactweave" "$f/deploy/ci" "$f/test/terraform"
  cp "$real/deploy/platform/SCENARIO-TEMPLATE.md" "$real/deploy/platform/MODULE-TEMPLATE.md" "$f/deploy/platform/"
  cp "$real/deploy/artifactweave/TEMPLATE.md" "$f/deploy/artifactweave/"
  printf '.terraform/\n*.tfstate*\n' >"$f/deploy/.gitignore"
  w "$f/deploy/ci/lab.mk" <<'EOF'
LAB_GCP_PROJECT ?= lab-project-0000
LAB_AZURE_SUBSCRIPTION ?= 00000000-1111-2222-3333-444444444444
LAB_AZURE_BOOTSTRAP_RG ?= artifactweave-bootstrap-rg
LAB_AWS_REGION ?= xx-lab-1
LAB_AWS_ZONE_ID ?= ZLABZONE0000
LAB_AWS_ZONE_NAME ?= aws.artifact.weaveplatform.run.
EOF
  for cloud in aws gcp azure; do
    case $cloud in
    aws) prov=aws placement=region principal=app_role_arn labels=tags scope='' tm=aws_s3_bucket ;;
    gcp) prov=google placement=location principal=app_service_account labels=labels scope=project_id tm=google_storage_bucket ;;
    azure) prov=azurerm placement=location principal=app_principal_id labels=tags scope=subscription_id tm=azurerm_storage_account ;;
    esac

    # The module.
    m=$f/deploy/platform/terraform/modules/$cloud/storage
    w "$m/versions.tf" <<EOF
terraform {
  required_providers {
    $prov = {
      source = "hashicorp/$prov"
    }
  }
}
EOF
    w "$m/variables.tf" <<'EOF'
variable "name" {
  type        = string
  description = "Start of every name."
}
EOF
    w "$m/main.tf" <<EOF
#trivy:ignore:FIX-${cloud}1 the reason
resource "$tm" "this" {
  name = var.name
}
EOF
    w "$m/outputs.tf" <<'EOF'
output "host" {
  description = "The host."
  value       = var.name
}
EOF
    w "$m/tests/inputs.tftest.hcl" <<EOF
mock_provider "$prov" {}

run "inputs" {
  command = plan
  variables {
    name = "x"
  }
}
EOF
    readme "$f/deploy/platform/MODULE-TEMPLATE.md" "$m/README.md" "FIX-${cloud}1" ""

    # The scenario.
    s=$f/deploy/platform/terraform/$cloud/good
    {
      echo 'terraform {'
      echo '  required_version = ">= 1.9"'
      echo '  required_providers {'
      echo "    $prov = {"
      echo "      source = \"hashicorp/$prov\""
      echo '    }'
      echo '  }'
      echo '}'
      echo
      echo "provider \"$prov\" {"
      if [ "$cloud" = azure ]; then
        echo '  storage_use_azuread             = true'
        echo '  resource_provider_registrations = "none"'
      fi
      echo '}'
    } | w "$s/versions.tf"
    {
      for n in name $placement $principal $scope; do
        printf 'variable "%s" {\n  type        = string\n  description = "Required."\n}\n\n' "$n"
      done
      printf 'variable "%s" {\n  type        = map(string)\n  description = "Labels."\n  default     = {}\n}\n' "$labels"
      if [ "$cloud" != azure ]; then
        printf '\nvariable "force_destroy" {\n  type        = bool\n  description = "Destroy with contents."\n  default     = false\n}\n'
      fi
    } | w "$s/variables.tf"
    w "$s/main.tf" <<EOF
resource "random_id" "suffix" {
  byte_length = 4
}

module "storage" {
  source = "../../modules/$cloud/storage"
  name   = var.name
}
EOF
    w "$s/outputs.tf" <<'EOF'
output "artifactweave_config" {
  description = "The blob section."
  value       = "blob: {}"
}

output "storage_host" {
  description = "Where signed URLs point."
  value       = module.storage.host
}
EOF
    w "$s/harness.tf" <<EOF
output "expires_at" {
  description = "For the test harness: the expires-at label reap reads."
  value       = lookup(var.$labels, "expires-at", "")
}
EOF
    echo '# lock' | w "$s/.terraform.lock.hcl"
    w "$s/tests/composition.tftest.hcl" <<EOF
mock_provider "$prov" {}

run "readme_example" {
  command = plan
}
EOF
    w "$s/Makefile" <<'EOF'
# The fixture's own make targets.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
EOF
    {
      echo '# shellcheck shell=bash'
      # shellcheck disable=SC2016 # the line as written, unexpanded
      echo 'export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"'
      echo "export TF_VAR_$placement=\"\$PLATFORM_PLACE\""
      echo "export TF_VAR_$principal=\"\$PLATFORM_PRINCIPAL\""
      echo "export TF_VAR_$labels=\"\$SCENARIO_LABELS\""
      [ -n "$scope" ] && echo "export TF_VAR_$scope=\"\$PLATFORM_SCOPE\""
      [ "$cloud" != azure ] && echo 'export TF_VAR_force_destroy=true'
    } | w "$s/ci-vars.sh"
    n="name $placement $principal $labels $scope"
    [ "$cloud" != azure ] && n="$n force_destroy"
    readme "$f/deploy/platform/SCENARIO-TEMPLATE.md" "$s/README.md" "FIX-${cloud}1" "$n"

    # The bootstrap.
    b=$f/deploy/ci/terraform/$cloud
    if [ "$cloud" = gcp ]; then
      printf 'terraform {\n  backend "gcs" {\n    prefix = "bootstrap/gcp"\n  }\n}\n' | w "$b/backend.tf"
    else
      printf 'terraform {\n  backend "x" {\n    key = "bootstrap/%s.tfstate"\n  }\n}\n' "$cloud" | w "$b/backend.tf"
    fi
    echo 'locals {}' | w "$b/main.tf"
  done

  # The multicloud scenario: one provider and one module call per cloud, each
  # cloud-specific input named after its cloud.
  s=$f/deploy/platform/terraform/multicloud/good
  w "$s/versions.tf" <<'EOF'
terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
    google = {
      source = "hashicorp/google"
    }
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

provider "aws" {
}

provider "google" {
}

provider "azurerm" {
  storage_use_azuread             = true
  resource_provider_registrations = "none"
}
EOF
  {
    for n in name aws_region gcp_location azure_location app_role_arn app_service_account app_principal_id \
      project_id subscription_id; do
      printf 'variable "%s" {\n  type        = string\n  description = "Required."\n}\n\n' "$n"
    done
    printf 'variable "labels" {\n  type        = map(string)\n  description = "Labels."\n  default     = {}\n}\n'
    printf '\nvariable "force_destroy" {\n  type        = bool\n  description = "Destroy with contents."\n  default     = false\n}\n'
  } | w "$s/variables.tf"
  {
    printf 'resource "random_id" "suffix" {\n  byte_length = 4\n}\n'
    for cloud in aws gcp azure; do
      printf '\nmodule "%s" {\n  source = "../../modules/%s/storage"\n  name   = var.name\n}\n' "$cloud" "$cloud"
    done
  } | w "$s/main.tf"
  w "$s/outputs.tf" <<'EOF'
output "artifactweave_config" {
  description = "The blob section."
  value       = "blob: {}"
}

output "storage_host" {
  description = "Where signed URLs point."
  value       = module.aws.host
}
EOF
  w "$s/harness.tf" <<'EOF'
output "expires_at" {
  description = "For the test harness: the expires-at label reap reads."
  value       = lookup(var.labels, "expires-at", "")
}
EOF
  echo '# lock' | w "$s/.terraform.lock.hcl"
  w "$s/tests/composition.tftest.hcl" <<'EOF'
mock_provider "aws" {}

run "readme_example" {
  command = plan
}
EOF
  w "$s/Makefile" <<'EOF'
# The fixture's own make targets.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
EOF
  {
    echo '# shellcheck shell=bash'
    # shellcheck disable=SC2016 # the line as written, unexpanded
    echo 'export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"'
    for n in aws_region gcp_location azure_location app_role_arn app_service_account app_principal_id \
      project_id subscription_id; do
      echo "export TF_VAR_$n=\"\$PLATFORM_X\""
    done
    # shellcheck disable=SC2016 # the line as written, unexpanded
    echo 'export TF_VAR_labels="$SCENARIO_LABELS"'
    echo 'export TF_VAR_force_destroy=true'
  } | w "$s/ci-vars.sh"
  readme "$f/deploy/platform/SCENARIO-TEMPLATE.md" "$s/README.md" "FIX-aws1 FIX-gcp1 FIX-azure1" \
    "name aws_region gcp_location azure_location app_role_arn app_service_account app_principal_id project_id subscription_id labels force_destroy"

  # The app-layer root and its driver.
  a=$f/deploy/artifactweave/terraform/good
  w "$a/versions.tf" <<'EOF'
terraform {
  required_providers {
    artifactory = { source = "jfrog/artifactory", version = "1.2.3" }
    platform = {
      source  = "jfrog/platform"
      version = "4.5.6"
    }
  }
}
EOF
  printf 'variable "url" {\n  type        = string\n  description = "Base URL."\n}\n' | w "$a/variables.tf"
  echo 'locals {}' | w "$a/main.tf"
  echo '' | w "$a/outputs.tf"
  echo '# lock' | w "$a/.terraform.lock.hcl"
  readme "$f/deploy/artifactweave/TEMPLATE.md" "$a/README.md" "" ""
  w "$f/test/terraform/run.sh" <<'EOF'
ARTIFACTORY_VERSION=1.2.3
PLATFORM_VERSION=4.5.6
case $scenario in
deploy/artifactweave/terraform/good) tf apply ;;
esac
EOF
}

self_test() {
  local real base good cases failures=0 dir id rule root out result n sc sg sz sm ma ap
  real=$(cd "$here/../.." && pwd)
  base=$(mktemp -d)
  # shellcheck disable=SC2064 # the path is fixed now
  trap "rm -rf '$base'" EXIT
  good=$base/good
  fixture "$good" "$real"

  sc=deploy/platform/terraform/aws/good
  sg=deploy/platform/terraform/gcp/good
  sz=deploy/platform/terraform/azure/good
  sm=deploy/platform/terraform/multicloud/good
  ma=deploy/platform/terraform/modules/aws/storage
  ap=deploy/artifactweave/terraform/good

  # case <id> <rule> <root> <shell that breaks the rule, run in the copy>
  cases=$base/cases
  : >"$cases"
  c() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >>"$cases"; }
  c s1-missing S1 $sc "rm $sc/harness.tf"
  c s1-no-tests S1 $sc "rm -r $sc/tests"
  c s1-forbidden S1 $sc "touch $sc/compose.yaml"
  c s1-tfvars S1 $sc "touch $sc/lab.tfvars"
  c s2-resource S2 $sc "printf 'resource \"aws_s3_bucket\" \"b\" {\n}\n' >>$sc/main.tf"
  c s2-data S2 $sc "printf 'data \"aws_iam_role\" \"r\" {\n}\n' >>$sc/main.tf"
  c s2-source S2 $sc "sed -i.bak 's#../../modules/aws/storage#git::https://example.invalid//m#' $sc/main.tf"
  c s2-other-cloud S2 $sc "sed -i.bak 's#modules/aws/storage#modules/gcp/storage#' $sc/main.tf"
  c s3-provider-elsewhere S3 $sc "printf 'provider \"aws\" {\n}\n' >>$sc/main.tf"
  c s3-alias S3 $sc "printf 'provider \"aws\" {\n  alias  = \"replica\"\n  region = \"x\"\n}\n' >>$sc/versions.tf"
  c s3-version S3 $sc "sed -i.bak 's#>= 1.9#>= 1.6#' $sc/versions.tf"
  c s3-azuread S3 $sz "sed -i.bak '/storage_use_azuread/d' $sz/versions.tf"
  c s3-registrations S3 $sz "sed -i.bak '/resource_provider_registrations/d' $sz/versions.tf"
  c s3-scope S3 $sg "printf 'variable \"project_id\" {\n  type        = string\n  description = \"x\"\n  default     = \"p\"\n}\n' >$base/v && awk '/^variable \"project_id\"/{s=1} s&&/^}/{s=0;next} !s' $sg/variables.tf >$base/t && cat $base/t $base/v >$sg/variables.tf"
  c s4-type S4 $sc "awk '/  type        = string/ && !d { d = 1; next } { print }' $sc/variables.tf >$base/t && cp $base/t $sc/variables.tf"
  c s4-description S4 $sc "awk '/  description = \"Required.\"/ && !d { d = 1; next } { print }' $sc/variables.tf >$base/t && cp $base/t $sc/variables.tf"
  c s4-default S4 $sc "awk '/^variable \"app_role_arn\"/{print; print \"  default     = \\\"\\\"\"; next} {print}' $sc/variables.tf >$base/t && cp $base/t $sc/variables.tf"
  c s4-harness-input S4 $sc "printf 'variable \"scenario_labels\" {\n  type        = string\n  description = \"x\"\n}\n' >>$sc/variables.tf"
  c s4-vocabulary S4 $sg "printf 'variable \"region\" {\n  type        = string\n  description = \"x\"\n}\n' >>$sg/variables.tf"
  c s4-principal S4 $sc "awk '/^variable \"app_role_arn\"/{s=1} s&&/^}/{s=0;next} !s' $sc/variables.tf >$base/t && cp $base/t $sc/variables.tf"
  c s3-multicloud-scope S3 $sm "awk '/^variable \"subscription_id\"/{print; print \"  default     = \\\"x\\\"\"; next} {print}' $sm/variables.tf >$base/t && cp $base/t $sm/variables.tf"
  c s4-multicloud-vocabulary S4 $sm "printf 'variable \"location\" {\n  type        = string\n  description = \"x\"\n}\n' >>$sm/variables.tf"
  c s4-multicloud-placement S4 $sm "awk '/^variable \"azure_location\"/{s=1} s&&/^}/{s=0;next} !s' $sm/variables.tf >$base/t && cp $base/t $sm/variables.tf"
  c s4-multicloud-principal S4 $sm "awk '/^variable \"app_service_account\"/{s=1} s&&/^}/{s=0;next} !s' $sm/variables.tf >$base/t && cp $base/t $sm/variables.tf"
  c s5-description S5 $sc "sed -i.bak 's#For the test harness: #Reap: #' $sc/harness.tf"
  c s5-place S5 $sc "printf 'output \"mirror_members\" {\n  description = \"x\"\n  value       = 1\n}\n' >>$sc/outputs.tf"
  c s5-missing S5 $sc "printf 'output \"x\" {\n  description = \"x\"\n  value       = 1\n}\n' >$sc/outputs.tf"
  c s5-elsewhere S5 $sc "printf 'output \"y\" {\n  description = \"x\"\n  value       = 1\n}\n' >>$sc/main.tf"
  c s6-lab-value S6 $sc "printf 'locals {\n  p = \"lab-project-0000\"\n}\n' >>$sc/main.tf"
  c s6-fixed-name S6 $sc "printf '# granted to artifactweave-conformance\n' >>$sc/main.tf"
  c s6-module S6 $ma "printf '# weaveplatform.run\n' >>$ma/main.tf"
  c s7-line S7 $sc "echo 'TF_VAR_x=1' >>$sc/ci-vars.sh"
  c s7-undeclared S7 $sc "echo 'export TF_VAR_nope=1' >>$sc/ci-vars.sh"
  c s7-name S7 $sc "sed -i.bak '/TF_VAR_name=/d' $sc/ci-vars.sh"
  c s7-labels S7 $sg "sed -i.bak '/TF_VAR_labels=/d' $sg/ci-vars.sh"
  c s7-force-destroy S7 $sc "sed -i.bak '/force_destroy/d' $sc/ci-vars.sh"
  c s8-include S8 $sc "echo 'all:' >>$sc/Makefile"
  c s8-lab-value S8 $sc "{ echo 'ZONE := ZLABZONE0000'; cat $sc/Makefile; } >$base/t && cp $base/t $sc/Makefile"
  c s8-undeclared S8 $sc "{ echo 'TF_VARS := -var nope=1'; cat $sc/Makefile; } >$base/t && cp $base/t $sc/Makefile"
  c s9-headings S9 $sc "sed -i.bak 's/^## Teardown/## Tearing down/' $sc/README.md"
  c s9-config S9 $sc "sed -i.bak '/BEGIN_CONFIG_EXAMPLE/d' $sc/README.md"
  c s9-release S9 $sc "sed -i.bak '/x-release-please-version/d' $sc/README.md"
  c s9-tf-docs S9 $sc "awk '/^## Reference/ { print \"<!-- BEGIN_TF_DOCS -->\" } !/BEGIN_TF_DOCS/' $sc/README.md >$base/t && cp $base/t $sc/README.md"
  c s9-findings S9 $sc "sed -i.bak '/^- FIX-aws1/d' $sc/README.md"
  c s9-module-findings S9 $sc "printf '#trivy:ignore:FIX-new the reason\n' >>$ma/main.tf && awk '{ print } /^## Accepted findings/ { print \"- FIX-new\" }' $ma/README.md >$base/t && cp $base/t $ma/README.md"
  c s9-variables S9 $sc "sed -i.bak '/^- .force_destroy./d' $sc/README.md"
  c s10-readme-example S10 $sc "sed -i.bak 's/readme_example/composition/' $sc/tests/composition.tftest.hcl"
  c s10-mock-data S10 $sc "printf 'mock_provider \"aws\" {\n  mock_data \"aws_iam_role\" {}\n}\n' >>$sc/tests/composition.tftest.hcl"
  c s11-place S11 $sc "printf '# kept for the lab store\nmoved {\n  from = a.b\n  to   = c.d\n}\n' >>$sc/main.tf"
  c s11-comment S11 $sc "printf 'moved {\n  from = a.b\n  to   = c.d\n}\n' >$sc/moved.tf"
  c m1-files M1 $ma "rm -r $ma/tests"
  c m1-versions M1 $ma "printf 'locals {}\n' >>$ma/versions.tf"
  c m1-required-providers M1 $ma "printf 'terraform {\n}\n' >$ma/versions.tf"
  c m2-provider M2 $ma "printf 'provider \"aws\" {\n}\n' >>$ma/main.tf"
  c m2-module M2 $ma "printf 'module \"x\" {\n  source = \"./x\"\n}\n' >>$ma/main.tf"
  c m3-untested M3 $ma "printf 'variable \"tags\" {\n  type        = map(string)\n  description = \"x\"\n  default     = {}\n}\n' >>$ma/variables.tf"
  c m4-policy-document M4 $ma "printf 'locals {\n  p = data.aws_iam_policy_document.x.json\n}\n' >>$ma/main.tf"
  c m5-headings M5 $ma "sed -i.bak 's/^## Invariants/## Guarantees/' $ma/README.md"
  c m5-findings M5 $ma "sed -i.bak '/^- FIX-aws1/d' $ma/README.md"
  c m6-release M6 $ma "sed -i.bak '/x-release-please-version/d' $ma/README.md"
  c a1-files A1 $ap "rm $ap/outputs.tf"
  c a2-headings A2 $ap "sed -i.bak 's/^## Applying it/## Apply/' $ap/README.md"
  c a3-range A3 $ap "sed -i.bak 's/version = \"1.2.3\"/version = \"~> 1.2\"/' $ap/versions.tf"
  c a3-mismatch A3 $ap "sed -i.bak 's/^PLATFORM_VERSION=.*/PLATFORM_VERSION=9.9.9/' test/terraform/run.sh"
  c a4-run A4 $ap "sed -i.bak '/terraform\\/good)/d' test/terraform/run.sh"
  c b1-key B1 deploy/ci/terraform/aws "sed -i.bak 's#bootstrap/aws#scenarios/aws#' deploy/ci/terraform/aws/backend.tf"
  c b1-missing B1 deploy/ci/terraform/gcp "rm deploy/ci/terraform/gcp/backend.tf"
  c g1-root G1 $sc "touch $sc/.gitignore"
  c g1-missing G1 deploy "rm deploy/.gitignore"
  c g2-emdash G2 $sc "printf 'a $emdash b\n' >>$sc/README.md"
  c layout layout deploy/platform/terraform/stray "mkdir -p deploy/platform/terraform/stray && echo 'locals {}' >deploy/platform/terraform/stray/main.tf"

  # The passing fixture passes, with nothing pending.
  if ! out=$(lint "$good" 0 2>&1); then
    echo "self-test: the passing fixture fails:" >&2
    echo "$out" >&2
    return 1
  fi

  # Each case runs in its own copy, in parallel.
  while IFS=$'\t' read -r id _ _ result; do
    dir=$base/case-$id
    cp -R "$good" "$dir"
    (cd "$dir" && sh -c "$result") >/dev/null 2>&1 || {
      echo "self-test: case $id did not apply" >&2
      failures=$((failures + 1))
    }
    find "$dir" -name '*.bak' -exec rm {} +
    (lint "$dir" 1 >"$dir.out" 2>&1 && echo pass || echo fail) >"$dir.status" &
  done <"$cases"
  wait

  while IFS=$'\t' read -r id rule root _; do
    dir=$base/case-$id
    if [ "$(cat "$dir.status")" != fail ] || ! grep -q -F "FAIL [$rule] $root:" "$dir.out"; then
      echo "self-test: case $id did not fail $rule for $root:" >&2
      indent <"$dir.out" >&2
      failures=$((failures + 1))
    fi
  done <"$cases"

  # The ratchet: a pending root warns instead of failing, a pending root
  # that passes asks to be removed, an entry that is no root warns, and a
  # global finding fails even when deploy itself is listed.
  dir=$base/ratchet
  cp -R "$good" "$dir"
  rm "$dir/$sc/harness.tf"
  printf '%s\n' "# fixture" "$sc" "$sg" deploy/platform/terraform/aws/gone >"$dir/deploy/ci/contract-pending.txt"
  if ! out=$(lint "$dir" 0 2>&1); then
    echo "self-test: a pending root failed instead of warning:" >&2
    echo "$out" | indent >&2
    failures=$((failures + 1))
  fi
  for result in "WARN $sc: pending" "WARN $sg: meets the contract" "WARN deploy/platform/terraform/aws/gone: listed"; do
    echo "$out" | grep -q -F "$result" || {
      echo "self-test: the ratchet did not print \"$result\":" >&2
      echo "$out" | indent >&2
      failures=$((failures + 1))
    }
  done

  dir=$base/ratchet-global
  cp -R "$good" "$dir"
  rm "$dir/deploy/.gitignore"
  echo deploy >"$dir/deploy/ci/contract-pending.txt"
  if out=$(lint "$dir" 0 2>&1) || ! echo "$out" | grep -q -F "FAIL [G1] deploy:"; then
    echo "self-test: listing deploy held back a global finding:" >&2
    echo "$out" | indent >&2
    failures=$((failures + 1))
  fi

  n=$(grep -c . "$cases")
  if [ "$failures" -gt 0 ]; then
    echo "self-test: $failures checks failed" >&2
    return 1
  fi
  echo "contract self-test: $n rule cases, the passing fixture and the ratchet pass"
}

# ------------------------------------------------------------------- main

verbose=0
repo=
case ${1:-} in
--self-test)
  [ $# = 1 ] || usage
  self_test
  exit
  ;;
--verbose)
  verbose=1
  shift
  ;;
-*) usage ;;
esac
[ $# -le 1 ] || usage
repo=$(cd "${1:-$here/../..}" && pwd)
lint "$repo" "$verbose"
