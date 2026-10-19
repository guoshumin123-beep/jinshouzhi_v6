# 交易所后端 v2 (backend_v2) 架构与开发指南

本文件是当前项目的全局架构宪法。在生成任何代码、设计任何模块前，必须严格遵守以下准则。

## 1. 核心架构铁律 (Core Architecture Rules)

### 1.1 唯一账本真相 (Single Source of Truth)
- **核心引擎**：必须从零设计一个 `internal/dealer` 模块，它是整个系统**唯一的资产扣减与账本引擎**。
- **强制约束**：所有的余额变动、开仓冻结、平仓结算、充提划转、以及现货与合约账户间的资金划转，**必须且只能**通过 `dealer` 下发原子事务。
- **红线**：绝对不允许任何业务逻辑、Handler 或脚本直接使用 SQL 修改数据库中的资产余额表。

### 1.2 合约双轨动态路由 (Dynamic Routing Gateway)
现货业务走标准流程，但**合约业务 (Derivatives)** 必须实现受 Admin 后台控制的动态路由分流。路由配置在**用户级别 (per user)** 生效，且**在某个持仓的整个生命周期内路由必须固化**（修改标签只对未来的新开仓生效）：
- **Standard 画像 (DMA 模式)**：用户的合约订单直接透传至火币 (HTX) API。**极其重要：每个 Standard 用户必须提取并使用其专属绑定的火币子账户 (Sub-account) API Key 发起请求，绝不能使用全局母账户统一下单。**
- **Synthetic 画像 (本地虚拟流动性模式)**：订单被网关拦截，走本地虚拟流动性池处理。
  - **定价裁判**：通过 WebSocket 抓取火币实时 Tick / 深度数据作为裁判价格。
  - **结算机制**：在本地引擎计算保证金与盈亏，平仓时直接调用本地 `internal/dealer` 更新账本。

### 1.3 生产级安全与防御 (Security & Resilience)
- **物理隔离**：Admin 管理后台路由组与 User 前台路由组必须严格隔离。子账户 API Key 绝不允许在 User 端接口或日志中暴露。
- **全局防崩溃**：所有 HTTP 请求 Handler 以及任何后台 Goroutine，必须统一包裹 `recover()` 中间件，严防 Panic 导致服务退出。
- **通信安全**：HTTP 和 WebSocket 强制采用 JWT 鉴权。WebSocket 必须按 `user_id` 进行频道隔离，严禁越权订阅。

## 2. 火币 (HTX) API 对接规范

- **文档来源**：官方最新接口文档地址为 `https://www.htx.com/zh-cn/opend/newApiPages/`。
- **防反爬约束**：该网页为动态渲染，不要尝试用脚本直接爬取该网址。在实现具体接口时，必须优先读取本地 `docs/htx_api/` 目录下的具体文档片段，或直接要求用户提供具体的请求/返回 JSON 示例。
- **轻量级 Client**：优先基于最新的 HTX API 规范（`HmacSHA256` 签名方法）封装轻量级、高并发的 HTTP/WebSocket Client，避免引入臃肿的官方 SDK。
- **基础域名**：
  - 现货 (Spot)：`https://api.huobi.pro`
  - 合约 (Swap/Futures)：`https://api.hbdm.com`

## 3. 目录结构规范 (Standard Go Layout)

项目严格遵循 Go 标准目录布局：
- `cmd/`：应用入口。
- `internal/`：私有核心业务逻辑。
  - `internal/dealer/`：核心账本与资金引擎。
  - `internal/gateway/`：核心路由与网关分发逻辑（Standard/Synthetic 分流）。
  - `internal/htx/`：火币轻量级客户端封装。
- `api/`：HTTP 路由、中间件与 Handler 定义。
- `docs/htx_api/`：存放从官网提取的火币接口局部 Markdown/JSON 片段。

## 4. AI 协作与工作流限制

- **先思考，后编码**：在引入重大新模块前，先输出设计草案和接口签名，等待用户确认后再生成实现代码。
- **按需索要数据**：发现 HTX API 缺少关键字段时，停止猜测，要求用户补充提供对应的接口文档片段。
- **代码整洁**：错误处理必须包含上下文，关键资金流转必须有结构化日志。
