# cf-san-ip

Cloudflare Worker、D1 和 React 管理界面组成的 IP 池管理服务。Worker 同源提供管理 API、管理 SPA，以及使用 Bearer Token 保护的 `POST /postip` 和 `GET /getip`。

## 本地开发

需要已安装 Node.js 和 pnpm。安装依赖并创建本地 Worker Secret 文件：

```sh
pnpm install
cp .dev.vars.example .dev.vars
```

将 `.dev.vars` 中的示例值替换为仅供本机开发使用的随机 `JWT_SECRET`，不要复用生产密钥，也不要提交 `.dev.vars`。本地 D1 migration 和开发服务器：

```sh
pnpm exec wrangler d1 migrations apply cf-san-ip --local
pnpm dev
```

打开 `http://127.0.0.1:5173`，首次访问时设置管理员密码，然后登录。在后台的 Token 设置区域设置或生成 API Token；此 Token 用于公开接口。Cookie 带有 `Secure` 标志，管理界面应通过本地开发地址在浏览器中使用；需要用 curl 验证 cookie 会话时，应使用本地 HTTPS 代理和临时证书，不要移除或放宽 `Secure`。

公开接口示例（将占位 Token 替换为后台当前配置的值）：

```sh
curl -i \
  -H 'Authorization: Bearer local-only-token' \
  -H 'Content-Type: application/json' \
  --data '{"ips":[{"ip":"192.0.2.10","port":443,"asn":906,"region":"US"}]}' \
  http://127.0.0.1:5173/postip

curl -i \
  -H 'Authorization: Bearer local-only-token' \
  'http://127.0.0.1:5173/getip?asn=906&limit=10'
```

`/getip` 返回 `text/plain`，每行一个 IP。`limit` 默认是 10，正整数大于 20 时按 20 处理。

## 测试与构建

Worker 和共享代码测试使用 Cloudflare Vitest 插件及本地 D1 migration；客户端测试使用 jsdom。两个测试项目可以独立运行，也可通过总入口依次运行：

```sh
pnpm test
pnpm test:worker
pnpm test:client
pnpm typecheck
pnpm build
```

本地 migration 可重复应用；开发和验收请使用 `--local`，不会创建或访问 Cloudflare 远端资源：

```sh
pnpm exec wrangler d1 migrations apply cf-san-ip --local
```

## 生产部署

以下步骤是部署说明，执行前请确认当前 Wrangler 登录账号和目标 Cloudflare 账户。生产 D1 与本地 D1 相互独立：

1. 创建生产 D1，并将命令返回的真实 `database_id` 写入 `wrangler.jsonc` 中 `d1_databases[0].database_id`，替换现有的全零占位 ID：

   ```sh
   pnpm exec wrangler d1 create cf-san-ip
   ```

2. 将生产 JWT 签名密钥配置为 Worker Secret。使用密码管理器生成并保存高熵随机值；按 Wrangler 提示粘贴，不要把密钥写入源码或提交到 Git：

   ```sh
   pnpm exec wrangler secret put JWT_SECRET
   ```

3. 将 migration 应用到已确认的生产 D1，再构建并部署 Worker：

   ```sh
   pnpm exec wrangler d1 migrations apply cf-san-ip --remote
   pnpm deploy
   ```

首次部署后，通过 HTTPS 打开站点设置管理员密码，登录后设置或生成 API Token。不要将本地 `.dev.vars` 中的值用作生产凭据。

## 凭据存储与安全边界

按当前数据模型，管理员密码和 API Token **以明文保存在 D1**；有权限读取生产 D1、其备份或导出数据的人员也能读取这两项凭据。应严格限制 D1、备份和 Cloudflare 账号访问权限，并在怀疑泄露时立即更换管理员密码及 API Token。API Token 可在管理界面查看，因此任何获得管理会话的人都能读取它。

`JWT_SECRET` 不存入 D1：生产环境必须通过 Worker Secret 配置；仅本地开发使用被 Git 忽略的 `.dev.vars`。管理会话 Cookie 保持 `HttpOnly`、`Secure` 和 `SameSite=Lax`，生产站点必须通过 HTTPS 提供服务。
