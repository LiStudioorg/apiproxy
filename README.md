# web2api-go

> 通过真实浏览器驱动多个网页端 AI 平台，对外提供统一的 **OpenAI 兼容 API**。

一个本地运行的 Go 程序，自带 Web 管理界面。你用浏览器登录各平台后，任意支持 OpenAI SDK 的客户端（Cherry Studio、Cursor、各类 Chat 应用）都能直接调用这些平台，无需官方 API Key。

---

## 特性

- **真实浏览器请求**：TLS 指纹、Cookie、行为特征与真人一致，封号风险远低于纯 HTTP 逆向。
- **统一 OpenAI 接口**：`/v1/chat/completions`，支持 SSE 流式输出，兼容所有主流客户端。
- **多平台聚合**：一个程序管理十几个网页端 AI 平台。
- **Web 管理界面**：可视化登录、查看登录状态与请求统计。
- **单二进制部署**：`go build` 一次，拷贝即用。
- **易维护**：平台改版时，只改选择器和监听的 URL 即可。

---

## 快速开始

### 1. 环境要求

- Go 1.21+
- 可运行 Chromium 的系统（Linux / macOS / Windows）
- 网络能访问目标平台

### 2. 编译

```bash
go build -o web2api
```

### 3. 运行

```bash
./web2api                 # 默认加载 config.yaml
./web2api -config my.yaml # 指定配置文件
```

启动后打开管理界面：

```
http://localhost:8080/admin
```

### 4. 登录平台

在管理界面中，依次点击各平台标签页，在弹出的浏览器窗口中手动登录。登录状态会自动保存在浏览器 Profile 中，之后无需重复登录。

### 5. 使用 API

在任意 OpenAI 兼容客户端中配置：

| 项 | 值 |
|----|----|
| Base URL | `http://localhost:8080/v1` |
| API Key | 任意非空字符串（如 `sk-local`） |
| Model | 见下表 |

也可直接用 curl 测试：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

---

## 支持的平台

| 平台 | 模型名 | 状态 |
|------|--------|------|
| DeepSeek | `deepseek` | 已接入（选择器待实测） |
| Kimi | `kimi` | 已接入（选择器待实测） |
| 通义千问 | `qwen` | 已接入（选择器待实测） |
| 豆包 | `doubao` | 计划中 |
| 智谱清言 | `glm` | 计划中 |
| 文心一言 | `baidu` | 计划中 |
| 腾讯元宝 | `yuanbao` | 计划中 |
| MiniMax | `minimax` | 计划中 |
| 讯飞星火 | `spark` | 计划中 |
| Gemini | `gemini` | 计划中 |
| Manus | `manus` | 计划中 |
| Perplexity | `perplexity` | 计划中 |

> 模型名即平台标识，调用时填入 `model` 字段即可路由到对应平台。

---

## 配置

编辑 `config.yaml`：

```yaml
server:
  port: 8080
  admin_path: /admin

platforms:
  deepseek:
    enabled: true
    profile_dir: ./profiles/deepseek
    proxy: ""            # 可选，如 http://127.0.0.1:7890
    headless: false      # 无显示器环境（服务器/终端）请设为 true
    max_concurrency: 1
    min_interval: 2s
    request_limit: 150   # 达阈值自动切换备用账号

  qwen:
    enabled: true
    profile_dir: ./profiles/qwen
    proxy: ""
    headless: false
    max_concurrency: 1
    min_interval: 2s
    request_limit: 150
```

修改后保存即可生效（热加载）。

---

## API 说明

### `POST /v1/chat/completions`

请求体为标准 OpenAI Chat Completions 格式：

```json
{
  "model": "deepseek",
  "messages": [
    {"role": "system", "content": "你是一个助手"},
    {"role": "user", "content": "你好"}
  ],
  "stream": true
}
```

响应为 OpenAI 兼容的 SSE 流：

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"deepseek","choices":[{"index":0,"delta":{"content":"你"}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"deepseek","choices":[{"index":0,"delta":{"content":"好"}}]}

data: [DONE]
```

### `GET /v1/models`

列出当前可用的模型（平台）。

---

## 常见问题

**Q：必须一直开着浏览器窗口吗？**
A：程序以有头模式运行，浏览器窗口需要保持打开（可最小化）。这是为了维持会话与真实指纹。

**Q：登录会过期吗？**
A：各平台 Cookie 有效期不同，过期后重新在管理界面登录即可。

**Q：为什么每个平台要独立 Profile？**
A：隔离 Cookie 与会话，避免平台间互相污染，也便于多账号管理。

**Q：调用报错怎么办？**
A：平台网页端协议可能已改版。检查管理界面中的登录状态，或在 `platform/config.go` 中更新选择器与 StreamURL。

**Q：支持并发吗？**
A：每平台默认最大并发 1，请求间隔 ≥ 2 秒，这是为了降低封号风险，不建议调高。

---

## 注意事项与免责声明

> **请务必阅读以下内容。**

- 本项目**仅供个人学习与临时使用**，**禁止**对外提供服务或用于商业用途。
- 使用本工具存在**账号被封禁**的风险。请使用小号，不要使用主账号。
- 请**低频使用**，建议每个平台日均请求控制在 100 次以内。
- 重要任务请切回官方 API。
- 因使用本工具产生的任何后果（账号封禁、法律风险等），由使用者自行承担。
- 本项目不收集、不上传任何用户数据；所有 Cookie 与会话仅保存在本地。

---

## 开发

项目结构、技术选型、防封号策略与开发注意事项详见 [`AGENTS.md`](./AGENTS.md)。

---

## 许可

仅限个人学习使用。
