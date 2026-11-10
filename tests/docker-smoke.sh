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
NODE_CONTAINERS=()
TAILSCALE_IMAGE="${TAILSCALE_IMAGE:-tailscale/tailscale:v1.104.1}"

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
	for node in "${NODE_CONTAINERS[@]}"; do docker rm -f "$node" >/dev/null 2>&1 || true; done
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
assert_equals "$SECOND_ADMIN_FINAL" "$BASE_URL/admin/machines" "second user's independent network"
assert_contains "$WORK_DIR/second-admin.html" "Machines"
assert_contains "$WORK_DIR/second-admin.html" "Access controls"

printf '==> Testing independent networks with real Tailscale devices\n'
FIRST_ID="$(grep -oE 'usr_[0-9a-f]{24}' "$WORK_DIR/registered-account.html" | head -n 1)"
SECOND_ID="$(grep -oE 'usr_[0-9a-f]{24}' "$WORK_DIR/second-account.html" | head -n 1)"
[[ -n "$FIRST_ID" && -n "$SECOND_ID" && "$FIRST_ID" != "$SECOND_ID" ]] || fail "account identities are not distinct"
FIRST_KEY="$(docker exec "$CONTAINER_NAME" cat "/data/tailnets/$FIRST_ID/headscale/console-api-key")"
SECOND_KEY="$(docker exec "$CONTAINER_NAME" cat "/data/tailnets/$SECOND_ID/headscale/console-api-key")"
FIRST_USER="$(curl -fsS -H "Authorization: Bearer $FIRST_KEY" "$BASE_URL/api/v1/user" | jq -r '.users[0].id')"
SECOND_USER="$(curl -fsS -H "Authorization: Bearer $SECOND_KEY" "$BASE_URL/api/v1/user" | jq -r '.users[0].id')"
[[ "$FIRST_USER" != "$SECOND_USER" ]] || fail "client account profile IDs collide on the shared control URL"
FIRST_AUTH="$(curl -fsS -H "Authorization: Bearer $FIRST_KEY" -H 'Content-Type: application/json' \
  --data "{\"user\":\"$FIRST_USER\",\"reusable\":true}" "$BASE_URL/api/v1/preauthkey" | jq -r '.preAuthKey.key')"
SECOND_AUTH="$(curl -fsS -H "Authorization: Bearer $SECOND_KEY" -H 'Content-Type: application/json' \
  --data "{\"user\":\"$SECOND_USER\",\"reusable\":true}" "$BASE_URL/api/v1/preauthkey" | jq -r '.preAuthKey.key')"
start_node() {
  local label="$1" key="$2" name="$CONTAINER_NAME-$1"
  NODE_CONTAINERS+=("$name")
  docker run -d --name "$name" --network host \
    -e TS_USERSPACE=true -e TS_STATE_DIR=/tmp/tailscale \
    -e "TS_AUTHKEY=$key" -e "TS_EXTRA_ARGS=--login-server=$BASE_URL --hostname=$label --accept-dns=false" \
    -e 'TS_TAILSCALED_EXTRA_ARGS=--port=0' "$TAILSCALE_IMAGE" >/dev/null
}
start_node alice-private-one ""
printf '==> Testing device authorization through the shared account login\n'
AUTH_URL=""
for ((i=0; i<120; i++)); do
  AUTH_URL="$(docker logs "$CONTAINER_NAME-alice-private-one" 2>&1 | grep -oE "${BASE_URL}/register/[A-Za-z0-9_-]+" | head -n 1 || true)"
  [[ -n "$AUTH_URL" ]] && break
  sleep 0.5
done
[[ -n "$AUTH_URL" ]] || fail "interactive device did not request browser authorization"
curl -fsS -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o /dev/null "$BASE_URL/idp/language?lang=zh-CN&return_to=%2Fidp%2Faccount"
CONFIRM_URL="$(curl -fsS -L -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o "$WORK_DIR/device-confirm.html" -w '%{url_effective}' "$AUTH_URL")"
assert_contains "$WORK_DIR/device-confirm.html" "确认设备注册"
assert_contains "$WORK_DIR/device-confirm.html" 'lang="zh-CN"'
DEVICE_CSRF="$(sed -n 's/.*name="headscale_register_confirm"[^>]*value="\([^"]*\)".*/\1/p' "$WORK_DIR/device-confirm.html" | head -n 1)"
[[ -n "$DEVICE_CSRF" ]] || fail "device confirmation token not found"
curl -fsS -c "$ADMIN_JAR" -b "$ADMIN_JAR" --data-urlencode "headscale_register_confirm=$DEVICE_CSRF" \
  -o "$WORK_DIR/device-confirmed.html" "$CONFIRM_URL"
curl -fsS -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o /dev/null "$BASE_URL/idp/language?lang=en&return_to=%2Fidp%2Faccount"
start_node alice-private-two "$FIRST_AUTH"
start_node bob-private-one "$SECOND_AUTH"
for ((i=0; i<120; i++)); do
  curl -fsS -H "Authorization: Bearer $FIRST_KEY" "$BASE_URL/api/v1/node" > "$WORK_DIR/alice-nodes.json"
  curl -fsS -H "Authorization: Bearer $SECOND_KEY" "$BASE_URL/api/v1/node" > "$WORK_DIR/bob-nodes.json"
  if jq -e '.nodes | length == 2' "$WORK_DIR/alice-nodes.json" >/dev/null && jq -e '.nodes | length == 1' "$WORK_DIR/bob-nodes.json" >/dev/null; then break; fi
  sleep 0.5
done
jq -e '.nodes | length == 2' "$WORK_DIR/alice-nodes.json" >/dev/null || fail "Alice's devices did not join her network"
jq -e '.nodes | length == 1' "$WORK_DIR/bob-nodes.json" >/dev/null || fail "Bob's device did not join his network"
for ((i=0; i<60; i++)); do
  docker exec "$CONTAINER_NAME-alice-private-one" tailscale status --json > "$WORK_DIR/alice-status.json"
  docker exec "$CONTAINER_NAME-bob-private-one" tailscale status --json > "$WORK_DIR/bob-status.json"
  if jq -e '[.Peer[]?.HostName] | index("alice-private-two") != null' "$WORK_DIR/alice-status.json" >/dev/null && \
    jq -e '.BackendState == "Running" and (.CurrentTailnet.MagicDNSSuffix | type == "string")' "$WORK_DIR/bob-status.json" >/dev/null; then break; fi
  sleep 0.5
done
jq -e '[.Peer[]?.HostName] | index("alice-private-two") != null and index("bob-private-one") == null' "$WORK_DIR/alice-status.json" >/dev/null || fail "Alice's peer map crosses network boundaries"
jq -e '(.Peer // {}) | length == 0' "$WORK_DIR/bob-status.json" >/dev/null || fail "Bob can see Alice's devices"
FIRST_DOMAIN="$(jq -r '.CurrentTailnet.MagicDNSSuffix' "$WORK_DIR/alice-status.json")"
SECOND_DOMAIN="$(jq -r '.CurrentTailnet.MagicDNSSuffix' "$WORK_DIR/bob-status.json")"
[[ "$FIRST_DOMAIN" != "$SECOND_DOMAIN" && "$FIRST_DOMAIN" != "null" && "$SECOND_DOMAIN" != "null" ]] || fail "MagicDNS domains are shared"

printf '==> Testing restart recovery for both private networks\n'
docker restart "$CONTAINER_NAME" >/dev/null
for ((i=0; i<120; i++)); do
  if curl -fsS -H "Authorization: Bearer $FIRST_KEY" "$BASE_URL/api/v1/node" > "$WORK_DIR/alice-restored.json" 2>/dev/null && \
    curl -fsS -H "Authorization: Bearer $SECOND_KEY" "$BASE_URL/api/v1/node" > "$WORK_DIR/bob-restored.json" 2>/dev/null && \
    jq -e '.nodes | length == 2' "$WORK_DIR/alice-restored.json" >/dev/null && \
    jq -e '.nodes | length == 1' "$WORK_DIR/bob-restored.json" >/dev/null; then break; fi
  sleep 0.5
done
jq -e '.nodes | length == 2' "$WORK_DIR/alice-restored.json" >/dev/null || fail "restart lost Alice's devices"
jq -e '.nodes | length == 1' "$WORK_DIR/bob-restored.json" >/dev/null || fail "restart lost Bob's devices"
jq -S '[.nodes[] | {id, nodeKey, user: .user.id}] | sort_by(.id)' "$WORK_DIR/alice-nodes.json" > "$WORK_DIR/alice-identities-before.json"
jq -S '[.nodes[] | {id, nodeKey, user: .user.id}] | sort_by(.id)' "$WORK_DIR/alice-restored.json" > "$WORK_DIR/alice-identities-after.json"
cmp "$WORK_DIR/alice-identities-before.json" "$WORK_DIR/alice-identities-after.json" || fail "restart changed public node or user identities"
wait_for_url "$BASE_URL/admin/healthz" || fail "Headplane did not become ready after restart"
curl -fsS -L -c "$SECOND_JAR" -b "$SECOND_JAR" -o "$WORK_DIR/bob-console-restored.html" "$BASE_URL/admin/machines"
assert_contains "$WORK_DIR/bob-console-restored.html" "bob-private-one"

printf '==> Testing forged network headers and independent policies\n'
curl -fsS -L -b "$ADMIN_JAR" -H "X-HSC-Tailnet: $SECOND_ID" -H 'X-HSC-Proxy-Secret: forged' \
  -o "$WORK_DIR/forged-network.html" "$BASE_URL/admin/machines"
assert_contains "$WORK_DIR/forged-network.html" "alice-private-one"
if grep -Fq 'bob-private-one' "$WORK_DIR/forged-network.html"; then fail "browser headers can select another account's network"; fi
printf '==> Testing that a network reload preserves the other network\n'
curl -fsS -X POST -H "Authorization: Bearer $FIRST_KEY" "$BASE_URL/api/console/tailnet/reload" >/dev/null
curl -fsS -H "Authorization: Bearer $SECOND_KEY" "$BASE_URL/api/v1/node" | jq -e '.nodes | length == 1' >/dev/null || fail "reloading Alice disrupted Bob"
curl -fsS -H "Authorization: Bearer $FIRST_KEY" "$BASE_URL/api/v1/node" | jq -e '.nodes | length == 2' >/dev/null || fail "reloading Alice lost her devices"
curl -fsS -H "Authorization: Bearer $SECOND_KEY" "$BASE_URL/api/v1/policy" > "$WORK_DIR/bob-policy-before.json"
curl -fsS -X PUT -H "Authorization: Bearer $FIRST_KEY" -H 'Content-Type: application/json' \
  --data '{"policy":"{\"acls\":[]}"}' "$BASE_URL/api/v1/policy" >/dev/null
curl -fsS -H "Authorization: Bearer $SECOND_KEY" "$BASE_URL/api/v1/policy" > "$WORK_DIR/bob-policy-after.json"
cmp "$WORK_DIR/bob-policy-before.json" "$WORK_DIR/bob-policy-after.json" || fail "one network changed another network's policy"

printf '==> Testing administrator account deletion and access revocation\n'
assert_equals "$(curl -sS -b "$SECOND_JAR" -o /dev/null -w '%{http_code}' "$BASE_URL/idp/users")" "403" "personal owner is not a platform administrator"
curl -fsS -b "$ADMIN_JAR" -o "$WORK_DIR/account-management.html" "$BASE_URL/idp/users?lang=zh-CN"
assert_contains "$WORK_DIR/account-management.html" "删除账号"
curl -fsS -c "$ADMIN_JAR" -b "$ADMIN_JAR" -o "$WORK_DIR/delete-confirm.html" "$BASE_URL/idp/users/$SECOND_ID/delete"
DELETE_CSRF="$(csrf_token "$WORK_DIR/delete-confirm.html")"
assert_equals "$(curl -sS -b "$ADMIN_JAR" -o /dev/null -w '%{http_code}' \
  --data-urlencode "csrf_token=$DELETE_CSRF" --data-urlencode 'confirm_email=second@example.com' \
  "$BASE_URL/idp/users/$SECOND_ID/delete")" "303" "delete registered account and network"
assert_equals "$(curl -sS -b "$SECOND_JAR" -o /dev/null -w '%{http_code}' "$BASE_URL/admin/machines")" "303" "deleted account's old Headplane cookie is rejected"
REVOKED_STATUS="$(curl -sS -H "Authorization: Bearer $SECOND_KEY" -o /dev/null -w '%{http_code}' "$BASE_URL/api/v1/node")"
[[ "$REVOKED_STATUS" == 401 || "$REVOKED_STATUS" == 403 ]] || fail "deleted network API key still works: $REVOKED_STATUS"
docker exec "$CONTAINER_NAME" test ! -e "/data/tailnets/$SECOND_ID/headscale/db.sqlite" || fail "deleted network database remains"
docker exec "$CONTAINER_NAME" test ! -e "/data/tailnets/$SECOND_ID/headplane/data/hp_persist.db" || fail "deleted network sessions remain"
curl -fsS -H "Authorization: Bearer $FIRST_KEY" "$BASE_URL/api/v1/node" | jq -e '.nodes | length == 2' >/dev/null || fail "deleting Bob changed Alice's devices"

printf '\nDocker smoke test passed.\n'
printf '  Headscale: %s\n' "$BASE_URL"
printf '  Headplane: %s/admin/\n' "$BASE_URL"
printf '  Accounts:  %s/idp/\n' "$BASE_URL"
