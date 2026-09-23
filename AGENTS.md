# AGENTS.md — 开发方案与注意事项

本文件面向在此仓库工作的 AI 代理 / 开发者，记录项目方案、架构约束与开发注意事项。

---

## 一、项目定位

本地运行的 Go 程序，自带 Web 管理界面，通过真实浏览器驱动多个网页端 AI 平台，对外提供统一的 **OpenAI 兼容 API**。

核心优势：请求由真实浏览器发出，TLS 指纹、Cookie、行为特征与真人无异，封号风险远低于纯 HTTP 逆向；协议变化时只需更新选择器与监听的 URL，维护成本低。

---

## 二、整体架构

```
客户端 (Cherry Studio / Cursor / 任意 OpenAI SDK)
        │ OpenAI 兼容 API (SSE)
        ▼
Go 反向代理服务
  ├─ API 接入层        /v1/chat/completions
  ├─ 路由分发          模型 → 平台
  ├─ OpenAI 格式转换层  SSE 流式组装
  └─ BrowserPool (浏览器实例池)
       ├─ Platform Profile A (DeepSeek)
       ├─ Platform Profile B (千问)
       ├─ Platform Profile C (豆包)
       └─ ... (每平台/每账号独立 Profile)
        │ CDP
        ▼
嵌入式 Web 管理界面 (http://localhost:8080/admin)
  平台标签页 / 登录页 / 状态监控
```

启动流程：程序启动后打开本地管理页面，用户在页面中依次登录各平台；登录完成后 Cookie 自动保存在浏览器 Profile 中，后续 API 调用复用该会话。

---

## 三、支持平台矩阵（14+）

### 第一梯队（优先接入，已有成熟方案）

| 平台 | 网页端 | 逆向难度 | 关键风控 |
|------|--------|----------|----------|
| DeepSeek | chat.deepseek.com | 中 | PoW WASM 挑战、频率限制 |
| 通义千问 | chat.qwen.ai | 中 | Cookie-Jar 回放、v2 chat API |
| 豆包 | doubao.com/chat | 高 | a_bogus 签名、msToken、设备指纹 |
| Kimi | kimi.com | 低 | 官方 API 已兼容 OpenAI |
| 智谱清言 | chatglm.cn | 低 | 多 Base URL 切换 |
| 文心一言 | chat.baidu.com | 中 | 纯算法实现 |
| 腾讯元宝 | yuanbao.tencent.com | 中 | hy_user / hy_token 认证 |
| MiniMax（海螺） | hailuo.ai | 低 | 浏览器 Cookie 导入 |
| 讯飞星火 | xinghuo.xfyun.cn | 中 | WebSocket、cookie/fd/GtToken |

### 第二梯队（需更多适配）

| 平台 | 网页端 | 逆向难度 | 关键风控 |
|------|--------|----------|----------|
| Gemini | gemini.google.com | 中 | TLS 指纹、IP 限流 |
| Manus | manus.im | 高 | Connect RPC、hCaptcha |
| Perplexity | perplexity.ai | 中 | Cloudflare |

### 第三梯队（逆向风险极高，谨慎或仅用官方 API）

- **可灵 AI**：国产模型逆向风险极高，社区明确警告不要碰逆向接口。
- **即梦 AI**：同上，且网页版收费，逆向成本高。

---

## 四、技术选型

| 模块 | 选型 | 理由 |
|------|------|------|
| 浏览器控制 | `go-rod/rod` | 高层封装、零驱动、自动下载 Chromium、链式调用 |
| 反检测 | `go-rod/stealth` | 移除 `navigator.webdriver`、伪造 WebGL、修复插件数组 |
| TLS 指纹（HTTP 直连模式） | `bogdanfinn/tls-client` + `refraction-networking/utls` | 模拟 Chrome 146 真 TLS 握手，通过 JA3/JA4 |
| 网络拦截 | `rod.HijackRouter` | 基于 CDP Fetch 域 |
| SSE 流式捕获 | CDP `Network.dataReceived` | `getResponseBody` 对 SSE 无效 |
| API 格式 | OpenAI `chat.completion.chunk` | 兼容主流客户端 |
| 管理界面 | `net/http` + `embed` | 内嵌 HTML，单二进制部署 |
| 代理池 | 自定义 `http.Transport.Proxy` | 标准库仅支持单代理，需轮询 |

---

## 五、核心模块设计

### 5.1 统一平台驱动接口

```go
type PlatformDriver interface {
    Name() string
    LoginURL() string
    LoginCheck(page *rod.Page) (bool, error)
    SendMessage(page *rod.Page, text string) error
    StreamEndpoint() string                    // SSE 端点 URL 关键词
    Selectors() PlatformSelectors              // 输入框/发送按钮选择器
    ParseSSEChunk(raw []byte) (string, error)  // 解析各平台 SSE 格式
}
```

新增平台 = 新增一个实现该接口的文件 + 在平台配置表注册。

### 5.2 浏览器实例池（单浏览器 + 按需 Tab）

**全平台共用一个常驻无头浏览器进程**（省内存的关键，不要每平台各开一个进程）：

```go
type Pool struct {
    browser *rod.Browser            // 全平台唯一浏览器
    instances map[string]*Instance  // 每平台 = 一个会话句柄（不是进程）
}

type Instance struct {
    name   string
    driver platform.PlatformDriver
    page   *rod.Page   // 登录画面 Tab，仅「浏览器画面」打开期间存在
    logged *bool       // 登录态缓存
}
```

关键点：
- 唯一浏览器按需懒启动，`stealth.MustPage(browser)` 开每任务独立 **Tab**，用完即 `page.Close()`（释放渲染进程），不长久持有页面。
- Cookie 按域名在单 profile 里天然隔离：`launcher.New().UserDataDir(srv.ProfileDir)`。
- 登录入口唯一：管理界面「浏览器画面」(`Pool.OpenViewer` / `CloseView`)；无桌面环境全靠 CDP 截图+事件回灌。
- `Headless(true)` + `Set("--headless","new")`：new 模式才兼容 Termux 无 X 环境。
- 省内存参数（Termux chromium 实测，不许乱改）：
  - `--no-zygote` **必须开**：不开会拉起 5 个 zygote（每个最大 ~190MB）。
  - `--renderer-process-limit=1`：关 Tab 后最多保留 1 个渲染进程。
  - `--disable-gpu --disable-extensions --disable-sync --disable-background-networking --mute-audio --disable-dev-shm-usage --window-size=800,600`。
- 聊天 Tab 拦截 Image/Font/Media 请求（`Fetch` 域 `BlockedByClient`），登录检查临时 Tab 同样拦截。
- 内存阈值守护：单个浏览器 RSS 超 `maxBrowserRSS`(1.5G) 且无进行中对话 → 自动重启回收（`browserRSS` 扫 `/proc`）。
- headless=new 单浏览器 Termux 实测空闲 ~400-750MB（多浏览器会是 N 倍）。

### 5.3 SSE 流式响应捕获（最关键技术点）

CDP 的 `Network.getResponseBody` 对 `text/event-stream` 不生效，必须监听 `Network.dataReceived` 逐块拼接：

```go
for raw := range topic {
    cdpEvt := raw.(*cdp.Event)
    if cdpEvt.Method == "Network.dataReceived" {
        var e proto.NetworkDataReceived
        rod.Event(cdpEvt, &e)
        chunk := base64Decode(e.Data)
        if content, ok := pb.Driver.ParseSSEChunk(chunk); ok {
            replyCh <- content
        }
    }
}
```

### 5.4 OpenAI 兼容 SSE 输出

输出标准 `chat.completion.chunk` 事件，逐块 `Flush()`，最后发 `data: [DONE]`。

### 5.5 代理池与账号池

- 代理池：自定义 `Proxy(req) (*url.URL, error)`，用 `atomic` 轮询。
- 账号池：每平台多账号轮询，累计请求达阈值自动切换备用账号。

---

## 六、防封号策略（必须遵守）

### 6.1 身份伪装
- 必须使用 `stealth.MustPage(browser)`。
- 豆包等需 `a_bogus` 签名的平台，依赖前端 JS 拦截器自动注入，不要手写。
- HTTP 直连模式用 `bogdanfinn/tls-client` 对齐 Chrome 146 JA3/JA4。

### 6.2 行为拟人化
- 输入用 `input.MustType()`，不用 `MustInput`。
- 鼠标用 `page.Mouse.MustMove(x, y, steps)`。
- 请求间隔加 2~5 秒随机延迟。

### 6.3 资源隔离
- 全平台共用唯一无头浏览器 + 全局 profile（Cookie 按域名天然隔离，一处登录全局复用）。
- 每平台会话句柄互相独立（登录画面 Tab / 登录态缓存），互不干扰。
- 浏览器代理全局统一（`[server] proxy`）；如需换网络环境，改配置后浏览器空闲时自动重建。

### 6.4 频率控制
- 每平台最大并发 1，请求间隔 ≥ 2 秒。
- 累计请求达阈值（如 150 次）自动切换备用账号。
- 每个出口 IP 独立限流：并发 / RPM / RPH 三档。

---

## 七、项目结构

```
web2api-go/
├── main.go
├── go.mod
├── config.toml
├── AGENTS.md                  # 本文件：方案与注意事项
├── README.md                  # 面向用户的使用说明
├── api/
│   └── types.go               # OpenAI 兼容类型
├── internal/
│   ├── config/
│   │   └── config.go          # 根目录单份 config.toml 加载（[server]/[auth]/[platforms]）+ 文件 mtime 热加载
│   ├── browser/
│   │   ├── pool.go            # BrowserPool 管理 + CDP dataReceived 捕获 + 发消息编排
│   │   └── lifecycle.go       # 保活
│   ├── platform/
│   │   ├── driver.go          # PlatformDriver 接口
│   │   ├── config.go          # 驱动注册表
│   │   ├── parse.go           # 通用 SSE JSON 增量抽取
│   │   ├── deepseek.go
│   │   ├── kimi.go
│   │   └── qwen.go            # 其余平台按此模板扩展
│   ├── stream/
│   │   ├── capture.go         # SSE 帧解码 (data: 事件按行拆分)
│   │   └── openai.go          # OpenAI SSE 格式输出
│   ├── router/
│   │   └── router.go          # /v1/chat/completions、/v1/models、请求统计
│   └── admin/
│       ├── handler.go         # Web 管理界面 API
│       └── static/
│           └── index.html     # 平台状态 / 登录按钮页面
└── pkg/
    ├── rate/
    │   └── limiter.go         # 并发 / 最小间隔 / 日配额
    ├── proxy/
    │   └── pool.go            # 代理池（轮询）
    └── account/
        └── pool.go            # 账号池（轮询）
```

---

## 八、平台适配配置表（示例）

```go
var platformConfigs = map[string]PlatformConfig{
    "deepseek": {
        URL:       "https://chat.deepseek.com",
        InputSel:  "textarea#chat-input",
        SendSel:   "div[role='button'][aria-label='发送']",
        StreamURL: "/api/v0/chat/completion",
    },
    "qwen": {
        URL:       "https://chat.qwen.ai",
        InputSel:  "textarea[placeholder*='输入']",
        SendSel:   "button.send-btn",
        StreamURL: "/api/chat",
    },
    "doubao": {
        URL:       "https://www.doubao.com/chat",
        InputSel:  "textarea[data-testid='chat-input']",
        SendSel:   "button[data-testid='send-button']",
        StreamURL: "/api/v1/chat",
    },
    "kimi": {
        URL:       "https://kimi.com",
        InputSel:  "div[contenteditable='true']",
        SendSel:   "button[data-testid='send']",
        StreamURL: "/api/chat",
    },
    "glm": {
        URL:       "https://chatglm.cn",
        InputSel:  "textarea[placeholder*='输入']",
        SendSel:   "button[type='submit']",
        StreamURL: "/api/chat",
    },
    // ... 更多平台
}
```

> **注意**：以上选择器为示例，需实际登录后用 DevTools 检查确认。平台 UI 改版时只需更新此处。

---

## 九、部署与运维

- 单二进制：`go build -o web2api`，无额外依赖。
- 管理界面：`http://localhost:8080/admin`，显示各平台登录状态、今日请求次数、代理配置。
- 配置热加载：根目录 `config.toml` 保存即热加载。
- 日志：记录模型、平台、耗时、状态；**不记录** prompt 与回复内容。

---

## 十、风险与边界

### 技术风险
- 网页端协议随时可能变化，工具可能突然失效。
- 即使做足防封措施，封号风险依然存在。

### 法律边界
- **仅供个人临时使用，禁止对外提供服务或商用。**
- 上海已有“API 中转站”负责人因反向代理 + 账号池模式被刑事拘留的案例。
- 参考项目的免责声明明确提示“账号封禁、法律风险”自担。

### 使用建议
- 使用小号，不要用主账号。
- 低频使用，个人临时救急足够。
- 每平台日均请求控制在 100 次以内。
- 重要任务切回官方 API。

---

## 十一、开发注意事项（AI 代理必读）

1. **不要用 `browser.MustPage()`**，一律用 `stealth.MustPage(browser)`。
2. **SSE 捕获不要用 `getResponseBody`**，必须监听 `Network.dataReceived`。
3. **不要硬编码选择器到逻辑里**，统一放 `platform/config.go`，方便改版时更新。
4. **新增平台**：实现 `PlatformDriver` 接口 + 注册配置表 + 在 `internal/platform/` 加文件。
5. **不要在日志中输出 prompt / 回复内容**。
6. **不要提交 Cookie、Token、代理凭证等密钥**。
7. **注释仅在明确要求时添加**。
8. **代码风格**：遵循现有文件风格，新增依赖前先确认 `go.mod` 是否已有同类库。
9. **频率控制与账号切换逻辑**必须实现，不得绕过。
10. **每次请求之间必须有随机延迟**，不得裸发。
