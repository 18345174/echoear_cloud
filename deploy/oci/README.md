# OCI 部署说明

服务器：Oracle Cloud Always Free ARM（`ubuntu@161.153.84.30`）  
部署目录：`/opt/stack/echoear_cloud`

## 目录内容（服务器上）

| 文件 | 作用 |
| --- | --- |
| `docker-compose.yml` | postgres + api，镜像来自 GHCR |
| `.env` | 简单占位配置（请自行改密码 / PUBLIC_BASE_URL） |
| `deploy.sh` | 拉最新镜像并重启；GitHub Actions 远程只执行它 |
| `README.md` | 服务器侧简要说明 |
| `relay_data/` | 自托管 HAPI Relay 的 WireGuard、证书和访问控制状态 |

## 专用 SSH 部署密钥

**不要**把个人 OCI 私钥放进 GitHub Secrets。

本项目使用独立密钥对：

| 项 | 位置 |
| --- | --- |
| 私钥（仅 Actions） | GitHub Secret `OCI_DEPLOY_SSH_KEY` |
| 公钥（服务器） | `ubuntu` 的 `~/.ssh/authorized_keys` |
| 本机备份（可选） | `~/.ssh/echoear_cloud_deploy_ed25519` |
| 指纹 | `SHA256:GED6aNEzBLMIexM8kB5Uz4F9mknyqNdS1NbHnpplJ0Y` |

服务器上的公钥配置了 **强制命令**，只能执行：

```text
/opt/stack/echoear_cloud/deploy.sh
```

即使有人拿到这把专用私钥，也无法打开交互 shell，只能触发部署脚本。

### 仓库 Secrets

| Secret | 示例值 |
| --- | --- |
| `OCI_DEPLOY_HOST` | `161.153.84.30` |
| `OCI_DEPLOY_USER` | `ubuntu` |
| `OCI_DEPLOY_SSH_KEY` | 专用私钥全文（含 `BEGIN OPENSSH PRIVATE KEY`） |

## Action 触发关系

1. **Publish Docker image**（`docker-publish.yml`）  
   - `main` 推送后构建 **linux/amd64 + linux/arm64** 多架构镜像并推 GHCR  
   - 成功后用 `gh workflow run deploy-oci.yml` **主动触发**部署工作流  

2. **Deploy OCI**（`deploy-oci.yml`）  
   - 独立工作流，也可在 Actions 页手动 Run  
   - **不会**用 `workflow_run` 去监听上一个 Action  

```text
main push
  → Publish Docker image（多架构）
  → 主动 dispatch
  → Deploy OCI（SSH 跑 deploy.sh）
```

新版 `deploy.sh` 在拉镜像前会从仓库的 `main` 分支整组下载并校验
`docker-compose.yml`、`deploy.sh` 和 `validate-relay.sh`。三份文件全部有效
时才替换服务器上的部署文件，然后立刻由下载后的脚本继续部署；服务器
`.env` 不会被覆盖。这个能力需要先在服务器上手工安装一次新版
`deploy.sh`；旧版脚本本身无法自我获得这项能力。

同步默认兼容新旧仓库版本。如果目标分支还是旧版、缺少 Relay 文件，或
下载/校验临时失败，脚本会清理 `.next` 临时文件，保留服务器当前部署
文件，并继续执行原有 API 的 `pull/up`。如需让部署文件同步失败时立即
中止，可设置 `ECHOEAR_DEPLOY_SYNC_REQUIRED=true`；首次升级建议保持默认
`false`。

如需临时固定部署清单版本，可在服务器 `.env` 中设置
`ECHOEAR_DEPLOY_REF=<commit-sha>`。紧急情况下在 `.env` 设置
`ECHOEAR_SYNC_DEPLOY_FILES=false` 可停止自动同步。

## 手动部署

服务器上：

```bash
cd /opt/stack/echoear_cloud
./deploy.sh
```

本机用专用密钥（同样只会跑 deploy.sh）：

```bash
ssh -i ~/.ssh/echoear_cloud_deploy_ed25519 \
  -o IdentitiesOnly=yes \
  ubuntu@161.153.84.30
```

健康检查：

```bash
# 服务器本机
curl -fsS http://127.0.0.1:18080/healthz

# 公网（Traefik + Cloudflare）
curl -fsS https://xu-hapi.flyooo.uk/healthz
```

## 域名与反代

| 项 | 值 |
| --- | --- |
| 公网域名 | `https://xu-hapi.flyooo.uk` |
| 应用配置 | `.env` 里 `PUBLIC_BASE_URL=https://xu-hapi.flyooo.uk` |
| Traefik Host 规则 | 与上面同一域名 |
| Docker 网络 | `proxy`（与 OCI 上其他服务一致） |
| 证书 | Traefik `letsencrypt` |

DNS 由 Cloudflare 指向本机源站（橙云即可，与 `imagegen` / `way` 同套路）。

## 自托管 HAPI Relay

Relay 是部署栈中的可选 `tunwg` 容器。默认 profile 关闭，不影响现有 API。
启用后 Controller 的本地 HAPI Hub 通过 WireGuard/TCP Relay 接入该服务，App
从加密 descriptor 得到动态 HTTPS 地址后直接访问 Hub；现有 EchoEar
WebSocket 隧道继续作为回退。

### 首次引导

1. 恢复 OCI Console、Cloud Shell 或普通运维 SSH 访问。
2. 将本仓库的 `deploy/oci/deploy.sh` 安装到
   `/opt/stack/echoear_cloud/deploy.sh`，权限设为 `0755`。
3. 保持 `.env` 中 `ECHOEAR_RELAY_ENABLED=false` 和
   `ECHOEAR_DEPLOY_SYNC_REQUIRED=false`，先执行一次 `deploy.sh`。即使此时
   远端分支仍是旧版，现有 API 也会继续按服务器上的清单更新。
4. 确认 API `/healthz` 正常，再继续配置 Relay。

### DNS 与网络

假设 Relay 根域名为 `relay.xu-hapi.flyooo.uk`，创建两个指向 OCI 公网 IP
的 A 记录：

```text
relay.xu-hapi.flyooo.uk       A  <OCI_PUBLIC_IP>
*.relay.xu-hapi.flyooo.uk     A  <OCI_PUBLIC_IP>
```

这两个记录必须使用 Cloudflare **DNS only（灰云）**。Cloudflare HTTP
代理会终止 TLS，破坏 tunwg 的 SNI 路由和端到端证书签发。

OCI Security List/NSG 与主机防火墙需要允许：

| 协议 | 端口 | 用途 |
| --- | --- | --- |
| TCP | 80 | ACME HTTP-01 和 HTTPS 跳转 |
| TCP | 443 | Relay API、TLS passthrough 和 TCP fallback |
| UDP | 443 | WireGuard 默认高速通道 |

TCP 80/443 仍由现有 Traefik 监听。Relay 通过 Docker labels 接收自己的
根域名和动态子域名，其中 443 使用 TLS passthrough；UDP 443 由 Relay
容器直接发布。Traefik 必须支持 `HostSNIRegexp`。

### 服务器配置

在 `/opt/stack/echoear_cloud/.env` 增加：

```dotenv
ECHOEAR_RELAY_ENABLED=true
ECHOEAR_RELAY_IMAGE=ghcr.io/tiann/tunwg:eb51a7f
ECHOEAR_RELAY_DOMAIN=relay.xu-hapi.flyooo.uk
ECHOEAR_RELAY_PUBLIC_IP=<OCI_PUBLIC_IP>
ECHOEAR_RELAY_AUTH_SECRET=<openssl-rand-hex-32>
ECHOEAR_RELAY_QUOTA_BYTES=0
ECHOEAR_RELAY_SSL_EMAIL=<operator-email>
```

`ECHOEAR_RELAY_AUTH_SECRET` 只保存在服务器 `.env`，不能提交。首次启用前
创建并保护持久化目录：

```bash
install -d -m 0700 /opt/stack/echoear_cloud/relay_data
cd /opt/stack/echoear_cloud
./deploy.sh
./validate-relay.sh .env
```

部署健康检查对 `/issue` 使用无副作用 GET，并预期 HTTP 405；不会消耗
Relay 的每 IP 签发额度。随后还必须用 Controller 做一次真实 `--relay`
连接，分别验证 UDP 默认模式和 `HAPI_RELAY_FORCE_TCP=true` 回退模式。

Controller 生产配置最终应使用：

```dotenv
HAPI_RELAY_API=relay.xu-hapi.flyooo.uk
```

不要在公网端点使用共享 `HAPI_RELAY_AUTH`。Controller 会通过 `/issue`
获取每个 Hub 独立、可撤销的签名 key，并持久化在本地 HAPI 设置中。

## 配置文件

真实密钥不要提交到 Git。`ACCESS_TICKET_SIGNING_KEY` 留空时，服务会在 `app_settings` 中原子生成并持久化一次；也可以在服务器 `/opt/stack/echoear_cloud/.env`（权限 600）中用 `openssl rand -base64 32` 的结果显式覆盖，后续必须保持不变。
仓库里的 `deploy/oci/.env.example` 仅作模板。
