#!/bin/sh
# Permission policy for GitHub workflow files (ADR-0007, rule 5; the release exception is ADR-0018).
# Run by `make workflow-policy`, which the workflow-lint job in .github/workflows/ci.yml also runs.
#
#   scripts/check-workflow-permissions.sh [DIR]   check every *.yml and *.yaml in DIR (default .github/workflows)
#   scripts/check-workflow-permissions.sh --self-test
#                                                 check that the negative fixtures are caught and the positive ones pass
#
# Every workflow must have a top-level `permissions: contents: read`. No file may request a write scope, with one
# exception: release.yml may request `contents: write` at job level, only as a block entry, and only in a job that
# runs for a tag push (and never for a dispatch or a pull request). This is a text check, not a YAML parser (ADR-0007,
# Consequences).
set -eu

FIXTURES=scripts/testdata/permission-policy
RELEASE_FILE=release.yml

# A write scope: a colon, blanks, an optional quote, write or write-all, an optional closing quote, then a blank,
# a comma, a closing brace or the end of the line.
WRITE_SCOPE=':[[:space:]]*["'"'"']?write(-all)?["'"'"']?([[:space:],}]|$)'

has_write_scope() {
  grep -qE "$WRITE_SCOPE" "$1"
}

top_level_read_only() {
  grep -A1 -x 'permissions:' "$1" | grep -qx '  contents: read'
}

# release_write_violations FILE: prints one error per write scope in a release workflow that the exception does not
# allow, and exits 1 if there is any. Allowed: `      contents: write` as a block entry of a job-level `permissions:`
# block (job keys at indent 2, job permissions at indent 4, entries at indent 6), in a job whose `if:` names both
# `github.event_name == 'push'` and `refs/tags/`.
release_write_violations() {
  awk -v scope="$WRITE_SCOPE" -v file="$1" '
    function indent(s) { match(s, /^ */); return RLENGTH }
    /^[[:space:]]*(#.*)?$/ { next }
    {
      ind = indent($0)
      if (ind == 0) { section = ($0 ~ /^jobs:/) ? "jobs" : "other"; job = ""; inperm = 0 }
      else if (section == "jobs" && ind == 2 && $0 ~ /^  [A-Za-z_][A-Za-z0-9_-]*:[[:space:]]*$/) {
        job = $0; sub(/^  /, "", job); sub(/:.*$/, "", job); inperm = 0
      }
      else if (section == "jobs" && ind == 4 && $0 ~ /^    permissions:[[:space:]]*$/) { inperm = 1 }
      else if (ind <= 4 && $0 !~ /^    permissions:/) { inperm = 0 }
      if (section == "jobs" && ind == 4 && $0 ~ /^    if:/ && index($0, "refs/tags/") && index($0, "github.event_name == '\''push'\''")) {
        tagged[job] = 1
      }
      if ($0 ~ scope) {
        if (section == "jobs" && job != "" && inperm && ind == 6 && $0 ~ /^      contents: write[[:space:]]*$/) {
          writes[job]++
        } else {
          printf "::error file=%s,line=%d::write scope outside the release exception (contents: write at job level only)\n", file, NR
          bad = 1
        }
      }
    }
    END {
      for (j in writes) if (!tagged[j]) {
        printf "::error file=%s::job %s requests contents: write but its if: does not limit it to a tag push\n", file, j
        bad = 1
      }
      exit bad
    }
  ' "$1"
}

# check_file FILE: a file must be read-only at top level; a write scope is allowed only in release.yml (see above).
check_file() {
  status=0
  if [ "$(basename "$1")" = "$RELEASE_FILE" ]; then
    release_write_violations "$1" || status=1
  elif has_write_scope "$1"; then
    echo "::error file=$1::must stay read-only, but a write scope was requested"
    status=1
  fi
  if ! top_level_read_only "$1"; then
    echo "::error file=$1::top-level permissions must be exactly contents read"
    status=1
  fi
  return "$status"
}

# check_dir DIR: every workflow in DIR must satisfy check_file.
check_dir() {
  status=0
  for f in "$1"/*.yml "$1"/*.yaml; do
    [ -e "$f" ] || continue
    check_file "$f" || status=1
  done
  return "$status"
}

# self_test: each fail-* fixture must contain a write scope, the pass-* fixtures must satisfy the policy, and every
# directory under release/ must pass (pass-*) or fail (fail-*) as its name says.
self_test() {
  status=0
  for f in "$FIXTURES"/fail-*.yml; do
    if has_write_scope "$f"; then
      echo "ok: $f is rejected"
    else
      echo "::error file=$f::expected a write scope to be detected"
      status=1
    fi
  done
  for f in "$FIXTURES"/pass-*.yml; do
    if has_write_scope "$f" || ! top_level_read_only "$f"; then
      echo "::error file=$f::expected a read-only workflow to pass"
      status=1
    else
      echo "ok: $f passes"
    fi
  done
  for d in "$FIXTURES"/release/*/; do
    d=${d%/}
    name=$(basename "$d")
    case "$name" in
      pass-*) want=0 ;;
      fail-*) want=1 ;;
      *) echo "::error file=$d::fixture directories must be named pass-* or fail-*"; status=1; continue ;;
    esac
    if check_dir "$d" > /dev/null 2>&1; then got=0; else got=1; fi
    if [ "$got" = "$want" ]; then
      echo "ok: $d behaves as its name says"
    else
      echo "::error file=$d::expected $( [ "$want" = 0 ] && echo pass || echo fail ), got the opposite"
      status=1
    fi
  done
  return "$status"
}

case "${1:-}" in
  --self-test) self_test ;;
  *) check_dir "${1:-.github/workflows}" ;;
esac
