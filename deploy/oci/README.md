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

健康检查（服务器本机）：

```bash
curl -fsS http://127.0.0.1:18080/healthz
```

## 你需要改的配置

登录服务器编辑：

```bash
nano /opt/stack/echoear_cloud/.env
```

至少改掉：

- `POSTGRES_PASSWORD`
- `BOOTSTRAP_ADMIN_PASSWORD`
- `PUBLIC_BASE_URL`（真实对外源站，例如 `https://echoear.your.domain`）

改完后执行一次 `./deploy.sh` 或手动跑 **Deploy OCI** 工作流。

域名 / Traefik 反代尚未默认打开；当前 api 仅绑定 `127.0.0.1:18080`。需要公网域名时，再在 compose 里加 Traefik labels 并接入 `proxy` 网络。
