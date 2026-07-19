#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/queue-release-scripts.XXXXXX")"
FIXTURE_DIR="$TMP_DIR/repository"
RELEASE_VERSION="v0.3.0"
GIT_BIN="$(command -v git)"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

fail() {
  echo "release script contract: $*" >&2
  exit 1
}

run_tag() {
  run_tag_in "$FIXTURE_DIR" "$@"
}

run_tag_in() {
  local repository="$1"
  shift
  (
    cd "$repository"
    ./scripts/tag-all-modules.sh "$@"
  )
}

clone_fixture() {
  local destination="$1"
  git clone -q "$FIXTURE_DIR" "$destination"
  git -C "$destination" config user.name "Release Contract"
  git -C "$destination" config user.email "release-contract@example.invalid"
}

create_bare_remote() {
  local destination="$1"
  git clone -q --bare "$FIXTURE_DIR" "$destination"
  git -C "$destination" config user.name "Release Contract"
  git -C "$destination" config user.email "release-contract@example.invalid"
}

# create_fixture mirrors the minimum repository surfaces guarded by the real
# release path so the contract stays offline and independent of working-tree state.
create_fixture() {
  mkdir -p "$FIXTURE_DIR/scripts" "$FIXTURE_DIR/driver/mockqueue" "$FIXTURE_DIR/.github/workflows"
  cp "$ROOT_DIR/scripts/check-module-inventory.sh" "$FIXTURE_DIR/scripts/check-module-inventory.sh"
  cp "$ROOT_DIR/scripts/plan-module-release-tags.sh" "$FIXTURE_DIR/scripts/plan-module-release-tags.sh"
  cp "$ROOT_DIR/scripts/release-version.sh" "$FIXTURE_DIR/scripts/release-version.sh"
  cp "$ROOT_DIR/scripts/tag-all-modules.sh" "$FIXTURE_DIR/scripts/tag-all-modules.sh"
  chmod +x "$FIXTURE_DIR/scripts/check-module-inventory.sh" "$FIXTURE_DIR/scripts/plan-module-release-tags.sh" "$FIXTURE_DIR/scripts/tag-all-modules.sh"

  cat >"$FIXTURE_DIR/go.mod" <<'EOF_ROOT_MOD'
module example.com/queue-release-fixture

go 1.24.4
EOF_ROOT_MOD

  cat >"$FIXTURE_DIR/driver/mockqueue/go.mod" <<'EOF_DRIVER_MOD'
module example.com/queue-release-fixture/driver/mockqueue

go 1.24.4

require example.com/queue-release-fixture v0.0.0

replace example.com/queue-release-fixture => ../..
EOF_DRIVER_MOD

  cat >"$FIXTURE_DIR/go.work" <<'EOF_WORK'
go 1.24.4

use (
	.
	./driver/mockqueue
)
EOF_WORK

  cat >"$FIXTURE_DIR/.github/workflows/test.yml" <<'EOF_WORKFLOW'
jobs:
  race_modules:
    strategy:
      matrix:
        race_module: [".", "driver/mockqueue"]
EOF_WORKFLOW

  git -C "$FIXTURE_DIR" init -q
  git -C "$FIXTURE_DIR" config user.name "Release Contract"
  git -C "$FIXTURE_DIR" config user.email "release-contract@example.invalid"
  git -C "$FIXTURE_DIR" add go.mod go.work driver/mockqueue/go.mod scripts .github/workflows/test.yml
  git -C "$FIXTURE_DIR" commit -qm "test: initialize release fixture"
}

create_fixture

invalid_versions=(
  v01.2.3
  v0.3.0-01
  v0.3.0-alpha..1
  v0.3.0-alpha_1
)
version_case=0
for invalid_version in "${invalid_versions[@]}"; do
  version_case=$((version_case + 1))
  for entrypoint in tag planner inventory; do
    version_output="$TMP_DIR/version-$version_case-$entrypoint.log"
    case "$entrypoint" in
      tag)
        if run_tag "$invalid_version" --dry-run --allow-dirty >"$version_output" 2>&1; then
          fail "tag entrypoint accepted invalid version $invalid_version"
        fi
        ;;
      planner)
        if (
          cd "$FIXTURE_DIR"
          ./scripts/plan-module-release-tags.sh "$invalid_version"
        ) >"$version_output" 2>&1; then
          fail "planner entrypoint accepted invalid version $invalid_version"
        fi
        ;;
      inventory)
        if (
          cd "$FIXTURE_DIR"
          ./scripts/check-module-inventory.sh --release-version "$invalid_version"
        ) >"$version_output" 2>&1; then
          fail "inventory entrypoint accepted invalid version $invalid_version"
        fi
        ;;
    esac
    if ! grep -Fq "invalid Go release version $invalid_version" "$version_output"; then
      cat "$version_output" >&2
      fail "$entrypoint entrypoint rejected $invalid_version without the shared diagnostic"
    fi
  done
done

for entrypoint in tag planner inventory; do
  major_output="$TMP_DIR/major-$entrypoint.log"
  case "$entrypoint" in
    tag)
      if run_tag v2.0.0 --dry-run --allow-dirty >"$major_output" 2>&1; then
        fail "tag entrypoint accepted v2 for unsuffixed module paths"
      fi
      ;;
    planner)
      if (
        cd "$FIXTURE_DIR"
        ./scripts/plan-module-release-tags.sh v2.0.0
      ) >"$major_output" 2>&1; then
        fail "planner entrypoint accepted v2 for unsuffixed module paths"
      fi
      ;;
    inventory)
      if (
        cd "$FIXTURE_DIR"
        ./scripts/check-module-inventory.sh --release-version v2.0.0
      ) >"$major_output" 2>&1; then
        fail "inventory entrypoint accepted v2 for unsuffixed module paths"
      fi
      ;;
  esac
  if ! grep -Fq "requires /v2 for release v2.0.0" "$major_output"; then
    cat "$major_output" >&2
    fail "$entrypoint entrypoint rejected v2 without the module-path diagnostic"
  fi
done

V2_DIR="$TMP_DIR/v2-module"
mkdir -p "$V2_DIR/.github/workflows"
cp -R "$FIXTURE_DIR/scripts" "$V2_DIR/scripts"
cat >"$V2_DIR/go.mod" <<'EOF_V2_MOD'
module example.com/queue-release-fixture/v2

go 1.24.4
EOF_V2_MOD
cat >"$V2_DIR/go.work" <<'EOF_V2_WORK'
go 1.24.4

use .
EOF_V2_WORK
cat >"$V2_DIR/.github/workflows/test.yml" <<'EOF_V2_WORKFLOW'
jobs:
  race_modules:
    strategy:
      matrix:
        race_module: ["."]
EOF_V2_WORKFLOW
git -C "$V2_DIR" init -q
git -C "$V2_DIR" config user.name "Release Contract"
git -C "$V2_DIR" config user.email "release-contract@example.invalid"
git -C "$V2_DIR" add go.mod go.work scripts .github/workflows/test.yml
git -C "$V2_DIR" commit -qm "test: initialize v2 release fixture"
if ! run_tag_in "$V2_DIR" v2.1.0 >"$TMP_DIR/v2-tag.log" 2>&1; then
  cat "$TMP_DIR/v2-tag.log" >&2
  fail "tag entrypoint rejected a matching /v2 module path"
fi
if ! (
  cd "$V2_DIR"
  ./scripts/plan-module-release-tags.sh v2.1.0
) >"$TMP_DIR/v2-planner.log" 2>&1; then
  cat "$TMP_DIR/v2-planner.log" >&2
  fail "planner entrypoint rejected a matching /v2 module path"
fi
if ! (
  cd "$V2_DIR"
  ./scripts/check-module-inventory.sh --release-version v2.1.0
) >"$TMP_DIR/v2-inventory.log" 2>&1; then
  cat "$TMP_DIR/v2-inventory.log" >&2
  fail "inventory entrypoint rejected a matching /v2 module path"
fi
if [[ "$(git -C "$V2_DIR" tag --list)" != "v2.1.0" ]]; then
  fail "matching /v2 module path produced the wrong tag family"
fi

HIDDEN_PIN_DIR="$TMP_DIR/hidden-pin"
clone_fixture "$HIDDEN_PIN_DIR"
GOWORK=off go mod edit \
  -require="example.com/queue-release-fixture@$RELEASE_VERSION" \
  "$HIDDEN_PIN_DIR/driver/mockqueue/go.mod"
git -C "$HIDDEN_PIN_DIR" update-index --assume-unchanged driver/mockqueue/go.mod
if [[ -n "$(git -C "$HIDDEN_PIN_DIR" status --porcelain)" ]]; then
  fail "hidden-pin fixture is not clean according to Git"
fi

hidden_preview_output="$TMP_DIR/hidden-pin-preview.log"
if ! run_tag_in "$HIDDEN_PIN_DIR" "$RELEASE_VERSION" --dry-run --allow-dirty >"$hidden_preview_output" 2>&1; then
  cat "$hidden_preview_output" >&2
  fail "dry-run did not preview hidden working-tree module pins"
fi
if ! grep -Fq "create tags (2):" "$hidden_preview_output"; then
  cat "$hidden_preview_output" >&2
  fail "hidden-pin dry-run produced the wrong working-tree tag plan"
fi

hidden_release_output="$TMP_DIR/hidden-pin-release.log"
if run_tag_in "$HIDDEN_PIN_DIR" "$RELEASE_VERSION" >"$hidden_release_output" 2>&1; then
  fail "hidden working-tree pins validated a release that captured HEAD does not contain"
fi
if ! grep -Fq "requires sibling example.com/queue-release-fixture at v0.0.0; release $RELEASE_VERSION requires a resolvable $RELEASE_VERSION pin" "$hidden_release_output"; then
  cat "$hidden_release_output" >&2
  fail "captured-HEAD pin rejection omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$HIDDEN_PIN_DIR" tag --list)" ]]; then
  fail "captured-HEAD pin rejection created local tags"
fi

invalid_output="$TMP_DIR/invalid-pins.log"
if run_tag "$RELEASE_VERSION" >"$invalid_output" 2>&1; then
  fail "tagging unexpectedly accepted a v0.0.0 sibling pin"
fi
if [[ -n "$(git -C "$FIXTURE_DIR" tag --list)" ]]; then
  fail "invalid release pins created tags before the preflight failed"
fi
if ! grep -Fq "requires sibling example.com/queue-release-fixture at v0.0.0; release $RELEASE_VERSION requires a resolvable $RELEASE_VERSION pin" "$invalid_output"; then
  cat "$invalid_output" >&2
  fail "invalid release pins failed without the expected diagnostic"
fi

# Excluding the unpublished driver should retain the documented root-only
# dry-run path even while that driver's own pin remains intentionally invalid.
exclude_output="$TMP_DIR/excluded-driver.log"
if ! run_tag "$RELEASE_VERSION" --dry-run --exclude driver >"$exclude_output" 2>&1; then
  cat "$exclude_output" >&2
  fail "an excluded invalid module blocked a legitimate dry-run"
fi
if ! grep -Fq "create tags (1):" "$exclude_output" || \
  ! grep -Fq "  - $RELEASE_VERSION" "$exclude_output" || \
  grep -Fq "driver/mockqueue/$RELEASE_VERSION" "$exclude_output"; then
  cat "$exclude_output" >&2
  fail "exclude dry-run produced the wrong tag plan"
fi
if [[ -n "$(git -C "$FIXTURE_DIR" tag --list)" ]]; then
  fail "exclude dry-run created a tag"
fi

GOWORK=off go mod edit \
  -require="example.com/queue-release-fixture@$RELEASE_VERSION" \
  "$FIXTURE_DIR/driver/mockqueue/go.mod"
git -C "$FIXTURE_DIR" add driver/mockqueue/go.mod
git -C "$FIXTURE_DIR" commit -qm "test: pin the release fixture"

VALID_VERSION="v0.3.0-rc.1+build.01"
VALID_VERSION_DIR="$TMP_DIR/valid-version"
clone_fixture "$VALID_VERSION_DIR"
valid_version_output="$TMP_DIR/valid-version-tag.log"
if ! run_tag_in "$VALID_VERSION_DIR" "$VALID_VERSION" --exclude driver >"$valid_version_output" 2>&1; then
  cat "$valid_version_output" >&2
  fail "tag entrypoint rejected valid prerelease/build metadata"
fi
if ! (
  cd "$VALID_VERSION_DIR"
  ./scripts/plan-module-release-tags.sh "$VALID_VERSION" --exclude driver
) >"$TMP_DIR/valid-version-planner.log" 2>&1; then
  cat "$TMP_DIR/valid-version-planner.log" >&2
  fail "planner entrypoint rejected valid prerelease/build metadata"
fi
if ! (
  cd "$VALID_VERSION_DIR"
  ./scripts/check-module-inventory.sh --release-version "$VALID_VERSION" --exclude driver
) >"$TMP_DIR/valid-version-inventory.log" 2>&1; then
  cat "$TMP_DIR/valid-version-inventory.log" >&2
  fail "inventory entrypoint rejected valid prerelease/build metadata"
fi

excluded_target_output="$TMP_DIR/excluded-target.log"
if run_tag "$RELEASE_VERSION" --dry-run --exclude . >"$excluded_target_output" 2>&1; then
  fail "tagging accepted an included module whose required sibling tag was excluded"
fi
if ! grep -Fq "driver/mockqueue is included but requires excluded sibling example.com/queue-release-fixture; release $RELEASE_VERSION would omit required tag $RELEASE_VERSION" "$excluded_target_output"; then
  cat "$excluded_target_output" >&2
  fail "an excluded required sibling failed without the expected diagnostic"
fi
if [[ -n "$(git -C "$FIXTURE_DIR" tag --list)" ]]; then
  fail "an incomplete dependency tag plan created tags before the preflight failed"
fi

printf 'uncommitted fixture state\n' >"$FIXTURE_DIR/dirty-marker.txt"
dirty_dry_run_output="$TMP_DIR/dirty-dry-run.log"
if ! run_tag "$RELEASE_VERSION" --dry-run --allow-dirty >"$dirty_dry_run_output" 2>&1; then
  cat "$dirty_dry_run_output" >&2
  fail "--allow-dirty no longer supports its safe dry-run path"
fi
if ! grep -Fq "dry-run: no tags created" "$dirty_dry_run_output"; then
  cat "$dirty_dry_run_output" >&2
  fail "dirty dry-run omitted its no-mutation result"
fi

dirty_tag_output="$TMP_DIR/dirty-tag.log"
if run_tag "$RELEASE_VERSION" --allow-dirty >"$dirty_tag_output" 2>&1; then
  fail "--allow-dirty permitted real tags against uncommitted module state"
fi
if ! grep -Fq -- "--allow-dirty is only supported with --dry-run" "$dirty_tag_output"; then
  cat "$dirty_tag_output" >&2
  fail "dirty real-tag rejection omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$FIXTURE_DIR" tag --list)" ]]; then
  fail "dirty real-tag rejection created tags"
fi
git -C "$FIXTURE_DIR" add dirty-marker.txt
git -C "$FIXTURE_DIR" commit -qm "test: restore a clean release fixture"

FAKE_GIT_DIR="$TMP_DIR/fake-git"
mkdir -p "$FAKE_GIT_DIR"
cat >"$FAKE_GIT_DIR/git" <<'EOF_FAKE_GIT'
#!/usr/bin/env bash
set -euo pipefail

real_git="${RELEASE_TEST_REAL_GIT:?}"
state_file="${RELEASE_TEST_STATE_FILE:?}"
mode="${RELEASE_TEST_MUTATION_MODE:?}"
matched=0
if [[ "$mode" == "head" && "$#" -eq 3 && "$1" == "rev-parse" && "$2" == "--verify" && "$3" == "HEAD" ]]; then
  matched=1
elif [[ ( "$mode" == "dirty" || "$mode" == status-error-* ) && "$#" -eq 2 && "$1" == "status" && "$2" == "--porcelain" ]]; then
  matched=1
fi

if [[ "$matched" -eq 1 ]]; then
  count=0
  if [[ -f "$state_file" ]]; then
    count="$(<"$state_file")"
  fi
  count=$((count + 1))
  printf '%s\n' "$count" >"$state_file"
  if [[ "$mode" == "status-error-initial" && "$count" -eq 1 ]]; then
    exit 73
  fi
  if [[ "$mode" == "status-error-final" && "$count" -eq 2 ]]; then
    exit 74
  fi
  if [[ "$count" -eq 2 ]]; then
    if [[ "$mode" == "head" ]]; then
      "$real_git" update-ref HEAD "${RELEASE_TEST_TARGET_HEAD:?}" "${RELEASE_TEST_ORIGINAL_HEAD:?}"
    else
      printf 'changed during release planning\n' >"${RELEASE_TEST_REPOSITORY:?}/final-state-dirty.txt"
    fi
  fi
fi

exec "$real_git" "$@"
EOF_FAKE_GIT
chmod +x "$FAKE_GIT_DIR/git"

HEAD_CHANGE_DIR="$TMP_DIR/head-change"
clone_fixture "$HEAD_CHANGE_DIR"
original_head="$(git -C "$HEAD_CHANGE_DIR" rev-parse HEAD)"
original_tree="$(git -C "$HEAD_CHANGE_DIR" rev-parse "HEAD^{tree}")"
changed_head="$(printf 'test: concurrent head change\n' | git -C "$HEAD_CHANGE_DIR" commit-tree "$original_tree" -p "$original_head")"
head_change_output="$TMP_DIR/head-change.log"
if (
  cd "$HEAD_CHANGE_DIR"
  PATH="$FAKE_GIT_DIR:$PATH" \
    RELEASE_TEST_REAL_GIT="$GIT_BIN" \
    RELEASE_TEST_STATE_FILE="$TMP_DIR/head-change.state" \
    RELEASE_TEST_MUTATION_MODE="head" \
    RELEASE_TEST_ORIGINAL_HEAD="$original_head" \
    RELEASE_TEST_TARGET_HEAD="$changed_head" \
    RELEASE_TEST_REPOSITORY="$HEAD_CHANGE_DIR" \
    ./scripts/tag-all-modules.sh "$RELEASE_VERSION"
) >"$head_change_output" 2>&1; then
  fail "a concurrent HEAD change passed the final release-state check"
fi
if ! grep -Fq "HEAD changed during release planning; expected $original_head, found $changed_head" "$head_change_output"; then
  cat "$head_change_output" >&2
  fail "concurrent HEAD rejection omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$HEAD_CHANGE_DIR" tag --list)" ]]; then
  fail "concurrent HEAD rejection created local tags"
fi

FINAL_DIRTY_DIR="$TMP_DIR/final-dirty"
clone_fixture "$FINAL_DIRTY_DIR"
final_dirty_output="$TMP_DIR/final-dirty.log"
if (
  cd "$FINAL_DIRTY_DIR"
  PATH="$FAKE_GIT_DIR:$PATH" \
    RELEASE_TEST_REAL_GIT="$GIT_BIN" \
    RELEASE_TEST_STATE_FILE="$TMP_DIR/final-dirty.state" \
    RELEASE_TEST_MUTATION_MODE="dirty" \
    RELEASE_TEST_ORIGINAL_HEAD="unused" \
    RELEASE_TEST_TARGET_HEAD="unused" \
    RELEASE_TEST_REPOSITORY="$FINAL_DIRTY_DIR" \
    ./scripts/tag-all-modules.sh "$RELEASE_VERSION"
) >"$final_dirty_output" 2>&1; then
  fail "a concurrent working-tree change passed the final release-state check"
fi
if ! grep -Fq "working tree changed during release planning; refusing to mutate tags" "$final_dirty_output"; then
  cat "$final_dirty_output" >&2
  fail "concurrent working-tree rejection omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$FINAL_DIRTY_DIR" tag --list)" ]]; then
  fail "concurrent working-tree rejection created local tags"
fi

STATUS_INITIAL_DIR="$TMP_DIR/status-initial"
clone_fixture "$STATUS_INITIAL_DIR"
status_initial_output="$TMP_DIR/status-initial.log"
if (
  cd "$STATUS_INITIAL_DIR"
  PATH="$FAKE_GIT_DIR:$PATH" \
    RELEASE_TEST_REAL_GIT="$GIT_BIN" \
    RELEASE_TEST_STATE_FILE="$TMP_DIR/status-initial.state" \
    RELEASE_TEST_MUTATION_MODE="status-error-initial" \
    RELEASE_TEST_ORIGINAL_HEAD="unused" \
    RELEASE_TEST_TARGET_HEAD="unused" \
    RELEASE_TEST_REPOSITORY="$STATUS_INITIAL_DIR" \
    ./scripts/tag-all-modules.sh "$RELEASE_VERSION"
) >"$status_initial_output" 2>&1; then
  fail "an initial git status failure was treated as a clean tree"
fi
if ! grep -Fq "failed to inspect working tree before release planning (git status exit 73)" "$status_initial_output"; then
  cat "$status_initial_output" >&2
  fail "initial git status failure omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$STATUS_INITIAL_DIR" tag --list)" ]]; then
  fail "initial git status failure created local tags"
fi

STATUS_FINAL_DIR="$TMP_DIR/status-final"
clone_fixture "$STATUS_FINAL_DIR"
status_final_output="$TMP_DIR/status-final.log"
if (
  cd "$STATUS_FINAL_DIR"
  PATH="$FAKE_GIT_DIR:$PATH" \
    RELEASE_TEST_REAL_GIT="$GIT_BIN" \
    RELEASE_TEST_STATE_FILE="$TMP_DIR/status-final.state" \
    RELEASE_TEST_MUTATION_MODE="status-error-final" \
    RELEASE_TEST_ORIGINAL_HEAD="unused" \
    RELEASE_TEST_TARGET_HEAD="unused" \
    RELEASE_TEST_REPOSITORY="$STATUS_FINAL_DIR" \
    ./scripts/tag-all-modules.sh "$RELEASE_VERSION"
) >"$status_final_output" 2>&1; then
  fail "a final git status failure was treated as a clean tree"
fi
if ! grep -Fq "failed to inspect working tree immediately before tag mutation (git status exit 74)" "$status_final_output"; then
  cat "$status_final_output" >&2
  fail "final git status failure omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$STATUS_FINAL_DIR" tag --list)" ]]; then
  fail "final git status failure created local tags"
fi

REMOTE_DIR="$TMP_DIR/remote.git"
BROKEN_REMOTE_DIR="$TMP_DIR/missing-remote.git"
git init --bare -q "$REMOTE_DIR"
git -C "$FIXTURE_DIR" remote add origin "$REMOTE_DIR"
git -C "$FIXTURE_DIR" remote add broken "$BROKEN_REMOTE_DIR"

empty_remote_output="$TMP_DIR/empty-remote.log"
if ! run_tag "$RELEASE_VERSION" --dry-run --push >"$empty_remote_output" 2>&1; then
  cat "$empty_remote_output" >&2
  fail "an empty reachable remote was not treated as having no release tags"
fi
if ! grep -Fq "push tags (2):" "$empty_remote_output"; then
  cat "$empty_remote_output" >&2
  fail "empty-remote dry-run produced the wrong push plan"
fi

broken_remote_output="$TMP_DIR/broken-remote.log"
if run_tag "$RELEASE_VERSION" --dry-run --push --remote broken >"$broken_remote_output" 2>&1; then
  fail "an unreachable remote was treated as an absent tag during dry-run"
fi
if ! grep -Fq "failed to query remote for tag $RELEASE_VERSION" "$broken_remote_output"; then
  cat "$broken_remote_output" >&2
  fail "unreachable-remote dry-run omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$FIXTURE_DIR" tag --list)" ]]; then
  fail "remote-query dry-run created local tags"
fi

broken_push_output="$TMP_DIR/broken-push.log"
if run_tag "$RELEASE_VERSION" --push --remote broken >"$broken_push_output" 2>&1; then
  fail "an unreachable remote allowed a real tag operation to continue"
fi
if ! grep -Fq "failed to query remote for tag $RELEASE_VERSION" "$broken_push_output"; then
  cat "$broken_push_output" >&2
  fail "unreachable real push omitted the expected diagnostic"
fi
if [[ -n "$(git -C "$FIXTURE_DIR" tag --list)" ]]; then
  fail "remote query failure created local tags"
fi

STALE_LOCAL_DIR="$TMP_DIR/stale-local"
clone_fixture "$STALE_LOCAL_DIR"
stale_local_commit="$(git -C "$STALE_LOCAL_DIR" rev-parse HEAD)"
git -C "$STALE_LOCAL_DIR" tag -a "$RELEASE_VERSION" -m "stale root" "$stale_local_commit"
git -C "$STALE_LOCAL_DIR" tag -a "driver/mockqueue/$RELEASE_VERSION" -m "stale driver" "$stale_local_commit"
printf 'advance past local tags\n' >"$STALE_LOCAL_DIR/stale-local-marker.txt"
git -C "$STALE_LOCAL_DIR" add stale-local-marker.txt
git -C "$STALE_LOCAL_DIR" commit -qm "test: advance past local tags"
stale_local_head="$(git -C "$STALE_LOCAL_DIR" rev-parse HEAD)"
stale_local_output="$TMP_DIR/stale-local.log"
if run_tag_in "$STALE_LOCAL_DIR" "$RELEASE_VERSION" --skip-existing >"$stale_local_output" 2>&1; then
  fail "--skip-existing accepted a stale local release tag"
fi
if ! grep -Fq "local tag $RELEASE_VERSION resolves to $stale_local_commit; --skip-existing requires HEAD $stale_local_head" "$stale_local_output"; then
  cat "$stale_local_output" >&2
  fail "stale local tag rejection omitted the expected peeled commit diagnostic"
fi
if [[ "$(git -C "$STALE_LOCAL_DIR" tag --list | wc -l | tr -d ' ')" != "2" ]]; then
  fail "stale local tag rejection mutated the local tag family"
fi

STALE_REMOTE_DIR="$TMP_DIR/stale-remote"
STALE_REMOTE_BARE_DIR="$TMP_DIR/stale-remote.git"
clone_fixture "$STALE_REMOTE_DIR"
create_bare_remote "$STALE_REMOTE_BARE_DIR"
stale_remote_commit="$(git -C "$STALE_REMOTE_DIR" rev-parse HEAD)"
git -C "$STALE_REMOTE_BARE_DIR" tag -a "$RELEASE_VERSION" -m "stale remote root" "$stale_remote_commit"
git -C "$STALE_REMOTE_BARE_DIR" tag -a "driver/mockqueue/$RELEASE_VERSION" -m "stale remote driver" "$stale_remote_commit"
git -C "$STALE_REMOTE_DIR" remote set-url origin "$STALE_REMOTE_BARE_DIR"
printf 'advance past remote tags\n' >"$STALE_REMOTE_DIR/stale-remote-marker.txt"
git -C "$STALE_REMOTE_DIR" add stale-remote-marker.txt
git -C "$STALE_REMOTE_DIR" commit -qm "test: advance past remote tags"
stale_remote_head="$(git -C "$STALE_REMOTE_DIR" rev-parse HEAD)"
stale_remote_output="$TMP_DIR/stale-remote.log"
if run_tag_in "$STALE_REMOTE_DIR" "$RELEASE_VERSION" --skip-existing --dry-run --push >"$stale_remote_output" 2>&1; then
  fail "--skip-existing accepted a stale remote release tag"
fi
if ! grep -Fq "remote tag $RELEASE_VERSION resolves to $stale_remote_commit; --skip-existing requires HEAD $stale_remote_head" "$stale_remote_output"; then
  cat "$stale_remote_output" >&2
  fail "stale remote tag rejection omitted the expected peeled commit diagnostic"
fi
if [[ -n "$(git -C "$STALE_REMOTE_DIR" tag --list)" ]]; then
  fail "stale remote tag rejection created local tags"
fi

REUSE_DIR="$TMP_DIR/reuse"
REUSE_REMOTE_DIR="$TMP_DIR/reuse.git"
clone_fixture "$REUSE_DIR"
create_bare_remote "$REUSE_REMOTE_DIR"
reuse_head="$(git -C "$REUSE_DIR" rev-parse HEAD)"
git -C "$REUSE_DIR" tag -a "$RELEASE_VERSION" -m "reusable root" "$reuse_head"
git -C "$REUSE_REMOTE_DIR" tag -a "driver/mockqueue/$RELEASE_VERSION" -m "reusable driver" "$reuse_head"
git -C "$REUSE_DIR" remote set-url origin "$REUSE_REMOTE_DIR"
reuse_output="$TMP_DIR/reuse.log"
if ! run_tag_in "$REUSE_DIR" "$RELEASE_VERSION" --skip-existing --dry-run --push >"$reuse_output" 2>&1; then
  cat "$reuse_output" >&2
  fail "--skip-existing rejected same-HEAD local or remote tags"
fi
if ! grep -Fq "reuse local tag for push: $RELEASE_VERSION" "$reuse_output" || \
  ! grep -Fq "skip existing: driver/mockqueue/$RELEASE_VERSION" "$reuse_output"; then
  cat "$reuse_output" >&2
  fail "same-HEAD reuse produced the wrong push plan"
fi

ATOMIC_DIR="$TMP_DIR/atomic"
ATOMIC_REMOTE_DIR="$TMP_DIR/atomic.git"
clone_fixture "$ATOMIC_DIR"
create_bare_remote "$ATOMIC_REMOTE_DIR"
git -C "$ATOMIC_DIR" remote set-url origin "$ATOMIC_REMOTE_DIR"
atomic_trace="$TMP_DIR/atomic-push.trace"
atomic_output="$TMP_DIR/atomic-push.log"
if ! GIT_TRACE="$atomic_trace" run_tag_in "$ATOMIC_DIR" "$RELEASE_VERSION" --push >"$atomic_output" 2>&1; then
  cat "$atomic_output" >&2
  fail "the synchronized atomic family push failed"
fi
if ! grep -Fq "git push --atomic origin refs/tags/$RELEASE_VERSION:refs/tags/$RELEASE_VERSION refs/tags/driver/mockqueue/$RELEASE_VERSION:refs/tags/driver/mockqueue/$RELEASE_VERSION" "$atomic_trace"; then
  cat "$atomic_trace" >&2
  fail "the family push was not atomic with fully qualified tag refspecs"
fi
expected_tags=$'driver/mockqueue/v0.3.0\nv0.3.0'
atomic_local_tags="$(git -C "$ATOMIC_DIR" tag --list | LC_ALL=C sort)"
atomic_remote_tags="$(git -C "$ATOMIC_REMOTE_DIR" tag --list | LC_ALL=C sort)"
if [[ "$atomic_local_tags" != "$expected_tags" || "$atomic_remote_tags" != "$expected_tags" ]]; then
  printf 'expected tags:\n%s\nlocal tags:\n%s\nremote tags:\n%s\n' "$expected_tags" "$atomic_local_tags" "$atomic_remote_tags" >&2
  fail "the atomic push did not publish the complete tag family"
fi

ATOMIC_REJECT_DIR="$TMP_DIR/atomic-reject"
ATOMIC_REJECT_REMOTE_DIR="$TMP_DIR/atomic-reject.git"
clone_fixture "$ATOMIC_REJECT_DIR"
create_bare_remote "$ATOMIC_REJECT_REMOTE_DIR"
git -C "$ATOMIC_REJECT_DIR" remote set-url origin "$ATOMIC_REJECT_REMOTE_DIR"
cat >"$ATOMIC_REJECT_REMOTE_DIR/hooks/update" <<EOF_UPDATE_HOOK
#!/bin/sh
if [ "\$1" = "refs/tags/driver/mockqueue/$RELEASE_VERSION" ]; then
  exit 1
fi
exit 0
EOF_UPDATE_HOOK
chmod +x "$ATOMIC_REJECT_REMOTE_DIR/hooks/update"
atomic_reject_output="$TMP_DIR/atomic-reject.log"
if run_tag_in "$ATOMIC_REJECT_DIR" "$RELEASE_VERSION" --push >"$atomic_reject_output" 2>&1; then
  fail "a rejected member unexpectedly allowed the atomic family push"
fi
if [[ -n "$(git -C "$ATOMIC_REJECT_REMOTE_DIR" tag --list)" ]]; then
  cat "$atomic_reject_output" >&2
  fail "a rejected atomic family push partially updated the remote"
fi

valid_output="$TMP_DIR/valid-pins.log"
if ! run_tag "$RELEASE_VERSION" >"$valid_output" 2>&1; then
  cat "$valid_output" >&2
  fail "valid synchronized pins did not produce a tag family"
fi
actual_tags="$(git -C "$FIXTURE_DIR" tag --list | LC_ALL=C sort)"
if [[ "$actual_tags" != "$expected_tags" ]]; then
  printf 'expected tags:\n%s\nactual tags:\n%s\n' "$expected_tags" "$actual_tags" >&2
  fail "valid synchronized pins produced the wrong tag family"
fi

if ! (
  cd "$FIXTURE_DIR"
  ./scripts/check-module-inventory.sh --tag-version "$RELEASE_VERSION"
) >"$TMP_DIR/tag-family.log" 2>&1; then
  cat "$TMP_DIR/tag-family.log" >&2
  fail "the synchronized fixture tags failed the ordinary tag-family guard"
fi

if ! run_tag "$RELEASE_VERSION" --skip-existing >"$TMP_DIR/skip-existing.log" 2>&1; then
  cat "$TMP_DIR/skip-existing.log" >&2
  fail "the release preflight broke the documented skip-existing path"
fi

inventory_stdout="$TMP_DIR/inventory-stdout.log"
inventory_stderr="$TMP_DIR/inventory-stderr.log"
if ! (
  cd "$FIXTURE_DIR"
  ./scripts/check-module-inventory.sh
) >"$inventory_stdout" 2>"$inventory_stderr"; then
  cat "$inventory_stdout" >&2
  cat "$inventory_stderr" >&2
  fail "the ordinary inventory guard failed after the release contracts"
fi
if [[ -s "$inventory_stderr" ]]; then
  cat "$inventory_stderr" >&2
  fail "the ordinary inventory guard emitted non-portable parser diagnostics"
fi

echo "release script contract: strict versions, path majors, captured-HEAD planning, fail-closed status, final-state checks, dependency closure, dirty-tree safety, remote queries, safe reuse, atomic push, excludes, dry-run, and synchronized tags OK"
