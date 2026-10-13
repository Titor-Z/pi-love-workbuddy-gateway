# pi-love-workbuddy-gateway

**把 WorkBuddy（腾讯 CodeBuddy 国内版）的订阅额度，接给 pi 用。**

你订阅了 WorkBuddy，里面有额度、还有些限时免费的好模型。但它的桌面端太重了，
你只想在 pi 里用那些额度。这个小程序就干这一件事：在本机开一个接口，
让 pi 像用任何模型一样用 WorkBuddy 的模型。

一个文件，双击就能跑，不吃内存，不用装 Python，不用装 Node。

---

## 下载安装

到 [Releases](../../releases) 页面下载对应平台的压缩包，解压后只有一个可执行文件，
放到哪里都能跑。

| 平台 | 文件 |
|---|---|
| macOS（Apple 芯片） | `..._darwin_arm64.tar.gz` |
| macOS（Intel） | `..._darwin_amd64.tar.gz` |
| Windows | `..._windows_amd64.zip` |
| Linux | `..._linux_amd64.tar.gz` / `..._linux_arm64.tar.gz` |

每个压缩包都可以用同目录的 `checksums.txt` 校验完整性。

**macOS 首次运行被拦怎么办**

二进制没有做代码签名（签名证书要花钱，这个小工具不值当）。如果系统提示
「无法验证开发者」，在终端里执行一次即可：

```bash
xattr -d com.apple.quarantine ./pi-love-workbuddy-gateway
```

---

## 它解决什么问题

- **你不想开 WorkBuddy 桌面端。** 它只在你**第一次登录**时需要。登录完，令牌由本程序自己续期。
- **你想在 pi 里直接选那些模型。** 装好后 `pi` 的模型列表里会多出 31 个 WorkBuddy 模型。
- **你想一眼看出哪些免费。** 模型名后面直接标着费用，比如
  `Step-5-Preview · 免费 · 独家限免 · 至2026-10-25`。

---

## 快速开始

### 第一步：确认你已经在 WorkBuddy 桌面端登录过

只要你在这台电脑上装过并登录过 WorkBuddy，就够了。之后可以不再打开它。

> 程序需要一个小钥匙（登录态文件是加密的）来读你的登录信息。第一次运行它会自动
> 去 WorkBuddy 里取一次，之后就记在自己手里了。

### 第二步：跑起来

```bash
./pi-love-workbuddy-gateway
```

看到这几行就成功了：

```
登录态：/Users/你/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info
密钥指纹：9127dea1b44020a7
监听：http://127.0.0.1:8788
```

### 第三步：看看有哪些模型

```bash
./pi-love-workbuddy-gateway models
```

会打印一张表，带费用标记。想直接生成 pi 的配置：

```bash
./pi-love-workbuddy-gateway models -pi > ~/.pi/agent/models.json
```

### 第四步：在 pi 里用

打开 pi，输入 `/model`，会看到 `workbuddy` 这一组，选一个就行。

也可以直接指定：

```bash
pi --model workbuddy/step-5-preview "帮我看看这个项目"
```

---

## 常用命令

| 命令 | 作用 |
|---|---|
| `pi-love-workbuddy-gateway` | 启动网关（默认 `127.0.0.1:8788`） |
| `pi-love-workbuddy-gateway models` | 打印模型清单（带费用标记） |
| `pi-love-workbuddy-gateway models -pi` | 输出 pi 的 `models.json` |

### 可选参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-addr` | `127.0.0.1:8788` | 监听地址。只绑本机，外网访问不到 |
| `-api-key` | 空 | 填了之后，本地客户端必须带同一个密钥 |
| `-auth-file` | 自动探测 | 手动指定登录态文件 |
| `-at-rest-secret` | 自动获取 | 手动指定解密用的根密钥 |
| `-promotions` | 见下 | 手动指定促销配置文件 |
| `-log` | 空 | 把请求日志写进文件 |

---

## 模型名后面的标记怎么读

```
Hy3 · 免费 · 限时免费 · 至2026-10-31
│      │      │           └─ 免费到什么时候
│      │      └─ WorkBuddy 官方挂的活动名
│      └─ 免费 / 或者倍率
└─ 模型名
```

- **免费**：这个模型现在不收积分。
- **`0.29x`**：收积分，倍率 0.29。数字越小越便宜。
- 活动名和截止日期是 WorkBuddy 的活动，随时可能变。

---

## 配置文件

都放在 `~/.config/piloveworkbuddy/` 下面。

### `promotions.json`

**为什么需要它：** WorkBuddy 的接口只告诉你「这个模型现在免费」，**不告诉你免费到哪天**。
那个日期只写在它的桌面端界面上。所以我们自己记一份。

格式：

```json
{
  "hy3": {
    "free_until": "2026-10-31",
    "note": "每日赠送免费额度（7月6日-10月31日）"
  }
}
```

- 键是模型 ID（用 `models` 命令看到的那个）。
- `free_until` 会显示成「至2026-10-31」。
- `note` 只是给人看的备注，不显示在模型名里。
- **这个文件不存在也完全没问题**，只是不显示截止日期。

参考 `promotions.example.json`。

---

## 遇到问题

**提示找不到登录态文件**
WorkBuddy 桌面端没登录过。打开它登一次即可。

**返回 401**
两种可能：
1. 你启了 `-api-key`，但 pi 那边没填同一个。
2. WorkBuddy 那边的登录过期了。打开桌面端重新登录一次。

**回复很慢**
换个快点的模型试试，比如 `deepseek-v4-flash`。

**回复被拦了，说内容不合适**
这是 WorkBuddy 后端的内容审核。它有时候不是因为你问的问题敏感，而是 pi 自己的
提示词里有安全术语（`exploit`、`sandbox`、`credential` 这类）被误判。
日志里会记一条 `⚠️ 疑似内容审核拦截`，换个说法或者换个模型通常能过去。

**想确认它在干活**
浏览器打开 <http://127.0.0.1:8788/health>，会看到账号和令牌状态。

---

## 安全说明

- 只监听 `127.0.0.1`，外部网络访问不到。除非你主动改 `-addr`。
- **你的令牌不会被打印、不会被上传。** 程序只在本机读写那份登录态文件。
- 令牌过期时它**会自动帮你续期**，并把新令牌写回原文件——这样 WorkBuddy 桌面端
  下次打开还是登录状态。
- 唯一的例外：自动续期会调用 WorkBuddy 的官方接口，这是它本来就会做的事。

---

## 免责声明

本项目只用于个人学习与研究，与腾讯、WorkBuddy、CodeBuddy 无官方关联。
请仅在你合法拥有订阅的前提下使用，风险自负。

---

## 给开发者

```bash
go build -o pi-love-workbuddy-gateway .   # 编译
go vet ./...                              # 检查
```

- **零第三方依赖。** 只用 Go 标准库，`go.mod` 里没有 require。
- 代码分模块：`config` / `atrest` / `credential` / `upstream` / `catalog` / `gateway` / `logging` / `wbapi`。
- 开发约定、架构决策、踩过的坑都记在 `AGENTS.md`（**不提交到仓库**）。

### 它只连通三个后端接口

| 用途 | 接口 |
|---|---|
| 对话补全 | `POST /v2/chat/completions` |
| 令牌续期 | `POST /v2/plugin/auth/token/refresh` |
| 模型目录 | `GET /console/enterprises/personal/models` |

对话接口返回的本来就是标准 OpenAI SSE，所以网关是**字节透传**，不做协议翻译。
唯一的感觉不到的工作是：补鉴权头、把 pi 的 `developer` 角色改成后端认的 `system`、
非流式请求时把 SSE 聚合成一个 JSON。
