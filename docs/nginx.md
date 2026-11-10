# Nginx 反向代理与域名配置

Headscale Console 在一个端口内同时提供 Headscale 控制面、Headplane 管理后台和内置 OIDC 账号中心。生产环境推荐这样部署：

```text
浏览器 / Tailscale 客户端
          │
          │ HTTPS 443
          ▼
       Nginx（独立管理）
          │
          │ HTTP 127.0.0.1:8080
          ▼
  Headscale Console 单二进制
```

> Nginx 是独立组件。本项目不会安装、修改或重载 Nginx。下面的配置由你自己保存、替换占位符、测试并上线。

## 1. 域名规划

本文示例使用两个不同的域名：

| 用途 | 示例 | 说明 |
| --- | --- | --- |
| Console 域名 | `hs.example.com` | 浏览器、Headplane、Headscale 客户端统一访问的 HTTPS 域名，必须配置 DNS 和 TLS 证书。 |
| MagicDNS 后缀 | `tailnet.example.com` | Headscale 分配给 Tailscale 客户端的内网域名后缀，不是 Console 的访问地址。 |

实际使用时把 `example.com` 和 `hs` 换成自己的域名。不要直接把示例域名原样上线。

Console 域名必须满足：

- 使用 `https://`，不要在 `server.base_url` 中带路径、查询参数或结尾斜杠。
- 例如 `https://hs.example.com`，不能写成 `https://hs.example.com/admin`。
- 证书的 SAN 必须包含 `hs.example.com`。
- 该域名在服务器本机、Nginx 和应用中必须表示同一个地址。

MagicDNS 后缀可以是另一个域名，例如 `tailnet.example.com`。它通常不需要公网 A/AAAA 记录，也不需要使用 Console 的 TLS 证书。

## 2. DNS 配置

为 Console 域名添加 A 和/或 AAAA 记录。假设公网 IPv4 是 `203.0.113.10`，IPv6 是 `2001:db8::10`：

```dns
hs.example.com.  300  IN  A     203.0.113.10
hs.example.com.  300  IN  AAAA  2001:db8::10
```

没有 IPv6 时可以只配置 A 记录。使用子域名时也可以使用 CNAME，但不要给域名根（apex）随意添加与现有记录冲突的 CNAME。

配置完成后在服务器上验证：

```bash
dig +short A hs.example.com
dig +short AAAA hs.example.com
getent ahosts hs.example.com
```

`getent` 必须返回 Nginx 所在服务器的地址。应用启动时会通过 `server.base_url` 访问自己的 `/idp`，所以仅让浏览器能解析域名还不够，服务器本身也必须能解析并访问它。

### 内网 DNS 或 NAT 回环

如果域名只在内网使用，请使用内网 DNS，并确保内网 CA 或公共 CA 签发的证书能被服务器信任。

如果公网 DNS 可用，但服务器所在网络不支持 NAT 回环，可以在服务器上增加仅本机使用的主机记录：

```text
127.0.0.1 hs.example.com
```

这会改变服务器本机对该域名的解析。录入 `/etc/hosts` 前必须确认 Nginx 在本机 `443` 端口监听，并且证书仍然匹配 `hs.example.com`。不要在 Tailscale 客户端使用的 DNS 中设置这条记录。

### CDN/Cloudflare 注意事项

如果域名经过 CDN 或 Cloudflare 代理：

- 必须允许 WebSocket 和长连接。
- 控制连接可能长时间保持，代理层不能设置过短的读取超时。
- 自托管场景最简单的是使用 DNS-only（灰云），让连接直接进入自己的 Nginx。
- 如果使用橙云，请确认套餐和代理规则允许 Headplane WebSocket 以及 Tailscale 控制连接。

## 3. 端口与防火墙

推荐只让 Nginx 对外开放：

```text
80/tcp     HTTP，仅用于跳转 HTTPS 或 ACME 验证
443/tcp    HTTPS
```

Headscale Console 保持回环监听：

```yaml
server:
  listen: 127.0.0.1:8080
```

内置组件默认也只监听回环地址：

```text
127.0.0.1:18070   内置 OIDC
127.0.0.1:18090   Headplane
127.0.0.1:18081   Headscale HTTP
127.0.0.1:18082   Headscale gRPC
127.0.0.1:18083   Headscale metrics
```

不要把 `8080` 或这些内部端口直接暴露到公网。使用 UFW 时可以参考：

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
sudo ufw status
```

如果使用云厂商安全组，也需要只放行 `80/443`。

## 4. 应用配置

准备 `/etc/headscale-console/config.yaml`，至少确认以下字段：

```yaml
server:
  listen: 127.0.0.1:8080
  base_url: https://hs.example.com
  admin_path: /admin

headscale:
  base_domain: tailnet.example.com
```

其中：

- `server.listen` 是应用自己的监听地址，Nginx 会转发到这里。
- `server.base_url` 是用户实际访问的 HTTPS 地址，必须与 Nginx 的 `server_name` 和证书域名一致。
- `admin_path` 是 Headplane 的管理路径，默认 `/admin`，不要把它拼进 `base_url`。
- `headscale.base_domain` 是 Tailscale 客户端的 MagicDNS 后缀，与 Console 域名可以不同。

完整示例见仓库根目录的 `config.example.yaml`。

## 5. TLS 证书

### 使用 Certbot 和 Nginx 插件

Debian/Ubuntu：

```bash
sudo apt update
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d hs.example.com
```

Certbot 会自行修改 Nginx 配置并重载 Nginx。上线前仍应检查最终的 `server_name`、证书路径和代理配置。

### 使用 DNS Challenge

无法开放 80 端口或使用通配符证书时：

```bash
sudo certbot certonly \
  --manual \
  --preferred-challenges dns \
  -d hs.example.com
```

然后使用：

```nginx
ssl_certificate     /etc/letsencrypt/live/hs.example.com/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/hs.example.com/privkey.pem;
```

### 使用已有证书

把 `ssl_certificate` 指向包含完整中间证书链的 `fullchain.pem`，把 `ssl_certificate_key` 指向私钥。只配置叶证书而不提供中间证书，会导致应用访问自己的 `https://hs.example.com/idp` 时验证失败。

自签名证书必须被服务器系统 CA 信任库信任，否则内置 Headscale 和 Headplane 在 OIDC discovery 阶段会失败。

证书的 SAN 必须包含 Console 域名本身，而不是同服务器上的另一个域名。例如部署 `hs.odata.cc` 时，如果手头只有 `f.odata.cc` 的证书，应用会报：

```text
x509: certificate is valid for f.odata.cc, not hs.odata.cc
```

这种证书不能靠关闭校验解决。请为 `hs.odata.cc` 单独签发证书，并确认 `openssl` 输出中包含该名称：

```bash
openssl x509 -in /path/to/fullchain.pem -noout -subject -ext subjectAltName
```

### 实际域名示例：hs.odata.cc

把本文其他位置的 `hs.example.com` 替换为 `hs.odata.cc` 后，关键配置应当一致：

```text
DNS A/AAAA:  hs.odata.cc -> Nginx 所在服务器
证书路径:    /etc/letsencrypt/live/hs.odata.cc/fullchain.pem
私钥路径:    /etc/letsencrypt/live/hs.odata.cc/privkey.pem
base_url:    https://hs.odata.cc
server_name: hs.odata.cc
```

```nginx
server_name hs.odata.cc;
ssl_certificate     /etc/letsencrypt/live/hs.odata.cc/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/hs.odata.cc/privkey.pem;
```

```yaml
server:
  base_url: https://hs.odata.cc
```

## 6. Nginx 配置

下面是一份完整示例。请替换：

- `hs.example.com`：你的 Console 域名。
- `127.0.0.1:8080`：应用实际监听地址。
- 两个证书路径：你的证书和私钥。

保存为 `/etc/nginx/conf.d/headscale-console.conf`：

```nginx
map $http_upgrade $hsc_connection_upgrade {
    default upgrade;
    ''      close;
}

upstream headscale_console {
    server 127.0.0.1:8080;
}

server {
    listen 80;
    listen [::]:80;
    server_name hs.example.com;

    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name hs.example.com;
    server_tokens off;

    ssl_certificate     /etc/letsencrypt/live/hs.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/hs.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:headscale_console:10m;
    ssl_session_timeout 1d;

    client_max_body_size 32m;

    location / {
        proxy_pass http://headscale_console;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $hsc_connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Port $server_port;
        proxy_redirect off;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_connect_timeout 15s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

仓库中的 `deploy/nginx/headscale-console.conf` 与上面内容一致，可直接复制后修改占位符。

### 为什么需要这些参数

- `proxy_http_version 1.1`、`Upgrade` 和 `Connection`：Headplane 的实时页面需要 WebSocket。
- `proxy_buffering off`：Tailscale 控制连接和事件流需要及时转发，不能使用普通响应缓冲。
- `proxy_read_timeout 3600s`：控制连接可能长期保持，不能使用 Nginx 默认的短超时。
- `X-Forwarded-For`、`X-Forwarded-Proto`、`Host`：应用生成外部链接和安全 Cookie 时需要正确的请求信息。
- `client_max_body_size`：Headplane/ACL 上传内容较大时需要，默认 `1m` 可能返回 413。
- `proxy_pass http://headscale_console;` 后面不要添加 `/` 或路径，否则可能改写 `/idp`、`/admin` 等路径。

### HTTP/2 版本差异

兼容 Debian/Ubuntu 老版本 Nginx 的写法是：

```nginx
listen 443 ssl http2;
```

较新的 Nginx 可能提示 `listen ... http2` 已废弃。使用 Nginx 1.25.1 及以上版本时可以改为：

```nginx
listen 443 ssl;
listen [::]:443 ssl;
http2 on;
```

两种写法都能工作。不要因为出现废弃警告就删除 HTTPS 或跳过 `nginx -t`。

## 7. 检查并重载 Nginx

Nginx 由你独立管理，执行：

```bash
sudo nginx -t
sudo systemctl reload nginx
sudo systemctl status nginx --no-pager
```

如果使用容器或其他方式运行 Nginx，请使用对应方式执行配置测试和重载。

## 8. 启动应用并验证

如果使用 `xuanyu` 安装 systemd 服务：

```bash
sudo xuanyu install \
  -binary ./headscale-console-linux-amd64 \
  -domain hs.example.com \
  -base-domain tailnet.example.com \
  -no-start

sudo xuanyu start
```

也可以直接运行：

```bash
./headscale-console-linux-amd64 \
  --config /etc/headscale-console/config.yaml \
  --data-dir /var/lib/headscale-console
```

验证域名、TLS 和应用：

```bash
curl -fsS https://hs.example.com/health
curl -fsS https://hs.example.com/idp/healthz
curl -fsS https://hs.example.com/idp/.well-known/openid-configuration
```

浏览器访问：

- 账号中心：`https://hs.example.com/idp/`
- Headplane：`https://hs.example.com/admin/`
- Headscale 控制面：`https://hs.example.com/`

对应的 OIDC discovery 必须由 Nginx 正常转发，而不是返回 404：

```bash
curl -fsS https://hs.example.com/idp/.well-known/openid-configuration
```

返回的 JSON 中应同时包含 `authorization_endpoint`、`token_endpoint`、
`jwks_uri` 和 `end_session_endpoint`。Headplane 会用
`end_session_endpoint` 完成 OIDC 退出，退出后的回调固定为：

```text
https://hs.example.com/admin/login?s=logout
```

如果服务器本机把域名解析到 `127.0.0.1`，但 Nginx 实际监听在其他地址，可以用
`--resolve` 指定真实地址来测试：

```bash
curl -fsS --resolve hs.example.com:443:<Nginx-IP> \
  https://hs.example.com/idp/.well-known/openid-configuration
```

Nginx 必须把 `/`、`/admin`、`/idp`、`/oidc`、WebSocket 和退出回调都转发到
同一个 `headscale_console` upstream。不要只代理 `/admin`，否则 Tailscale
控制面、OIDC discovery 和内置账号中心会返回 404 或无法登录。

## 9. 第三方登录回调地址

如果配置了 GitHub OAuth 或通用 OIDC，提供方后台的回调地址必须使用同一个域名：

```text
GitHub:  https://hs.example.com/idp/oauth/github/callback
OIDC:    https://hs.example.com/idp/oauth/<provider-name>/callback
```

应用内部使用的回调地址还包括：

```text
Headplane: https://hs.example.com/admin/oidc/callback
Headscale: https://hs.example.com/oidc/callback
Issuer:    https://hs.example.com/idp
```

更换域名后，除了修改 DNS、证书、`base_url` 和 Nginx，还要同步修改第三方 OAuth/OIDC 平台中的回调地址，否则会报 `redirect_uri` 不匹配。

## 10. 更换域名

更换 Console 域名会让 OIDC issuer 和 Cookie 域发生变化，已有登录会话通常需要重新登录。建议按以下顺序操作：

1. 为新域名配置 DNS 和 TLS 证书。
2. 更新 Nginx 的 `server_name` 和证书路径，先执行 `nginx -t`。
3. 修改 `config.yaml` 的 `server.base_url`，不要带路径。
4. 更新 GitHub/OIDC 提供方的回调地址。
5. 重启 Headscale Console。
6. 验证 `/health`、`/idp/healthz` 和 Headplane 登录。

旧域名的 Nginx 配置可以保留为 301 跳转，但应用只会使用一个新的 `server.base_url`。不要把多个域名同时写入 `server.base_url`。

## 11. 常见问题

### 502 Bad Gateway

```bash
sudo systemctl status headscale-console --no-pager
sudo journalctl -u headscale-console -n 100 --no-pager
ss -lntp | grep 8080
curl -v http://127.0.0.1:8080/health
```

确认 Nginx 的 `proxy_pass` 和应用 `server.listen` 一致。如果应用尚未启动，Nginx 也会返回 502。

### 启动时 OIDC discovery 失败

常见原因：

- `server.base_url` 与实际 HTTPS 域名不一致。
- 服务器本机无法解析 Console 域名。
- NAT 回环不可用，或 `/etc/hosts` 没有正确配置。
- Nginx 没有监听 443。
- 证书缺少中间证书链、SAN 不包含当前域名，或系统不信任该 CA。
- 防火墙阻止了服务器访问自己的公网地址。

在服务器本机执行：

```bash
getent ahosts hs.example.com
curl -v https://hs.example.com/idp/healthz
curl -v https://hs.example.com/idp/.well-known/openid-configuration
```

如果只看到 `404 Not Found` 且响应头带 `Server: nginx`，说明请求已经到 Nginx，
但 Nginx 没有把 `/idp` 路径转发到应用。检查 `proxy_pass` 是否保留了原始路径。
如果出现 `certificate is valid for f.odata.cc, not hs.odata.cc`，请重新签发包含
`hs.odata.cc` 的证书，不要使用其他子域名的证书。

### 退出登录无效或又自动登录

应用生成的 Headplane 配置会启用 OIDC `end_session_endpoint`，并把退出回调设置为
`https://<Console 域名>/admin/login?s=logout`。检查：

1. Headscale 配置的 OIDC issuer 是 `https://hs.example.com/idp`，不是其他域名。
2. 浏览器退出后确实请求了 `/idp/end-session`，并且最终回到 `?s=logout` 页面。
3. Nginx 没有缓存 `/admin/login`、`/idp` 或 `/oidc` 的响应。
4. 第三方 OIDC 提供方的 post-logout redirect URI 与本应用的退出回调一致。

在已经包含 `proxy_pass http://headscale_console;` 的 location 中加入：

```nginx
proxy_no_cache 1;
proxy_cache_bypass 1;
```

不要单独新建一个只有缓存指令、没有 `proxy_pass` 的 location，否则该路径会被
Nginx 当作本地文件处理。本项目的完整 server 示例已经转发所有路径；通常不需要
额外增加 location，只有在上游配置了缓存时才需要显式禁用上述路径的缓存。

### 登录后反复跳转或 Cookie 不生效

- 确认 `base_url` 使用 `https://`，并与浏览器地址完全一致。
- 不要通过不同域名交替访问管理后台。
- 不要在 Nginx 中改写 Cookie 的 Domain 或 Path。
- 检查系统时间是否准确，OIDC token 对时间敏感。

### Headplane 页面不实时或 WebSocket 断开

检查反向代理是否保留：

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection $hsc_connection_upgrade;
```

同时确认 CDN、负载均衡器和防火墙没有设置过短的连接超时。

### 上传文件返回 413

提高 Nginx 的限制后重载：

```nginx
client_max_body_size 32m;
```

```bash
sudo nginx -t && sudo systemctl reload nginx
```

### 404 或路径错误

- Nginx 的 `proxy_pass` 不要带路径或结尾 `/`。
- `server.base_url` 不要包含 `/admin`、`/idp` 或任何路径。
- Headplane 应访问 `https://hs.example.com/admin/`，通常需要结尾斜杠。
