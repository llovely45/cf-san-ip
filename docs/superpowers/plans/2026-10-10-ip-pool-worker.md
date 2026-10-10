# Cloudflare Worker IP 管理系统实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现一个由 Cloudflare Worker + D1 提供的同源管理后台与 IP 查询服务，支持 `/postip`、`/getip`、IP CRUD、ASN 别名、Bearer Token 和 JWT 登录。

**Architecture:** TypeScript Worker 使用 Hono 路由，React + Vite 提供单页管理界面，Cloudflare Vite 插件将两者用于同一 Worker。D1 存 IP 记录和全局管理设置；SQL migrations 管理表结构。

**Tech Stack:** Node.js 24（本地已安装）、pnpm（本地已安装）、TypeScript、React、Vite、`@cloudflare/vite-plugin`、Hono、D1、`ipaddr.js`、`jose`、Vitest、`@cloudflare/vitest-plugin`。

**Spec:** `docs/superpowers/specs/2026-10-10-ip-pool-worker-design.md`

## Global Constraints

- 两个公开数据接口路径固定为 `POST /postip` 与 `GET /getip`，二者均需同一个 `Authorization: Bearer <token>`。
- IP、端口、ASN、地区为必填字段；地区不得含空格；ASN 别名可选且不得含空格。
- IP 作为唯一键；有效重复记录更新，批量中的无效项单独丢弃；同批相同 IP 按顺序处理，最后一条有效记录生效。
- `/getip` 不传 `asn` 时在全库抽样；不传 `limit` 默认 10；`limit` 最大按 20 处理；结果格式为 `ip:port#别名或ASN 地区 NN`，单条也编号 `01`。
- ASN 别名存于每条 IP 记录；后台修改 ASN 别名时更新该 ASN 全部已有记录；POST 未带别名时从同 ASN 现有记录继承。
- 管理员密码和 API Token 按用户确认明文存入 D1；后台设置 Token 并可查看；实现与部署文档明确 D1 读取者可获取它们。
- 首次设置密码只可成功一次；JWT 使用 `JWT_SECRET` Worker Secret，并通过 7 天有效的 HttpOnly、Secure、SameSite=Lax Cookie 保存。
- 管理后台支持 IP 新增、编辑、删除；每页只展示当前页，默认 20 条，可选择 20、50、100。
- 本任务只做本地项目、测试、migration 和部署说明；不创建远端 D1、不访问用户 Cloudflare 账号、不生产部署。

---

## 文件结构

- `package.json`、`pnpm-lock.yaml`：依赖与开发、构建、测试命令。
- `wrangler.jsonc`：Worker 入口、兼容日期、D1 binding 和 migration 目录。
- `vite.config.ts`：React 与 Cloudflare Vite 插件。
- `tsconfig.json`、`tsconfig.worker.json`、`tsconfig.client.json`：严格 TypeScript 配置与 Worker/前端类型边界。
- `src/worker/index.ts`：Hono 应用入口和路由挂载。
- `src/worker/env.ts`：`DB` 与 `JWT_SECRET` 运行环境类型。
- `src/worker/middleware/api-token.ts`：公开 API Bearer Token 校验。
- `src/worker/middleware/admin-session.ts`：后台 JWT Cookie 校验。
- `src/worker/routes/public.ts`：`/postip`、`/getip`。
- `src/worker/routes/setup-auth.ts`：初始化、登录、会话查询、退出。
- `src/worker/routes/admin.ts`：记录分页与 CRUD、别名和 Token 管理。
- `src/worker/repositories/ip-records.ts`：D1 IP 记录查询和写入。
- `src/worker/repositories/settings.ts`：D1 全局设置读写。
- `src/worker/services/ip-service.ts`：批量逐条校验、别名继承和随机查询编排。
- `src/worker/services/session-service.ts`：首次初始化与 JWT 签发、验证。
- `src/shared/ip.ts`、`src/shared/output.ts`：无平台依赖的校验、地址规范化和纯文本格式化。
- `src/client/main.tsx`、`src/client/App.tsx`、`src/client/api.ts`：SPA 入口、页面状态和同源 API 客户端。
- `src/client/components/`：登录/初始化、IP 列表、记录编辑、别名和 Token 面板。
- `src/client/styles.css`：管理界面样式。
- `migrations/0001_initial.sql`：IP 记录和单例设置表。
- `tests/worker/`：Worker、D1、鉴权和路由集成测试。
- `tests/shared/`：地址校验、查询参数和输出格式单元测试。
- `tests/client/`：React 页面交互测试。
- `vitest.config.ts`、`vitest.client.config.ts`：Worker 与 jsdom 两套测试配置。
- `README.md`、`.dev.vars.example`、`.gitignore`：安全的本地设置和部署说明。

## Task 1: 搭建 Vite + Worker 项目骨架

**Files:**
- Create: `package.json`, `pnpm-lock.yaml`, `wrangler.jsonc`, `vite.config.ts`, `vitest.config.ts`, `tsconfig.json`, `tsconfig.worker.json`, `tsconfig.client.json`, `index.html`, `.gitignore`
- Create: `src/worker/index.ts`, `src/worker/env.ts`, `src/client/main.tsx`, `src/client/App.tsx`, `src/client/styles.css`

**Interfaces:**
- Worker 入口导出 Hono `app`，由 Vite Cloudflare 插件加载。
- `index.html` 加载 `src/client/main.tsx`；React 根节点渲染一个可见的 `IP 管理` 标题。`src/worker/env.ts` 声明 `Env` 中的 `DB: D1Database` 与 `JWT_SECRET: string`。

- [ ] **Step 1: 建立 pnpm 项目并添加运行时依赖**

Create `package.json` with these exact scripts, then install the dependencies below:

```json
{
  "name": "cf-san-ip",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite --host 127.0.0.1",
    "build": "vite build",
    "preview": "vite preview --host 127.0.0.1",
    "deploy": "pnpm build && wrangler deploy",
    "typecheck": "tsc -p tsconfig.worker.json --noEmit && tsc -p tsconfig.client.json --noEmit",
    "test:worker": "vitest run --config vitest.config.ts",
    "test:client": "vitest run --config vitest.client.config.ts",
    "test": "pnpm test:worker && pnpm test:client"
  }
}
```

```bash
pnpm add hono jose ipaddr.js react react-dom
pnpm add -D typescript vite @vitejs/plugin-react @cloudflare/vite-plugin wrangler @types/react @types/react-dom @cloudflare/workers-types @types/node vitest @cloudflare/vitest-plugin @testing-library/react @testing-library/user-event jsdom
```

- [ ] **Step 2: 添加 Vite Cloudflare 集成与脚本**

`vite.config.ts` 使用 `react()` 与 `cloudflare()` 插件；`vitest.config.ts` 初始化 `cloudflareTest()`；`package.json` 提供 `dev`、`build`、`preview`、`deploy`、`typecheck`、`test:worker`、`test:client` 和 `test` 脚本。`typecheck` 分别检查 Worker 和 client tsconfig，`test:worker` 使用 `vitest.config.ts`，`test:client` 使用 `vitest.client.config.ts`。`deploy` 只构建并调用 Wrangler，执行阶段不得运行它。

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { cloudflare } from "@cloudflare/vite-plugin";

export default defineConfig({ plugins: [react(), cloudflare()] });
```

- [ ] **Step 3: 配置 Worker、SPA 回退和本地 D1 binding**

`wrangler.jsonc` 指向 `src/worker/index.ts`，设置 `compatibility_date` 为 `2026-10-10`、D1 binding 为 `DB`、migration 目录为 `migrations`，并启用 SPA `not_found_handling`。数据库 ID 使用明显的本地占位值；README 说明部署前须替换为用户创建的 D1 ID。

```jsonc
{
  "$schema": "./node_modules/wrangler/config-schema.json",
  "name": "cf-san-ip",
  "main": "./src/worker/index.ts",
  "compatibility_date": "2026-10-10",
  "assets": {
    "not_found_handling": "single-page-application",
    "run_worker_first": ["/api/*", "/postip", "/getip"]
  },
  "d1_databases": [{
    "binding": "DB",
    "database_name": "cf-san-ip",
    "database_id": "00000000-0000-0000-0000-000000000000",
    "migrations_dir": "migrations"
  }]
}
```

- [ ] **Step 4: 添加最小 Worker 和 React 首页**

Worker 为后续 Hono 路由提供默认 JSON 404；首页显示 `IP 管理` 和“项目已启动”状态，不添加未约定的业务接口。

- [ ] **Step 5: 验证骨架可构建并可启动**

```bash
pnpm build
pnpm dev
```

另开终端执行 `curl -fsS http://127.0.0.1:5173/`，确认响应包含 SPA 的 `#root` 入口。构建必须退出码为 0。

- [ ] **Step 6: 提交骨架**

```bash
git add package.json pnpm-lock.yaml wrangler.jsonc vite.config.ts tsconfig*.json index.html .gitignore src
git commit -m "feat: scaffold Worker and React app"
```

## Task 2: 实现领域校验、参数解析与输出格式化

**Files:**
- Create: `src/shared/ip.ts`, `src/shared/output.ts`
- Create: `tests/shared/ip.test.ts`, `tests/shared/output.test.ts`, `vitest.client.config.ts`
- Modify: `package.json`, `tsconfig.client.json`

**Interfaces:**

```ts
export interface IpRecord {
  ip: string;
  port: number;
  asn: number;
  region: string;
  asnAlias: string | null;
}

export type Validation<T> =
  | { ok: true; value: T }
  | { ok: false; reason: string };

export function validateIpInput(input: unknown): Validation<Omit<IpRecord, "asnAlias"> & { asnAlias?: string }>;
export function parseGetIpQuery(params: URLSearchParams): Validation<{ asn: number | null; limit: number }>;
export function formatGetIpOutput(rows: IpRecord[]): string;
```

- [ ] **Step 1: 先写非法/有效 IP 输入测试**

覆盖 IPv4、IPv6、端口边界、正整数 ASN、必填字段、类型错误、地区空格、别名空格和可选别名。

```ts
expect(validateIpInput({ ip: "2001:db8::1", port: 443, asn: 906, region: "US" }).ok).toBe(true);
expect(validateIpInput({ ip: "bad", port: 0, asn: -1, region: "US West" }).ok).toBe(false);
```

- [ ] **Step 2: 运行指定测试确认失败**

```bash
pnpm exec vitest run --config vitest.client.config.ts tests/shared/ip.test.ts
```

预期：测试因实现尚不存在而失败。

- [ ] **Step 3: 实现地址规范化和逐字段验证**

用 `ipaddr.js` 验证并规范化 IP；去除输入两端空白；端口限制为 1 至 65535；ASN 必须为正整数；地区必填且含任何空白字符时拒绝；别名可选，但提供时必须是非空单词。

- [ ] **Step 4: 添加查询参数和输出测试，再实现函数**

测试默认 10、最大 20、非法 limit、可选 ASN、别名回退和逐组 `01` 编号；格式化必须稳定按 ASN、地区和 IP 排序，不添加尾随空行。

```ts
expect(formatGetIpOutput([{ ip: "1.2.3.4", port: 443, asn: 906, region: "US", asnAlias: "DMIT" }]))
  .toBe("1.2.3.4:443#DMIT US 01");
```

- [ ] **Step 5: 运行领域测试和类型检查**

```bash
pnpm test:client -- tests/shared
pnpm typecheck
```

预期：领域测试通过，TypeScript 无错误。

- [ ] **Step 6: 提交领域逻辑**

```bash
git add src/shared tests/shared vitest.client.config.ts package.json tsconfig.client.json pnpm-lock.yaml
git commit -m "feat: validate and format IP records"
```

## Task 3: 建立 D1 schema 与记录仓储

**Files:**
- Create: `migrations/0001_initial.sql`, `src/worker/repositories/ip-records.ts`, `src/worker/repositories/settings.ts`
- Create: `tests/worker/repositories.test.ts`, `tests/worker/apply-migrations.ts`
- Modify: `wrangler.jsonc`, `package.json`, `tsconfig.worker.json`, `vitest.config.ts`

**Interfaces:**

```ts
export interface IpRepository {
  upsert(record: IpRecord): Promise<void>;
  getByIp(ip: string): Promise<IpRecord | null>;
  findAliasByAsn(asn: number): Promise<string | null>;
  setAliasForAsn(asn: number, alias: string | null): Promise<number>;
  deleteByIp(ip: string): Promise<boolean>;
  list(page: number, pageSize: 20 | 50 | 100): Promise<{ rows: IpRecord[]; total: number }>;
  sample(limit: number, asn: number | null): Promise<IpRecord[]>;
}
```

- [ ] **Step 1: 先写 D1 仓储集成测试**

用 Worker Vitest 配置提供 `DB` binding，并在测试 setup 中从 `@cloudflare/vitest-plugin` 导入 `readD1Migrations`，通过 `readD1Migrations("migrations")` 读取 migration 并注入 `TEST_MIGRATIONS` binding；setup 文件从 `cloudflare:test` 导入 `applyD1Migrations`，从 `cloudflare:workers` 导入 `env`，然后调用 `applyD1Migrations(env.DB, env.TEST_MIGRATIONS)`。测试增改、分页、别名更新和 ASN 随机筛选。

```ts
const repository = new D1IpRepository(env.DB);
await repository.upsert({ ip: "1.2.3.4", port: 443, asn: 906, region: "US", asnAlias: null });
expect(await repository.getByIp("1.2.3.4")).toMatchObject({ port: 443, asn: 906 });
```

- [ ] **Step 2: 运行仓储测试确认失败**

```bash
pnpm test:worker -- tests/worker/repositories.test.ts
```

预期：测试因 migration 和 repository 尚不存在而失败。

- [ ] **Step 3: 创建初始 migration**

创建 `ip_records` 表，`ip` 为主键，包含 `port`、`asn`、`region`、可空 `asn_alias`；添加 `(asn, region)` 索引。创建单例 `app_settings` 表，字段为 `singleton_id=1`、`admin_password`、`api_token`，两项凭据均按设计明文保存，初值均为 NULL。

```sql
CREATE TABLE ip_records (
  ip TEXT PRIMARY KEY,
  port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
  asn INTEGER NOT NULL CHECK (asn > 0),
  region TEXT NOT NULL,
  asn_alias TEXT
);
CREATE INDEX idx_ip_records_asn_region ON ip_records(asn, region);
CREATE TABLE app_settings (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  admin_password TEXT,
  api_token TEXT
);
INSERT INTO app_settings(singleton_id, admin_password, api_token) VALUES (1, NULL, NULL);
```

- [ ] **Step 4: 实现 D1 仓储方法**

用 prepared statements 和 `bind()` 参数化所有值；分页使用单独的 `COUNT(*)` 与 `LIMIT/OFFSET`；抽样查询按是否提供 ASN 选择 SQL；别名设置更新该 ASN 的所有行；设置仓储读写唯一设置行。

- [ ] **Step 5: 执行 D1 测试与本地 migration 检查**

```bash
pnpm test:worker -- tests/worker/repositories.test.ts
pnpm exec wrangler d1 migrations apply cf-san-ip --local
```

预期：测试通过，migration 可重复检查且不修改任何远端数据库。

- [ ] **Step 6: 提交 schema 和仓储**

```bash
git add migrations src/worker/repositories tests/worker wrangler.jsonc package.json pnpm-lock.yaml tsconfig.worker.json vitest.config.ts
git commit -m "feat: add D1 schema and repositories"
```

## Task 4: 实现公开 Bearer API 与批量写入服务

**Files:**
- Create: `src/worker/middleware/api-token.ts`, `src/worker/routes/public.ts`, `src/worker/services/ip-service.ts`
- Create: `tests/worker/public-api.test.ts`
- Modify: `src/worker/index.ts`, `src/worker/repositories/ip-records.ts`

**Interfaces:**

```ts
export interface BatchResult {
  accepted: number;
  rejected: number;
  errors: Array<{ index: number; ip?: string; reason: string }>;
}

export async function ingestBatch(db: D1Database, input: unknown): Promise<BatchResult>;
export function createApp(env: Env): Hono;
```

- [ ] **Step 1: 先写 Worker API 测试**

覆盖未配置 Token、缺失/错误 Bearer Token、成功授权、顶层 JSON 错误、部分行失败、重复 IP 后写覆盖、GET 默认 10 条、ASN 过滤和最大 20 条。

```ts
import { exports } from "cloudflare:workers";

const response = await exports.default.fetch(new Request("https://app.test/postip", {
  method: "POST",
  headers: { Authorization: "Bearer test-token", "Content-Type": "application/json" },
  body: JSON.stringify({ ips: [{ ip: "1.2.3.4", port: 443, asn: 906, region: "US" }] }),
}));
expect(response.status).toBe(200);
```

- [ ] **Step 2: 运行 API 测试确认失败**

```bash
pnpm test:worker -- tests/worker/public-api.test.ts
```

预期：路由不存在或断言失败。

- [ ] **Step 3: 实现 Token 中间件**

从 D1 读取单例 Token，要求请求头精确使用 Bearer scheme；没有设置 Token 时返回稳定 `503` 错误码；缺失或不匹配时返回 `401`；比较时使用固定时长比较函数。

- [ ] **Step 4: 实现逐条批次服务与 `/postip`**

确认顶层 `ips` 是数组；每项独立验证，失败记录索引和原因后继续；有效项按数组顺序处理。同 IP 的最后一条有效项获胜。未提供别名时从同 ASN 现有 IP 中继承，查询不到则存 NULL。响应只返回汇总及错误项，不回显 Token。

- [ ] **Step 5: 实现 `/getip` 并测试纯文本格式**

解析 `asn`、`limit`；非法参数为 `400`；合法请求通过仓储随机抽取，格式化成逐行文本并设置 `text/plain; charset=utf-8`；空结果为 HTTP 200 和空文本。

- [ ] **Step 6: 运行公开 API 测试**

```bash
pnpm test:worker -- tests/worker/public-api.test.ts
pnpm typecheck
```

预期：API 测试通过，类型检查无错误。

- [ ] **Step 7: 提交公开 API**

```bash
git add src/worker tests/worker
git commit -m "feat: add authenticated IP APIs"
```

## Task 5: 实现首次设置、JWT 会话与管理 API

**Files:**
- Create: `src/worker/middleware/admin-session.ts`, `src/worker/routes/setup-auth.ts`, `src/worker/routes/admin.ts`, `src/worker/services/session-service.ts`
- Create: `tests/worker/auth.test.ts`, `tests/worker/admin-api.test.ts`
- Modify: `src/worker/index.ts`, `src/worker/repositories/settings.ts`

**Interfaces:**

```ts
export interface Env {
  DB: D1Database;
  JWT_SECRET: string;
}

export async function initializeAdmin(db: D1Database, password: string): Promise<boolean>;
export async function createSession(secret: string): Promise<string>;
export async function verifySession(secret: string, token: string): Promise<boolean>;
```

- [ ] **Step 1: 先写首次设置与会话测试**

测试无密码时允许初始化、初始化成功后第二次返回冲突、明文密码可登录、错误密码返回 401、签发的 JWT 能访问管理 API、失效/过期 JWT 被拒绝、退出清除 Cookie。

```ts
expect(await initializeAdmin(env.DB, "admin-example-password")).toBe(true);
expect(await initializeAdmin(env.DB, "another-password")).toBe(false);
```

- [ ] **Step 2: 实现一次性初始化**

`GET /api/setup/status` 只返回是否初始化，不返回密码。`POST /api/setup` 拒绝空密码；用 `UPDATE ... WHERE admin_password IS NULL` 确保首次设置只成功一次；成功后不再开放初始化。

- [ ] **Step 3: 实现登录 Cookie 与会话中间件**

用 `jose` 的 HS256 API 从 `JWT_SECRET` 签发/验证 JWT，payload 仅含固定管理员主体和 `iat`/`exp`；Cookie 设置 `HttpOnly`、`Secure`、`SameSite=Lax`、`Path=/`、7 天有效。不得把密码或 Token 放进 JWT。

- [ ] **Step 4: 实现管理 API**

定义并添加以下认证保护的路由：`GET /api/admin/records?page=1&pageSize=20`、`POST /api/admin/records`、`PUT /api/admin/records/:ip`、`DELETE /api/admin/records/:ip`、`PUT /api/admin/asns/:asn/alias`、`DELETE /api/admin/asns/:asn/alias`、`GET/PUT /api/admin/token`、`POST /api/admin/token/generate`。记录 body 与公开接口字段一致；别名 body 为 `{ "alias": "DMIT" }`；Token body 为 `{ "token": "..." }`。所有 D1 写入使用参数化语句；API Token 只能由已登录管理会话读取。

- [ ] **Step 5: 运行认证与后台 API 测试**

```bash
pnpm test:worker -- tests/worker/auth.test.ts tests/worker/admin-api.test.ts
```

预期：首次设置、会话隔离和全部管理 API 用例通过。

- [ ] **Step 6: 提交后台服务**

```bash
git add src/worker tests/worker
git commit -m "feat: add admin sessions and management API"
```

## Task 6: 实现 React 管理界面

**Files:**
- Create: `src/client/api.ts`, `src/client/components/SetupForm.tsx`, `src/client/components/LoginForm.tsx`, `src/client/components/IpTable.tsx`, `src/client/components/IpEditor.tsx`, `src/client/components/TokenSettings.tsx`, `src/client/components/AliasEditor.tsx`, `tests/client/App.test.tsx`
- Modify: `src/client/App.tsx`, `src/client/styles.css`

**Interfaces:**

```ts
export interface ClientApi {
  getSetupStatus(): Promise<{ initialized: boolean }>;
  login(password: string): Promise<void>;
  logout(): Promise<void>;
  listRecords(page: number, pageSize: 20 | 50 | 100): Promise<{ rows: IpRecord[]; total: number }>;
  saveRecord(record: IpRecord): Promise<void>;
  deleteRecord(ip: string): Promise<void>;
  setAsnAlias(asn: number, alias: string | null): Promise<void>;
  getToken(): Promise<{ token: string | null }>;
  setToken(token: string): Promise<void>;
}
```

- [ ] **Step 1: 先写页面交互测试**

用 jsdom + Testing Library 测试初始化/登录状态切换、每页选择器的三个选项、单页表格渲染、下一页加载、编辑保存、删除确认、别名和 Token 设置面板。

```tsx
render(<App api={fakeApi} />);
expect(await screen.findByRole("heading", { name: "初始化管理员密码" })).toBeTruthy();
```

- [ ] **Step 2: 运行 UI 测试确认失败**

```bash
pnpm test:client -- tests/client/App.test.tsx
```

预期：页面或操作不存在导致测试失败。

- [ ] **Step 3: 实现同源 API 客户端和初始化/登录页面**

所有请求使用相对路径，Fetch 明确启用同源 Cookie；401 时回到登录状态；设置/登录表单不在页面中回显密码。

- [ ] **Step 4: 实现当前页 IP 表格和编辑/删除操作**

默认 pageSize 20；选择器仅允许 20、50、100；切页或改变 pageSize 后请求对应一页，不在前端预载其他页；编辑表单只含定义字段；删除要求确认。

- [ ] **Step 5: 实现 ASN 别名与 Token 设置面板**

别名按 ASN 保存并立即刷新列表；Token 支持手动设置、生成和明文查看/复制；Token 更新成功后清空表单中的旧值并显示服务器当前值。

- [ ] **Step 6: 运行 UI 测试和完整构建**

```bash
pnpm test:client -- tests/client
pnpm build
```

预期：UI 交互用例通过，Worker 与 SPA 一起构建成功。

- [ ] **Step 7: 提交管理界面**

```bash
git add src/client tests/client
git commit -m "feat: add paginated IP admin UI"
```

## Task 7: 配置测试总入口、部署文档并做本地验收

**Files:**
- Create: `README.md`, `.dev.vars.example`
- Modify: `.gitignore`, `package.json`, `vitest.config.ts`, `wrangler.jsonc`, `tests/worker/apply-migrations.ts`

- [ ] **Step 1: 配置 Worker Vitest 与 D1 migrations**

使用 `@cloudflare/vitest-plugin` 的 `cloudflareTest()` 读取 `wrangler.jsonc`；在插件的异步配置中通过 `readD1Migrations("migrations")` 注入 `TEST_MIGRATIONS`；Worker setup 使用 `applyD1Migrations(env.DB, env.TEST_MIGRATIONS)`。Client 测试配置使用 jsdom，两个测试项目由独立脚本执行。

```ts
import { cloudflareTest, readD1Migrations } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    cloudflareTest(async () => ({
      wrangler: { configPath: "./wrangler.jsonc" },
      miniflare: { bindings: { TEST_MIGRATIONS: await readD1Migrations("migrations") } },
    })),
  ],
  test: { setupFiles: ["./tests/worker/apply-migrations.ts"] },
});
```

- [ ] **Step 2: 补充本地变量模板并阻止凭据进入 Git**

`.dev.vars.example` 只写 `JWT_SECRET=replace-with-a-random-local-secret` 示例；`.gitignore` 排除 `.dev.vars`、`.wrangler/`、构建产物和测试覆盖率目录。绝不创建或提交真实密钥。

- [ ] **Step 3: 编写 README 操作步骤**

说明安装 pnpm 依赖、本地 `JWT_SECRET` 设置、`pnpm dev`、本地 D1 migration、前端首次设置密码、后台设置 Token、接口 curl 示例、生产创建 D1 和配置 Worker Secret 的步骤。明确管理员密码和 API Token 明文存储风险。部署命令只作为文档示例，不在本地验收中执行。

- [ ] **Step 4: 运行完整自动化验证**

```bash
pnpm test
pnpm typecheck
pnpm build
pnpm exec wrangler d1 migrations apply cf-san-ip --local
```

所有命令必须成功。检查 `git status` 不含 `.dev.vars`、`node_modules`、`.wrangler` 或构建产物。

- [ ] **Step 5: 本地端到端 smoke test**

运行 `pnpm dev` 后逐项执行以下命令；各请求只连接本地 D1。首次设置与登录应为 `200`，无 Token 的公开请求应为 `401`，有效批次部分保存应返回一项成功和一项错误，GET body 应为纯文本。

```bash
curl -fsS http://127.0.0.1:5173/api/setup/status
curl -i -c /tmp/cf-san-ip.cookies -H "Content-Type: application/json" --data '{"password":"local-only-password"}' http://127.0.0.1:5173/api/setup
curl -i -c /tmp/cf-san-ip.cookies -b /tmp/cf-san-ip.cookies -H "Content-Type: application/json" --data '{"password":"local-only-password"}' http://127.0.0.1:5173/api/auth/login
curl -i -b /tmp/cf-san-ip.cookies -H "Content-Type: application/json" -X PUT --data '{"token":"local-only-token"}' http://127.0.0.1:5173/api/admin/token
curl -i -H "Content-Type: application/json" -H "Authorization: Bearer local-only-token" --data '{"ips":[{"ip":"192.0.2.10","port":443,"asn":906,"region":"US"},{"ip":"invalid","port":0,"asn":906,"region":"US"}]}' http://127.0.0.1:5173/postip
curl -i -H "Authorization: Bearer local-only-token" "http://127.0.0.1:5173/getip?asn=906&limit=10"
curl -i -b /tmp/cf-san-ip.cookies "http://127.0.0.1:5173/api/admin/records?page=1&pageSize=20"
```

- [ ] **Step 6: 提交文档与最终配置**

```bash
git add README.md .dev.vars.example .gitignore package.json pnpm-lock.yaml wrangler.jsonc vitest.config.ts tests
git commit -m "docs: add local setup and deployment guide"
```

## 计划自检

- Spec 第 2 节架构：Task 1、Task 7。
- Spec 第 3 节数据模型与别名传播：Task 3、Task 4、Task 5、Task 6。
- Spec 第 4 节公开 API、逐条校验、随机筛选和输出格式：Task 2、Task 4。
- Spec 第 5 节首次设置、JWT、后台 CRUD、分页、Token 管理：Task 5、Task 6。
- Spec 第 6 节明文凭据风险与 Secret 边界：Task 5、Task 7。
- Spec 第 7 节自动化测试、本地 migration 和验收：所有任务，尤其 Task 7。
- 同批重复 IP、无别名继承、limit 上限、空查询结果和单项 `01` 均有实现步骤和测试覆盖。
