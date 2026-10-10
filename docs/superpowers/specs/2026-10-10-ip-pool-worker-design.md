# IP Pool Worker 管理系统设计

- 日期：2026-10-10
- 状态：待用户审阅
- 项目：Cloudflare Workers + D1 IP 管理与查询服务

## 1. 目标与范围

构建一个由 Cloudflare Worker 和 D1 提供服务的 IP 管理应用，包含同源 Web 管理后台，以及两个使用固定 Bearer Token 鉴权的公开数据接口：`POST /postip` 和 `GET /getip`。

后台需要密码登录；首次部署时通过前端设置管理员密码。管理员密码和 API Token 按用户明确要求以明文保存在 D1。登录状态用 JWT Cookie 保存。管理后台可以管理 IP 记录、按 ASN 设置别名、设置并查看 API Token。

本阶段范围包括项目代码、D1 migration、本地开发与部署文档和测试；不连接用户的 Cloudflare 账号，不创建远端 D1，不执行生产部署。

## 2. 架构

- TypeScript Cloudflare Worker 处理公开接口、登录、会话校验和后台管理 API。
- Hono 负责 Worker 路由。
- React + Vite 构建管理 SPA。
- Worker 在同一来源提供 SPA 静态资源和 API，不拆分到 Cloudflare Pages。
- D1 通过 Worker binding 访问；数据库结构用 SQL migration 管理。
- Wrangler 配置本地开发、静态资源和 D1 binding；JWT 签名密钥通过 Worker Secret `JWT_SECRET` 提供，不存入 D1。

## 3. 数据模型

### 3.1 IP 记录

`ip_records` 表每条 IP 记录保存：

| 字段 | 约束 / 含义 |
| --- | --- |
| `ip` | 主键；IPv4 或 IPv6 |
| `port` | 必填整数，范围 `1–65535` |
| `asn` | 必填正整数 |
| `region` | 必填文本，不允许空格 |
| `asn_alias` | 可空文本；公开输出时优先使用此值，否则使用 ASN 数字 |

按 ASN 设置别名时，后台将该别名写入该 ASN 当前所有 IP 行的 `asn_alias`。`/postip` 未提交别名时，沿用同 ASN 现有记录的别名；若该 ASN 还没有别名，则新记录的别名为空。后续添加的字段通过新的 migration 扩展。

### 3.2 管理设置

管理员密码和 API Token 是全局设置，不属于每条 IP 记录。使用单独的设置记录保存；管理员密码和 API Token 均明文保存。初始化时仅设置管理员密码；登录后在后台设置或生成 API Token。未配置 API Token 前，两个公开数据接口返回未配置错误。

## 4. 公开接口

### 4.1 `POST /postip`

- Header：`Authorization: Bearer <token>`。
- Content-Type：`application/json`。
- Body：

```json
{
  "ips": [
    { "ip": "1.2.3.4", "port": 443, "asn": 906, "region": "US" }
  ]
}
```

`ip`、`port`、`asn`、`region` 每条都必填；`asn_alias` 可选。有效记录按 `ip` 主键新增或更新。批次逐条校验：有效记录保存，无效记录丢弃，不因单条错误拒绝整个批次。响应返回新增/更新成功数、失败数及失败项索引、IP（若可识别）和原因。JSON 无法解析或顶层 `ips` 不是数组时返回 `400`。

若同一请求中有多个有效行使用相同 IP，则按请求顺序处理，最后一条有效记录的值生效；无效行不覆盖有效行。

### 4.2 `GET /getip`

- Header：`Authorization: Bearer <token>`。
- Query 参数：`asn` 可选，`limit` 可选。
- 不传 `asn` 时从所有 IP 记录随机抽取；传入 ASN 时只从对应 ASN 抽取。
- 不传 `limit` 时默认返回 10 条；传入值超过 20 时按 20 条处理。非正整数或非数字的 `limit` 返回参数错误。
- 结果为 `text/plain; charset=utf-8`，每行一个 IP，格式为：

```text
ip:port#ASN别名 地区 序号
```

例如：

```text
1.2.3.4:443#DMIT US 01
```

没有 ASN 别名时 `#` 后显示 ASN 数字。随机选取后按 ASN、地区分组，并在每组内按 IP 排序；同组从 `01` 开始编号。即使该组只有一条记录，也显示 `01`。没有匹配记录时返回空的纯文本响应。

### 4.3 认证与错误

两个公开接口共用一个可在后台修改的 Bearer Token。Token 不放在 URL 中。缺少或错误的 Token 返回 `401`；Token 尚未设置时返回明确的未配置错误；无效 JSON 或查询参数返回 `400`。

## 5. 管理后台与会话

- 首次访问时，如果尚无管理员密码，显示一次性初始化页面；初始化成功后禁止再次走首次设置流程。
- 登录后设置 JWT Cookie；Cookie 使用 `HttpOnly`、`Secure`、`SameSite=Lax`，建议有效期 7 天。退出登录清除 Cookie。
- JWT 由 Worker Secret `JWT_SECRET` 签名。
- 管理界面包含 IP 记录新增、编辑、删除；ASN 别名设置；API Token 设置、生成和明文查看；登录和退出。
- IP 列表默认每页 20 条，提供 20、50、100 条选项，只渲染当前页及分页控件。
- 管理 API 需要有效 JWT 会话；不得将管理 API 当作公开 Bearer Token 接口。

## 6. 安全边界

管理员密码与 API Token 明文存储是用户明确要求的设计选择。任何能读取 D1 的主体都能读取这两项凭据；项目文档和部署说明必须明确这一风险。JWT 签名密钥单独放入 Worker Secret。

## 7. 测试与验收

测试至少覆盖：

- IPv4/IPv6、必填字段、端口范围、ASN 格式、地区空格和别名格式校验。
- `/postip` 的批次部分成功、失败项原因、按 IP 更新和同批重复 IP 行为。
- ASN 别名传播、别名继承与无别名时回退到 ASN 数字。
- `/getip` 的 Bearer 鉴权、ASN 筛选、默认数量、20 条上限、随机抽取、纯文本格式和 `01` 序号。
- 初始化只能完成一次、登录、JWT 会话保护、退出登录和后台 CRUD。
- 后台分页大小 20/50/100 与当前页展示。

通过 Worker 本地测试和本地 D1 migration 验证后，提供可复现的本地开发、建库、migration 和部署步骤。生产部署及远端数据库操作不在本次实施范围内。
