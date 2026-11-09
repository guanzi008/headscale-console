#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY="${1:-$ROOT_DIR/dist/headscale-console}"
MOCKOIDC_BIN="${MOCKOIDC_BIN:-$ROOT_DIR/.cache/build/headscale}"
IMAGE="${IMAGE:-headscale-console:docker-smoke}"
case "$BINARY" in
	/*) ;;
	*) BINARY="$ROOT_DIR/$BINARY" ;;
esac
case "$MOCKOIDC_BIN" in
	/*) ;;
	*) MOCKOIDC_BIN="$ROOT_DIR/$MOCKOIDC_BIN" ;;
esac

PUBLIC_PORT="${PUBLIC_PORT:-18080}"
IDP_PORT="${IDP_PORT:-18070}"
HEADPLANE_PORT="${HEADPLANE_PORT:-18090}"
HEADSCALE_HTTP_PORT="${HEADSCALE_HTTP_PORT:-18081}"
HEADSCALE_GRPC_PORT="${HEADSCALE_GRPC_PORT:-18082}"
HEADSCALE_METRICS_PORT="${HEADSCALE_METRICS_PORT:-18083}"
MOCKOIDC_PORT="${MOCKOIDC_PORT:-18071}"

BASE_URL="http://127.0.0.1:${PUBLIC_PORT}"
MOCKOIDC_URL="http://127.0.0.1:${MOCKOIDC_PORT}"
CONTAINER_NAME="headscale-console-smoke-$$"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/headscale-console-smoke.XXXXXX")"
MOCKOIDC_PID=""
CONTAINER_STARTED=0

fail() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

assert_contains() {
	local file="$1"
	local expected="$2"
	grep -Fq -- "$expected" "$file" || fail "expected $(basename "$file") to contain: $expected"
}

assert_matches() {
	local file="$1"
	local pattern="$2"
	grep -Eq -- "$pattern" "$file" || fail "expected $(basename "$file") to match: $pattern"
}

assert_equals() {
	local actual="$1"
	local expected="$2"
	local description="$3"
	[[ "$actual" == "$expected" ]] || fail "$description: got '$actual', want '$expected'"
}

csrf_token() {
	sed -n 's/.*name="csrf_token" value="\([^"]*\)".*/\1/p' "$1" | head -n 1
}

wait_for_url() {
	local endpoint="$1"
	local timeout_seconds="${2:-120}"
	local attempts=$((timeout_seconds * 2))
	for ((i = 0; i < attempts; i++)); do
		if curl -fsS --max-time 2 "$endpoint" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.5
	done
	return 1
}

check_port_available() {
	local port="$1"
	if command -v ss >/dev/null 2>&1; then
		if ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}$"; then
			fail "port ${port} is already in use"
		fi
	fi
}

cleanup() {
	local status=$?
	set +e
	trap - EXIT INT TERM
	if [[ "$status" -ne 0 && "$CONTAINER_STARTED" -eq 1 ]]; then
		printf '\n--- container logs ---\n' >&2
		docker logs "$CONTAINER_NAME" 2>&1 | tail -n 200 >&2
	fi
	if [[ "$status" -ne 0 && -f "$WORK_DIR/mockoidc.log" ]]; then
		printf '\n--- mock OIDC logs ---\n' >&2
		tail -n 100 "$WORK_DIR/mockoidc.log" >&2
	fi
	docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
	if [[ -n "$MOCKOIDC_PID" ]]; then
		kill "$MOCKOIDC_PID" >/dev/null 2>&1 || true
		wait "$MOCKOIDC_PID" >/dev/null 2>&1 || true
	fi
	if [[ -d "$WORK_DIR/data" ]] && docker image inspect "$IMAGE" >/dev/null 2>&1; then
		docker run --rm --user 0 --entrypoint sh \
			-v "$WORK_DIR/data:/data" "$IMAGE" \
			-c "chown -R $(id -u):$(id -g) /data" >/dev/null 2>&1 || true
	fi
	if [[ "${KEEP_SMOKE_DATA:-0}" == "1" ]]; then
		printf 'smoke test data kept at %s\n' "$WORK_DIR" >&2
	else
		rm -rf "$WORK_DIR"
	fi
	exit "$status"
}
trap cleanup EXIT INT TERM

for command in docker curl jq; do
	command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done
[[ -x "$BINARY" ]] || fail "binary not found or not executable: $BINARY"
[[ -x "$MOCKOIDC_BIN" ]] || fail "mock OIDC binary not found: $MOCKOIDC_BIN (run ./build.sh first)"
[[ "$BINARY" == "$ROOT_DIR"/* ]] || fail "binary must be inside the Docker build context: $ROOT_DIR"
BINARY_RELATIVE="${BINARY#"$ROOT_DIR"/}"

for port in "$PUBLIC_PORT" "$IDP_PORT" "$HEADPLANE_PORT" "$HEADSCALE_HTTP_PORT" "$HEADSCALE_GRPC_PORT" "$HEADSCALE_METRICS_PORT" "$MOCKOIDC_PORT"; do
	check_port_available "$port"
done

mkdir -p "$WORK_DIR/data"
chmod 0777 "$WORK_DIR/data"

cat >"$WORK_DIR/config.yaml" <<EOF
server:
  listen: 0.0.0.0:${PUBLIC_PORT}
  base_url: ${BASE_URL}
  admin_path: /admin
  internal:
    idp: 127.0.0.1:${IDP_PORT}
    headplane: 127.0.0.1:${HEADPLANE_PORT}
    headscale_http: 127.0.0.1:${HEADSCALE_HTTP_PORT}
    headscale_grpc: 127.0.0.1:${HEADSCALE_GRPC_PORT}
    headscale_metrics: 127.0.0.1:${HEADSCALE_METRICS_PORT}
registration:
  enabled: true
  password_min_length: 10
headscale:
  base_domain: tailnet.internal
  derp_urls:
    - https://controlplane.tailscale.com/derpmap/default
  dns_nameservers:
    - 1.1.1.1
    - 1.0.0.1
oauth_providers:
  - name: mock
    type: oidc
    issuer: ${MOCKOIDC_URL}/oidc
    client_id: mock-client
    client_secret: mock-secret
EOF
chmod 0644 "$WORK_DIR/config.yaml"

printf '==> Building Docker image %s\n' "$IMAGE"
docker build --quiet --build-arg "BINARY_PATH=${BINARY_RELATIVE}" -t "$IMAGE" "$ROOT_DIR" >/dev/null

printf '==> Starting mock OIDC on %s\n' "$MOCKOIDC_URL"
MOCKOIDC_CLIENT_ID=mock-client \
MOCKOIDC_CLIENT_SECRET=mock-secret \
MOCKOIDC_ADDR=127.0.0.1 \
MOCKOIDC_PORT="$MOCKOIDC_PORT" \
MOCKOIDC_USERS='[
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"},
  {"Subject":"usr_test","Email":"test@example.com","EmailVerified":true,"PreferredUsername":"test"}
]' \
	"$MOCKOIDC_BIN" mockoidc >"$WORK_DIR/mockoidc.log" 2>&1 &
MOCKOIDC_PID=$!
wait_for_url "$MOCKOIDC_URL/oidc/.well-known/openid-configuration" 30 || fail "mock OIDC did not start"

printf '==> Starting single binary container\n'
docker run -d \
	--name "$CONTAINER_NAME" \
	--network host \
	-v "$WORK_DIR/data:/data" \
	-v "$WORK_DIR/config.yaml:/config.yaml:ro" \
	"$IMAGE" --data-dir /data --config /config.yaml >/dev/null
CONTAINER_STARTED=1

wait_for_url "$BASE_URL/health" 120 || fail "Headscale did not become healthy"
wait_for_url "$BASE_URL/idp/healthz" 30 || fail "account center did not become healthy"
wait_for_url "$BASE_URL/admin/healthz" 120 || fail "Headplane did not become healthy"

printf '==> Checking Headscale and default policy\n'
assert_equals "$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/version")" "200" "Headscale version endpoint"
for ((i = 0; i < 100; i++)); do
	if docker exec "$CONTAINER_NAME" test -f /data/secrets/headscale-api-key >/dev/null 2>&1; then
		break
	fi
	sleep 0.2
done
docker exec "$CONTAINER_NAME" test -f /data/secrets/headscale-api-key >/dev/null 2>&1 \
	|| fail "Headscale API key was not created"
API_KEY="$(docker exec "$CONTAINER_NAME" cat /data/secrets/headscale-api-key)"
POLICY_STATUS="$(curl -sS -o "$WORK_DIR/policy.json" -w '%{http_code}' \
	-H "Authorization: Bearer ${API_KEY}" "$BASE_URL/api/v1/policy")"
assert_equals "$POLICY_STATUS" "200" "Headscale policy endpoint"
jq -e '.policy | contains("acls")' "$WORK_DIR/policy.json" >/dev/null || fail "default ACL policy was not initialized"

printf '==> Testing account center language switching\n'
curl -fsS -H 'Accept-Language: zh-CN,zh;q=0.9' -o "$WORK_DIR/login-zh.html" "$BASE_URL/idp/login"
assert_contains "$WORK_DIR/login-zh.html" '<html lang="zh-CN">'
assert_contains "$WORK_DIR/login-zh.html" "登录账号中心"
curl -fsS -H 'Accept-Language: en-US,en;q=0.9' -o "$WORK_DIR/login-en.html" "$BASE_URL/idp/login"
assert_contains "$WORK_DIR/login-en.html" '<html lang="en">'
assert_contains "$WORK_DIR/login-en.html" "Sign in to the account center"

LANG_JAR="$WORK_DIR/language.cookies"
curl -fsS -L -c "$LANG_JAR" -b "$LANG_JAR" -o "$WORK_DIR/login-stored-zh.html" \
	"$BASE_URL/idp/language?lang=zh-CN&return_to=%2Fidp%2Flogin"
assert_contains "$WORK_DIR/login-stored-zh.html" "登录账号中心"
curl -fsS -L -c "$LANG_JAR" -b "$LANG_JAR" -o "$WORK_DIR/login-stored-en.html" \
	"$BASE_URL/idp/language?lang=en&return_to=%2Fidp%2Flogin"
assert_contains "$WORK_DIR/login-stored-en.html" "Sign in to the account center"

printf '==> Testing registration and password login\n'
REGISTER_JAR="$WORK_DIR/register.cookies"
curl -fsS -c "$REGISTER_JAR" -b "$REGISTER_JAR" -o "$WORK_DIR/register.html" "$BASE_URL/idp/register"
REGISTER_CSRF="$(csrf_token "$WORK_DIR/register.html")"
[[ -n "$REGISTER_CSRF" ]] || fail "registration CSRF token not found"
REGISTER_STATUS="$(curl -sS -c "$REGISTER_JAR" -b "$REGISTER_JAR" -o /dev/null -w '%{http_code}' \
	--data-urlencode "csrf_token=${REGISTER_CSRF}" \
	--data-urlencode "display_name=测试用户" \
	--data-urlencode "email=test@example.com" \
	--data-urlencode "password=correct-horse-battery" \
	--data-urlencode "password_confirm=correct-horse-battery" \
	--data-urlencode "return_to=" \
	"$BASE_URL/idp/register")"
assert_equals "$REGISTER_STATUS" "303" "registration"
curl -fsS -c "$REGISTER_JAR" -b "$REGISTER_JAR" -o "$WORK_DIR/registered-account.html" "$BASE_URL/idp/account"
assert_contains "$WORK_DIR/registered-account.html" "test@example.com"
assert_contains "$WORK_DIR/registered-account.html" "Not linked"

USER_JAR="$WORK_DIR/user.cookies"
curl -fsS -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/login.html" "$BASE_URL/idp/login"
LOGIN_CSRF="$(csrf_token "$WORK_DIR/login.html")"
[[ -n "$LOGIN_CSRF" ]] || fail "login CSRF token not found"
LOGIN_STATUS="$(curl -sS -c "$USER_JAR" -b "$USER_JAR" -o /dev/null -w '%{http_code}' \
	--data-urlencode "csrf_token=${LOGIN_CSRF}" \
	--data-urlencode "email=test@example.com" \
	--data-urlencode "password=correct-horse-battery" \
	--data-urlencode "return_to=/idp/account" \
	"$BASE_URL/idp/login")"
assert_equals "$LOGIN_STATUS" "303" "password login"
curl -fsS -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/account.html" "$BASE_URL/idp/account"
assert_contains "$WORK_DIR/account.html" "Your account"

printf '==> Testing unbound third-party login rejection\n'
UNBOUND_JAR="$WORK_DIR/unbound.cookies"
UNBOUND_FINAL="$(curl -sS -L -c "$UNBOUND_JAR" -b "$UNBOUND_JAR" -o "$WORK_DIR/unbound.html" \
	-w '%{url_effective}' "$BASE_URL/idp/login/mock")"
[[ "$UNBOUND_FINAL" == *"error=unbound"* ]] || fail "unbound third-party login was not rejected: $UNBOUND_FINAL"
assert_contains "$WORK_DIR/unbound.html" "not linked yet"

printf '==> Testing third-party account binding\n'
curl -fsS -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/account-before-link.html" "$BASE_URL/idp/account"
LINK_CSRF="$(csrf_token "$WORK_DIR/account-before-link.html")"
[[ -n "$LINK_CSRF" ]] || fail "account CSRF token not found"
LINK_FINAL="$(curl -sS -L -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/account-linked.html" \
	-w '%{url_effective}' \
	--data-urlencode "csrf_token=${LINK_CSRF}" \
	--data-urlencode "provider=mock" \
	"$BASE_URL/idp/account/link")"
[[ "$LINK_FINAL" == *"/idp/account"* ]] || fail "binding did not return to the account page: $LINK_FINAL"
assert_contains "$WORK_DIR/account-linked.html" "Linked"
assert_contains "$WORK_DIR/account-linked.html" "Third-party account linked."

printf '==> Testing login through the bound third-party account\n'
curl -fsS -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/account-before-logout.html" "$BASE_URL/idp/account"
LOGOUT_CSRF="$(csrf_token "$WORK_DIR/account-before-logout.html")"
LOGOUT_STATUS="$(curl -sS -c "$USER_JAR" -b "$USER_JAR" -o /dev/null -w '%{http_code}' \
	--data-urlencode "csrf_token=${LOGOUT_CSRF}" "$BASE_URL/idp/logout")"
assert_equals "$LOGOUT_STATUS" "303" "logout"
EXTERNAL_FINAL="$(curl -sS -L -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/account-external.html" \
	-w '%{url_effective}' "$BASE_URL/idp/login/mock")"
[[ "$EXTERNAL_FINAL" == *"/idp/account"* ]] || fail "third-party login failed: $EXTERNAL_FINAL"
assert_contains "$WORK_DIR/account-external.html" "Your account"
assert_contains "$WORK_DIR/account-external.html" "test@example.com"

printf '==> Testing third-party account unlinking\n'
UNLINK_CSRF="$(csrf_token "$WORK_DIR/account-external.html")"
UNLINK_FINAL="$(curl -sS -L -c "$USER_JAR" -b "$USER_JAR" -o "$WORK_DIR/account-unlinked.html" \
	-w '%{url_effective}' \
	--data-urlencode "csrf_token=${UNLINK_CSRF}" \
	--data-urlencode "provider=mock" \
	"$BASE_URL/idp/account/unlink")"
[[ "$UNLINK_FINAL" == *"/idp/account"* ]] || fail "unlinking did not return to the account page: $UNLINK_FINAL"
assert_contains "$WORK_DIR/account-unlinked.html" "Not linked"
assert_contains "$WORK_DIR/account-unlinked.html" "Third-party account unlinked."

UNBOUND_AGAIN_JAR="$WORK_DIR/unbound-again.cookies"
UNBOUND_AGAIN_FINAL="$(curl -sS -L -c "$UNBOUND_AGAIN_JAR" -b "$UNBOUND_AGAIN_JAR" \
	-o "$WORK_DIR/unbound-again.html" -w '%{url_effective}' "$BASE_URL/idp/login/mock")"
[[ "$UNBOUND_AGAIN_FINAL" == *"error=unbound"* ]] || fail "unlinked identity still logged in: $UNBOUND_AGAIN_FINAL"

printf '==> Testing Headplane OIDC login\n'
ADMIN_JAR="$WORK_DIR/admin.cookies"
curl -fsS -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o "$WORK_DIR/admin-login.html" "$BASE_URL/idp/login"
ADMIN_CSRF="$(csrf_token "$WORK_DIR/admin-login.html")"
ADMIN_LOGIN_STATUS="$(curl -sS -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o /dev/null -w '%{http_code}' \
	--data-urlencode "csrf_token=${ADMIN_CSRF}" \
	--data-urlencode "email=test@example.com" \
	--data-urlencode "password=correct-horse-battery" \
	--data-urlencode "return_to=/idp/account" \
	"$BASE_URL/idp/login")"
assert_equals "$ADMIN_LOGIN_STATUS" "303" "Headplane pre-login"
ADMIN_RESULT="$(curl -sS -L -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o "$WORK_DIR/admin-machines.html" \
	-w '%{http_code}\n%{url_effective}' "$BASE_URL/admin/oidc/start")"
ADMIN_STATUS="$(printf '%s\n' "$ADMIN_RESULT" | sed -n '1p')"
ADMIN_FINAL="$(printf '%s\n' "$ADMIN_RESULT" | sed -n '2p')"
assert_equals "$ADMIN_STATUS" "200" "Headplane OIDC final response"
assert_equals "$ADMIN_FINAL" "$BASE_URL/admin/machines" "Headplane OIDC final URL"
assert_contains "$WORK_DIR/admin-machines.html" "<title>Headplane"
assert_contains "$WORK_DIR/admin-machines.html" "Machines"
assert_matches "$WORK_DIR/admin-machines.html" '<html[^>]*lang="en"'
assert_contains "$WORK_DIR/admin-machines.html" "Access controls"

printf '==> Testing Headplane language switching\n'
curl -fsS -L -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o "$WORK_DIR/admin-machines-zh.html" \
	"$BASE_URL/idp/language?lang=zh-CN&return_to=%2Fadmin%2Fmachines"
assert_matches "$WORK_DIR/admin-machines-zh.html" '<html[^>]*lang="zh-CN"'
assert_contains "$WORK_DIR/admin-machines-zh.html" "设备"
assert_contains "$WORK_DIR/admin-machines-zh.html" "访问控制"
assert_contains "$WORK_DIR/admin-machines-zh.html" "搜索设备"
curl -fsS -L -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o "$WORK_DIR/admin-machines-en.html" \
	"$BASE_URL/idp/language?lang=en&return_to=%2Fadmin%2Fmachines"
assert_matches "$WORK_DIR/admin-machines-en.html" '<html[^>]*lang="en"'
assert_contains "$WORK_DIR/admin-machines-en.html" "Machines"

printf '==> Testing a second user can register and sign in\n'
SECOND_JAR="$WORK_DIR/second-user.cookies"
curl -fsS -c "$SECOND_JAR" -b "$SECOND_JAR" -o "$WORK_DIR/second-register.html" "$BASE_URL/idp/register"
SECOND_CSRF="$(csrf_token "$WORK_DIR/second-register.html")"
SECOND_REGISTER_STATUS="$(curl -sS -c "$SECOND_JAR" -b "$SECOND_JAR" -o /dev/null -w '%{http_code}' \
	--data-urlencode "csrf_token=${SECOND_CSRF}" \
	--data-urlencode "display_name=第二用户" \
	--data-urlencode "email=second@example.com" \
	--data-urlencode "password=correct-horse-battery" \
	--data-urlencode "password_confirm=correct-horse-battery" \
	--data-urlencode "return_to=" \
	"$BASE_URL/idp/register")"
assert_equals "$SECOND_REGISTER_STATUS" "303" "second user registration"
curl -fsS -c "$SECOND_JAR" -b "$SECOND_JAR" -o "$WORK_DIR/second-account.html" "$BASE_URL/idp/account"
assert_contains "$WORK_DIR/second-account.html" "second@example.com"
SECOND_ADMIN_RESULT="$(curl -sS -L -c "$SECOND_JAR" -b "$SECOND_JAR" -o "$WORK_DIR/second-admin.html" \
	-w '%{http_code}\n%{url_effective}' "$BASE_URL/admin/oidc/start")"
SECOND_ADMIN_STATUS="$(printf '%s\n' "$SECOND_ADMIN_RESULT" | sed -n '1p')"
SECOND_ADMIN_FINAL="$(printf '%s\n' "$SECOND_ADMIN_RESULT" | sed -n '2p')"
assert_equals "$SECOND_ADMIN_STATUS" "200" "second user Headplane OIDC response"
assert_equals "$SECOND_ADMIN_FINAL" "$BASE_URL/admin/" "second user Headplane landing page"
assert_contains "$WORK_DIR/second-admin.html" "second@example.com"
assert_contains "$WORK_DIR/second-admin.html" "Connect your devices"

printf '\nDocker smoke test passed.\n'
printf '  Headscale: %s\n' "$BASE_URL"
printf '  Headplane: %s/admin/\n' "$BASE_URL"
printf '  Accounts:  %s/idp/\n' "$BASE_URL"
