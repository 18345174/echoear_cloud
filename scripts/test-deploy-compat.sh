#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY_SCRIPT="$ROOT_DIR/deploy/oci/deploy.sh"
TEMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEMP_ROOT"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "$expected" "$file" || fail "$file does not contain: $expected"
}

assert_no_next_files() {
  local stack="$1"
  if find "$stack" -maxdepth 1 -name '*.next' -print -quit | grep -q .; then
    fail "temporary deployment files remain in $stack"
  fi
}

make_fake_commands() {
  local bin_dir="$1"

  cat >"$bin_dir/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
output=""
url=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --output)
      output="$2"
      shift 2
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "$output" ]]; then
  printf '{"status":"ok"}'
  exit 0
fi

name="${url##*/}"
if [[ "$SCENARIO" == "missing_validate" && "$name" == "validate-relay.sh" ]]; then
  exit 22
fi
cp "$FIXTURE_DIR/$name" "$output"
EOF

  cat >"$bin_dir/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$DOCKER_LOG"
if [[ "$SCENARIO" == "invalid_compose" && "$*" == *"config --quiet"* ]]; then
  exit 1
fi
EOF

  chmod +x "$bin_dir/curl" "$bin_dir/docker"
}

make_fixture() {
  local fixture="$1"
  printf 'services:\n  api:\n    image: fixture-new\n' >"$fixture/docker-compose.yml"
  cat >"$fixture/deploy.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'new deploy executed\n' >>"$DEPLOY_MARKER"
EOF
  printf '#!/usr/bin/env bash\nexit 0\n' >"$fixture/validate-relay.sh"
  chmod +x "$fixture/deploy.sh" "$fixture/validate-relay.sh"
}

prepare_case() {
  local case_dir="$1"
  mkdir -p "$case_dir/stack" "$case_dir/bin" "$case_dir/fixture"
  printf 'services:\n  api:\n    image: installed-old\n' >"$case_dir/stack/docker-compose.yml"
  cat >"$case_dir/stack/.env" <<'EOF'
POSTGRES_PASSWORD=test
BOOTSTRAP_ADMIN_PASSWORD=test
PUBLIC_BASE_URL=https://example.invalid
ECHOEAR_RELAY_ENABLED=false
EOF
  cp "$DEPLOY_SCRIPT" "$case_dir/stack/deploy.sh"
  chmod +x "$case_dir/stack/deploy.sh"
  make_fixture "$case_dir/fixture"
  make_fake_commands "$case_dir/bin"
}

run_case() {
  local name="$1"
  local scenario="$2"
  local required="$3"
  local case_dir="$TEMP_ROOT/$name"
  prepare_case "$case_dir"
  cp "$case_dir/stack/.env" "$case_dir/env.before"
  cp "$case_dir/stack/docker-compose.yml" "$case_dir/compose.before"

  set +e
  PATH="$case_dir/bin:$PATH" \
    SCENARIO="$scenario" \
    FIXTURE_DIR="$case_dir/fixture" \
    DOCKER_LOG="$case_dir/docker.log" \
    DEPLOY_MARKER="$case_dir/deploy.marker" \
    ECHOEAR_STACK_DIR="$case_dir/stack" \
    ECHOEAR_DEPLOY_SYNC_REQUIRED="$required" \
    "$case_dir/stack/deploy.sh" >"$case_dir/output.log" 2>&1
  local status=$?
  set -e

  cmp "$case_dir/env.before" "$case_dir/stack/.env" || fail "$name changed .env"
  assert_no_next_files "$case_dir/stack"
  CASE_DIR="$case_dir"
  CASE_STATUS="$status"
}

run_case missing_file_fallback missing_validate false
[[ "$CASE_STATUS" -eq 0 ]] || fail "missing-file fallback exited $CASE_STATUS"
cmp "$CASE_DIR/compose.before" "$CASE_DIR/stack/docker-compose.yml" || fail "fallback replaced installed Compose file"
assert_contains "$CASE_DIR/output.log" "keeping the installed files and continuing"
assert_contains "$CASE_DIR/docker.log" "pull"
assert_contains "$CASE_DIR/docker.log" "up -d --remove-orphans"

run_case invalid_compose_fallback invalid_compose false
[[ "$CASE_STATUS" -eq 0 ]] || fail "invalid-Compose fallback exited $CASE_STATUS"
cmp "$CASE_DIR/compose.before" "$CASE_DIR/stack/docker-compose.yml" || fail "invalid Compose replaced installed file"
assert_contains "$CASE_DIR/docker.log" "pull"

run_case missing_file_strict missing_validate true
[[ "$CASE_STATUS" -ne 0 ]] || fail "strict sync unexpectedly succeeded"
cmp "$CASE_DIR/compose.before" "$CASE_DIR/stack/docker-compose.yml" || fail "strict failure replaced installed file"
assert_contains "$CASE_DIR/output.log" "ECHOEAR_DEPLOY_SYNC_REQUIRED=true"
if [[ -f "$CASE_DIR/docker.log" ]] && grep -Fq ' pull' "$CASE_DIR/docker.log"; then
  fail "strict sync failure continued to image pull"
fi

run_case successful_upgrade success false
[[ "$CASE_STATUS" -eq 0 ]] || fail "successful upgrade exited $CASE_STATUS"
assert_contains "$CASE_DIR/stack/docker-compose.yml" "fixture-new"
assert_contains "$CASE_DIR/deploy.marker" "new deploy executed"

printf 'deploy compatibility tests passed\n'
