# EchoEar Cloud 容器镜像

本文说明主分支自动构建后的镜像地址、引用方式和本地使用方法。

## 镜像地址

| 项目 | 值 |
| --- | --- |
| 仓库 | [github.com/18345174/echoear_cloud](https://github.com/18345174/echoear_cloud) |
| 注册表 | GitHub Container Registry（GHCR） |
| 镜像全名 | `ghcr.io/18345174/echoear_cloud` |
| Packages 页面 | https://github.com/18345174/echoear_cloud/pkgs/container/echoear_cloud |
| Actions 工作流 | `.github/workflows/docker-publish.yml` |
| Actions 运行记录 | https://github.com/18345174/echoear_cloud/actions/workflows/docker-publish.yml |

## 何时构建

以下事件会触发构建并推送镜像：

1. 向 `main` 分支 push
2. 在 GitHub Actions 页面手动运行 **Publish Docker image**（`workflow_dispatch`）

每次成功构建至少会打这些标签：

| 标签 | 含义 |
| --- | --- |
| `latest` | 当前 `main` 最新成功构建（推荐日常使用） |
| `<short-sha>` | 短 commit，例如 `a1b2c3d` |
| `<full-sha>` | 完整 commit SHA，便于精确回滚 |
| `YYYYMMDD-HHmmss` | 构建时间戳（UTC） |

示例：

```text
ghcr.io/18345174/echoear_cloud:latest
ghcr.io/18345174/echoear_cloud:a1b2c3d
ghcr.io/18345174/echoear_cloud:606925cc0c3ff2640876692b0a15c0383ab53fa3
ghcr.io/18345174/echoear_cloud:20260804-083012
```

## 拉取镜像

公开包可直接拉取：

```bash
docker pull ghcr.io/18345174/echoear_cloud:latest
```

若包尚未设为 Public，先登录再拉：

```bash
# 使用有 packages:read 权限的 GitHub token
echo "$GITHUB_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
docker pull ghcr.io/18345174/echoear_cloud:latest
```

把包设为公开（仓库维护者操作一次即可）：

1. 打开 https://github.com/users/18345174/packages/container/echoear_cloud/settings
2. 在 **Danger Zone / Change package visibility** 中改为 **Public**

或用 GitHub CLI：

```bash
gh api \
  --method PUT \
  -H "Accept: application/vnd.github+json" \
  /user/packages/container/echoear_cloud/visibility \
  -f visibility=public
```

> 说明：`gh` 需登录为包所有者账号（`18345174`）。若你是协作者，请在网页 Packages 设置里改可见性。

## 在 Docker Compose 中引用

本仓库 `docker-compose.yml` 已直接使用云上最新镜像：

```yaml
api:
  image: ghcr.io/18345174/echoear_cloud:latest
  pull_policy: always
```

启动：

```bash
cp .env.example .env
# 编辑 .env，填入真实密码和 PUBLIC_BASE_URL
docker compose pull
docker compose up -d
```

固定到某次构建（生产推荐）：

```yaml
api:
  image: ghcr.io/18345174/echoear_cloud:<short-sha>
  # 不要写 pull_policy: always，避免被 latest 覆盖预期版本
```

环境变量覆盖镜像名（可选）：

```bash
export ECHOEAR_IMAGE=ghcr.io/18345174/echoear_cloud:a1b2c3d
# 若 compose 里写成 image: ${ECHOEAR_IMAGE:-ghcr.io/18345174/echoear_cloud:latest}
docker compose up -d
```

当前仓库默认写死 `latest`，需要固定版本时直接改 `docker-compose.yml` 中的标签即可。

## 在 Dockerfile / 其他编排中引用

```dockerfile
FROM ghcr.io/18345174/echoear_cloud:latest
```

Kubernetes 示例：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echoear-cloud
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/18345174/echoear_cloud:latest
          imagePullPolicy: Always
          ports:
            - containerPort: 8080
```

## 仅跑 API 容器（自备 PostgreSQL）

```bash
docker run --rm -p 8080:8080 \
  -e POSTGRES_HOST=host.docker.internal \
  -e POSTGRES_PORT=5432 \
  -e POSTGRES_DB=echoear \
  -e POSTGRES_USER=echoear \
  -e POSTGRES_PASSWORD=your-db-password \
  -e BOOTSTRAP_ADMIN_PASSWORD=your-admin-password \
  -e PUBLIC_BASE_URL=https://echoear.example.com \
  ghcr.io/18345174/echoear_cloud:latest
```

## 本地改代码时临时构建

日常部署请用 GHCR 镜像。只有本地改 Dockerfile/源码需要验证时：

```bash
docker build -t echoear_cloud:local .
docker compose -f docker-compose.yml \
  -f <(printf 'services:\n  api:\n    image: echoear_cloud:local\n    pull_policy: never\n') \
  up -d
```

或临时把 `docker-compose.yml` 里的 `image` 换成 `build: .` 再 `docker compose up -d --build`。

## 工作流权限说明

工作流使用仓库内置 `GITHUB_TOKEN`，权限：

- `contents: read` — 检出代码
- `packages: write` — 推送到 `ghcr.io`

无需额外配置 PAT。首次推送成功后，建议把 package 可见性设为 Public，方便匿名 `docker pull`。
