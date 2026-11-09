# Headscale Console

把 Headplane、Headscale 和一个内置 OIDC 账号中心打包进同一个 Linux 二进制。对外只启动一个 HTTP 端口，同时提供账号注册登录、Headplane 管理后台和 Headscale 控制面。

## 功能

- 邮箱/密码注册、登录、退出和修改密码。
- 登录后绑定/解绑第三方账号，支持 GitHub OAuth 和通用 OIDC。
- 账号中心和 Headplane 支持中文 / English 切换，语言选择由共享 Cookie 保存。
- 第三方账号不能凭相同邮箱自动合并；必须先登录本地账号再主动绑定，避免账号接管。
- Headplane 和 Headscale 共用内置 OIDC 服务，不需要另外部署 Keycloak、Authelia 等身份提供方。
- Headplane 和 Headscale 作为受监管子进程运行，异常退出后自动重启，主进程退出时优雅关闭。
- 首次启动自动创建 Headscale API Key，并初始化显式 allow-all ACL，避免 Headplane 的 ACL 页面因 Headscale 0.30 的空策略返回 500。
- 数据、密钥、子进程配置默认都保存在一个数据目录下，便于备份和迁移。

## 架构

```text
Browser
   │  http://host:8080
   ▼
┌────────────────────────────────────────────┐
│ Headscale Console (single binary)         │
│                                            │
│  /idp/*    → 内置 OIDC 账号中心 (SQLite)   │
│  /admin/*  → Headplane (embedded Node.js)  │
│  /*        → Headscale (embedded binary)   │
└────────────────────────────────────────────┘
```

同一个二进制内包含：

- Headplane 服务端构建产物和数据库迁移文件。
- Linux x86-64 Headscale 二进制。
- Node.js 24 运行时。
- Go 编写的 OIDC、账号、SQLite、反向代理和进程监管逻辑。

## 构建

### 依赖

- Go 1.26.4 或更高版本。
- Docker，用于从 `node:24-slim` 提取 Node 运行时；使用 `--rebuild-headplane` 时也用于构建前端。
- 同级目录中的 Headplane 和 Headscale 源码：

```text
workspace/
├── headplane/
├── headscale/
└── headscale-console/
```

默认读取：

- `../headplane/build`：已经构建好的 Headplane。
- `../headplane/drizzle`：Headplane 数据库迁移。
- `../headscale`：Headscale Go 源码。

如果 Headplane 还没有构建，可在 Headplane 源码目录执行它自己的构建命令，或者直接重建：

```bash
./build.sh --rebuild-headplane
```

正常构建：

```bash
./build.sh
```

输出为 `dist/headscale-console`。也可以通过环境变量覆盖路径：

```bash
HEADPLANE_SOURCE=/path/to/headplane \
HEADSCALE_SOURCE=/path/to/headscale \
OUTPUT=dist/headscale-console \
./build.sh
```

默认只生成 `linux/amd64` 二进制。其他架构需同时设置 `GOARCH`，并确保 Docker/节点运行时与目标架构一致。

生成发布文件（要求工作区干净，并会重新构建 Headplane）：

```bash
./scripts/release.sh 0.1.2
```

## 发布包与安装

GitHub Releases 提供：

- `headscale-console-linux-amd64`：单二进制程序。
- `headscale-console-linux-amd64.tar.gz`：包含程序、示例配置和 Nginx 配置。
- `xuanyu-linux-amd64`：独立 systemd 安装/卸载工具。
- `SHA256SUMS`：发布文件校验值。

使用独立部署工具安装：

```bash
sudo ./xuanyu-linux-amd64 install \
  -binary ./headscale-console-linux-amd64 \
  -domain hs.example.com \
  -base-domain tailnet.example.com \
  -allowed-email-domains example.com \
  -no-start
sudo ./xuanyu-linux-amd64 start
```

Nginx 是独立组件，不由本项目和 `xuanyu` 安装或重载。复制 `deploy/nginx/headscale-console.conf`，按 [docs/nginx.md](docs/nginx.md) 替换域名、证书路径和上游地址，然后自行执行 `nginx -t` 与重载。

默认安装位置：

```text
/opt/headscale-console/headscale-console
/etc/headscale-console/config.yaml
/var/lib/headscale-console
/etc/systemd/system/headscale-console.service
```

卸载但保留数据：`sudo ./xuanyu-linux-amd64 uninstall`。彻底删除配置和数据：`sudo ./xuanyu-linux-amd64 uninstall -purge`。

## 启动

```bash
./dist/headscale-console --data-dir ./data
```

首次启动会自动创建 `./data/config.yaml`。生产环境应先修改 `server.base_url`，再重新启动：

```yaml
server:
  listen: 0.0.0.0:8080
  base_url: https://console.example.com
  admin_path: /admin

registration:
  enabled: true
  allowed_email_domains:
    - example.com
  password_min_length: 12

headplane:
  default_role: member

headscale:
  base_domain: tailnet.internal
  derp_urls:
    - https://controlplane.tailscale.com/derpmap/default
  dns_nameservers:
    - 1.1.1.1
    - 1.0.0.1
```

常用参数：

```bash
./dist/headscale-console --help
./dist/headscale-console --data-dir /var/lib/headscale-console --config /etc/headscale-console.yaml
./dist/headscale-console --version
```

### 界面语言

账号中心右上角和 Headplane 顶部导航都提供语言切换。切换入口是 `/idp/language`，会把所选语言写入 `hsc_locale` Cookie（`Path=/`，有效期一年），因此账号中心、`/admin/*` 和 OIDC 登录流程使用同一语言。

语言选择优先级为：请求中的 `?lang=`、`hsc_locale` Cookie、浏览器 `Accept-Language`、英文回退。页面由服务端按当前语言渲染，首屏不会出现先英文后中文的闪烁；`<html lang>` 也会同步更新。

当前账号中心、Headplane 登录/退出、公共导航、设备、用户、DNS、设置入口和访问控制主页面已支持中英文。少数高级编辑对话框和底层 API 错误详情仍可能显示英文。

也可以使用环境变量：

- `HEADSCALE_CONSOLE_DATA_DIR`
- `HEADSCALE_CONSOLE_CONFIG`
- `HEADSCALE_CONSOLE_LOG_LEVEL=debug`

启动完成后：

- 账号中心：`<base_url>/idp/`
- Headplane：`<base_url>/admin/`
- Headscale：`<base_url>/`

`server.base_url` 必须是不带路径的绝对 `http://` 或 `https://` URL。Headplane、Headscale 和内置 OIDC 的 issuer 都使用该地址，运行期间不要随意修改。

## 账号与第三方登录

### 注册和登录

注册入口为 `<base_url>/idp/register`。注册成功后自动登录，并可在账号页修改密码、绑定第三方账号。

关闭注册：

```yaml
registration:
  enabled: false
```

限制邮箱域名：

```yaml
registration:
  enabled: true
  allowed_email_domains:
    - example.com
    - team.example.net
```

域名限制只过滤注册表单，不等同于邮箱所有权验证。

### GitHub OAuth

在 GitHub OAuth App 中配置回调地址：

```text
<base_url>/idp/oauth/github/callback
```

配置示例：

```yaml
oauth_providers:
  - name: github
    type: github
    client_id: YOUR_GITHUB_CLIENT_ID
    client_secret: YOUR_GITHUB_CLIENT_SECRET
```

GitHub 提供方固定使用 `read:user user:email` 权限，并优先读取主邮箱中已验证的地址。邮箱只作为绑定信息展示，不会自动创建或合并本地账号。

### 通用 OIDC

第三方 OIDC 提供方需要在它的客户端配置中登记回调地址：

```text
<base_url>/idp/oauth/<provider-name>/callback
```

例如 `name: pocket-id` 对应：

```text
<base_url>/idp/oauth/pocket-id/callback
```

配置示例：

```yaml
oauth_providers:
  - name: pocket-id
    type: oidc
    issuer: https://id.example.com
    client_id: YOUR_CLIENT_ID
    client_secret: YOUR_CLIENT_SECRET
    scopes:
      - openid
      - profile
      - email
```

第三方 OIDC 使用授权码 + PKCE（S256），并校验 ID Token 的签名、issuer、audience、nonce 和有效期。未显式配置 `scopes` 时使用 `openid profile email`。

第三方登录规则：

1. 未绑定的第三方身份登录会返回“尚未绑定”，不会自动创建本地账号。
2. 先用邮箱/密码登录，再进入账号页点击“绑定”。
3. 绑定后可以直接使用第三方账号登录。
4. 解绑后，该第三方身份立即不能登录。

### Headplane 多用户角色

每个本地账号使用自己的稳定用户 ID 作为 OIDC `sub`，因此 Headplane 会为不同账号创建独立用户。Headplane 自身的行为是：

- 第一个成功登录 Headplane 的账号自动成为 owner。
- 后续账号首次创建时使用 `headplane.default_role`，默认是 `member`。member 能看到连接设备首页，但不能进入管理页面。
- owner、admin 或 IT admin 可以在 Headplane 的 Users 页面为成员分配 `viewer`、`auditor`、`admin` 等角色。

如果希望所有新账号默认只读，可设置：

```yaml
headplane:
  default_role: viewer
```

可选值：`admin`、`network_admin`、`it_admin`、`auditor`、`viewer`、`member`。`owner` 不能作为默认角色。

**首次登录决定 owner。** 公网开放注册时，应先由管理员注册并登录 Headplane，再开放给其他用户，或者在生产环境关闭注册。

### 内置 OIDC 回调

内置 OIDC 服务供 Headplane 和 Headscale 共用，客户端 ID 为 `headscale-console`。以下地址由程序内部处理，不需要用户配置：

- Headplane：`<base_url>/admin/oidc/callback`
- Headscale：`<base_url>/oidc/callback`
- Issuer / Discovery：`<base_url>/idp` 和 `<base_url>/idp/.well-known/openid-configuration`

修改 `admin_path` 时，Headplane 回调地址也会随之变化。

## Docker

先构建二进制，再构建运行时镜像：

```bash
./build.sh
docker build -t headscale-console:local .
```

运行：

```bash
docker volume create headscale-console-data
docker run -d \
  --name headscale-console \
  -p 8080:8080 \
  -v headscale-console-data:/data \
  headscale-console:local
```

也可以使用 `make docker-image` 构建镜像。镜像是 Debian bookworm，运行时用户 UID/GID 为 `10001`。使用宿主机目录挂载时，需要保证该目录对 `10001:10001` 可写：

```bash
mkdir -p ./data
sudo chown -R 10001:10001 ./data
docker run -d \
  --name headscale-console \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  headscale-console:local
```

首次运行后编辑 `data/config.yaml` 中的 `server.base_url`，然后重启容器。

### Docker 冒烟测试

```bash
make build
make docker-test
```

测试脚本为 `tests/docker-smoke.sh`，会：

- 构建运行镜像，并只把单个应用二进制放进镜像。
- 用宿主机网络启动 mock OIDC、容器和最终二进制。
- 验证 Headscale `/health`、`/version` 和 ACL API。
- 验证注册、密码登录、第三方未绑定拦截、绑定、OAuth 登录和解绑。
- 验证中英文页面、语言 Cookie 持久化，以及 Headplane 导航的语言切换。
- 走完 Headplane OIDC 登录，确认最终到达 `/admin/machines`。
- 注册第二个账号并登录 Headplane，确认多用户登录到 member 首页。

脚本使用 `127.0.0.1` 和以下端口：`18070`、`18071`、`18080`、`18081`、`18082`、`18083`、`18090`。可通过同名环境变量覆盖端口；Linux 宿主机网络是必需条件。设置 `KEEP_SMOKE_DATA=1` 可保留临时数据。

## TLS 与反向代理

程序可以直接加载证书：

```yaml
server:
  base_url: https://console.example.com
  tls_cert_file: /data/tls/fullchain.pem
  tls_key_file: /data/tls/privkey.pem
```

也可以由反向代理终止 TLS，再把所有路径转发到 `server.listen`。无论哪种方式，都必须满足：

- 外部地址与 `server.base_url` 完全一致。
- 程序所在主机/容器能通过该地址访问自己的 `/idp` 路径。
- 使用 HTTPS 时，系统信任链能验证证书。

内置 Headplane 和 Headscale 在启动时会访问 `<base_url>/idp` 做 OIDC discovery，因此不能用程序不信任的自签名证书，也不能使用仅在浏览器侧可解析的地址。

启用 HTTPS 时 Cookie 默认带 `Secure`；如确需覆盖，可设置 `server.secure_cookies`。

完整的域名规划、DNS、TLS 证书、Nginx 配置和排错说明见 [docs/nginx.md](docs/nginx.md)，示例文件为 [deploy/nginx/headscale-console.conf](deploy/nginx/headscale-console.conf)。Nginx 由你独立安装和管理，项目不会修改或重载 Nginx。

## 数据目录

默认数据目录在 root 用户下为 `/var/lib/headscale-console`，普通用户下为 `$XDG_CONFIG_HOME/headscale-console`。主要文件：

```text
data/
├── config.yaml
├── secrets/                  # OIDC client secret、Cookie 密钥、Headscale API Key
├── account/accounts.sqlite   # 本地账号、会话、第三方身份绑定
├── generated/                # 自动生成的 Headplane / Headscale 配置
├── headplane/data/           # Headplane 数据
├── headscale/                # Headscale SQLite、Noise key、socket、DNS records
└── runtime/                  # 从二进制解包的 Node/Headplane/Headscale 运行时
```

备份时应整体备份数据目录。不要只备份 SQLite 而遗漏 `secrets/`，否则 OIDC client secret 和 API Key 会变化。

示例配置见 [config.example.yaml](config.example.yaml)。

## 开发与测试

```bash
gofmt -l $(find . -name '*.go' -not -path './.cache/*')
go test ./...
go vet ./...
```

单独运行 Docker 测试：

```bash
./tests/docker-smoke.sh dist/headscale-console
```

## 安全边界

当前实现适合自托管、可信用户或受控邀请场景，但上线前必须了解：

- 没有邮箱验证、找回密码、MFA、登录限流、失败锁定和审计日志。
- 本地账号的 `email_verified` 在 OIDC Token 中始终为 `true`，这是“账号中心已接受该邮箱”的声明，不代表已经发送验证邮件。
- 注册默认开启。公网部署至少应关闭注册、限制邮箱域名，或在前置代理增加访问控制。
- 第一个登录 Headplane 的账号会成为 owner。开放注册前必须由可信管理员先完成首次登录。
- 邮箱域名限制不能防止冒用域名邮箱，不能替代邮箱验证。
- 第三方账号绑定不会按邮箱自动合并，但仍建议启用提供方的邮箱验证能力。
- SQLite 和本地文件存储只适合单实例部署，不支持多副本共享同一数据目录。
- Node 运行时是动态链接程序，当前镜像依赖 glibc、libstdc++ 和系统 CA；Alpine/musl 镜像不能直接使用。
- 默认 Headscale 配置使用公共 DERP 和 `tailnet.internal` MagicDNS 域名。生产部署需要按实际网络修改 `base_domain`、`derp_urls`、DNS 和域名解析。
- 程序首次写入的是显式 allow-all ACL，以保持 Headscale 原本的默认连通行为。生产环境应在 Headplane 中按需收紧策略。

## 项目结构

```text
cmd/headscale-console/      启动编排和公共 HTTP 服务
internal/idp/               内置 OIDC、注册登录、第三方绑定、SQLite
internal/headscale/         Headscale 配置、API Key、默认策略
internal/headplane/         Headplane 配置生成
internal/runtime/           内嵌运行时解包
internal/supervisor/        子进程启动、重启和关闭
internal/proxy/             单端口路径反向代理
internal/secret/            持久化随机密钥
tests/docker-smoke.sh       Docker 端到端冒烟测试
deploy/nginx/               Nginx 反向代理示例
docs/nginx.md               Nginx、证书与部署排错说明
```
