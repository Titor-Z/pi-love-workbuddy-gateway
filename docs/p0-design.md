# P0 设计 — 网关 / 客户端双模式（局域网多设备共享一个账号）

> 状态：设计稿，未开工。不含代码。
> 讨论 ID：`DISC-20261014-2400`
> 版本计划：步 0 单独发 `v0.3.0`（控制台分离，破坏性变更）；步 1–9 发 `v0.4.0`。

---

## 0. 范围与硬边界

### 进 P0

| 项 | 内容 |
|---|---|
| 模式 | `-mode gateway`（默认）/ `client` |
| 控制台分离 | 独立端口，只听 `127.0.0.1` |
| 设备 ID | 首次启动生成，长期持久化，双模式共用 |
| 配对鉴权 | PSK，网关生成并持久化，客户端填入后存本地 |
| 传输 | 自签 TLS（relay 端口），客户端 TOFU 指纹确认一次 |
| 转发 | 客户端把 `/v1/chat/completions`、`/v1/models` 转给网关 |
| 日志分桶 | `Record.Device`，网关按设备分桶与统计 |
| 并发控制 | 网关侧全局闸门，超限排队、超时 503 |
| 破坏性变更文档 | CHANGELOG / README / 迁移说明 |

### 明确不在 P0

- 凭证同步、版本号、冷/热仲裁（P1/P2）
- 客户端模式本地刷新/回写凭证（**禁止**）
- 网关离线自动切换
- `/relay/v1/billing`（客户端面板余额，P1）
- 客户端独立并发闸门（由网关统一裁决）

### P0 后的真实能力

在家多设备共享一个 WorkBuddy 账号；**外出仍不可用**（客户端手上没有凭证）。
这一条必须原样写进 CHANGELOG，不许含糊成「支持多设备」。

---

## 1. 端口模型（三端口）

| 端口 | 默认 | 协议 | 面向 | 内容 |
|---|---|---|---|---|
| API | `-addr 127.0.0.1:8788` | HTTP | 本机 pi | `/v1/*`、`/health` |
| 控制台 | `-console-addr 127.0.0.1:8789` | HTTP | 本机浏览器 | `/`、`/api/*` |
| Relay | `-relay-addr 0.0.0.0:8790` | HTTPS(TLS) | 局域网客户端 | 认证 + 设备标注 + 反代 |

**为什么不做单端口 HTTP/HTTPS 嗅探**：首字节分派是自造协议，SSE 长连接下一旦分派错就是极难复现的故障；且 pi 侧配的是 `http://127.0.0.1:8788`，端口变 HTTPS 意味着 `~/.pi/agent/models.json` 也要改成 https + 忽略证书，违背「pi 侧不受影响」。

**`-addr` 上 `/` 的行为**：返回纯文本提示，写明控制台已迁到 `-console-addr`。不静默 404（升级后用户会以为服务坏了）。

**relay 默认关闭**（拍板）：控制台里手动开启。P0 多设备共享是新增功能，不是所有人都需要；默认开会让每台升级的网关都多一个 `0.0.0.0:8790` 监听口，对单机用户是不必要的暴露。

**控制台显式设 `0.0.0.0`**：允许，但启动日志打警告。风险写进 CHANGELOG（见 §7）。

---

## 2. 模块划分

### 2.1 `internal/appdir`（扩展，不新建包）

新增路径常量。所有新文件一律走这里（AGENTS §5.7 铁律：业务包不许自己拼路径）。

```go
DevicePath()      // device.json    —— 本设备 ID + 名称
PSKPath()         // psk.txt        —— 网关 PSK，0600
RelayCertPath()   // relay-cert.pem
RelayKeyPath()    // relay-key.pem，0600
DevicesPath()     // devices.json   —— 网关侧设备注册表
ClientPath()      // client.json    —— 客户端配对配置，0600
ClientJournalName // client.jsonl   —— 客户端模式默认事件文件名（见 §2.8）
```

### 2.2 `internal/identity` — 设备 ID、PSK、请求上下文

```go
// Device 是本设备的长期身份。与运行模式无关：
// 同一台机器切 gateway / client 用的是同一个 ID，否则网关注册表会分裂出两个身份。
type Device struct {
    ID   string `json:"id"`   // 16 字节随机，hex
    Name string `json:"name"` // 默认 hostname，可在控制台改
}

func LoadDevice(path string) (*Device, error)  // 不存在则生成并落盘
func NewPSK() (string, error)                  // 32 字节随机，base64url
func LoadOrCreatePSK(path string) (string, error)

// 请求上下文：relay 中间件注入，网关 handler 读取。
func WithDevice(ctx context.Context, id string) context.Context
func DeviceFrom(ctx context.Context) string
func MarkRelay(ctx context.Context) context.Context  // 跳过 -api-key 校验
func FromRelay(ctx context.Context) bool
```

### 2.3 `internal/relaytls` — 证书与指纹

```go
func LoadOrCreate(certPath, keyPath string) (tls.Certificate, string, error)
// 第二个返回值：SHA-256(DER) 的 hex 指纹，供控制台展示、客户端确认

func DialerConfig(pinned string) *tls.Config
// InsecureSkipVerify = true + VerifyPeerCertificate 比对指纹
// 指纹比对必须 subtle.ConstantTimeCompare，不用 ==

func ProbeFingerprint(ctx context.Context, addr string) (string, error)
// 只为取指纹而连一次：不校验、不发业务数据、不落信任
```

- ECDSA P-256 自签，`IsCA=true`，有效期 10 年；重生成只要删文件。
- **不走系统信任链**：局域网自签没有可签发的 CA，指纹钉扎才是真约束。
- 客户端 `http.Transport` **必须关 HTTP/2**（`TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}`）。h2 分帧会缓冲 SSE，表现为「客户端模式流式卡住、最后一次性吐完」。这条是必踩的坑。

### 2.4 `internal/gateway` — 后端抽象（行为零变化）

```go
// Backend 是网关取数据的地方。
//   gateway 模式：*upstream.Client（直连 WorkBuddy）
//   client  模式：*relay.Client（转发到网关）
type Backend interface {
    ChatStream(ctx context.Context, body []byte) (*http.Response, error)
    Models(ctx context.Context) ([]upstream.ModelInfo, error)
}
```

`Server.up` 类型从 `*upstream.Client` 改为 `Backend`。两个实现天然满足，**不写适配器**。

`Server` 新增字段：`gate *Gate`、`devices`（可选，仅用于补名字）。

### 2.5 Gate 的位置（关键决策）

```
                ┌─── API mux (127.0.0.1:8788) ─────────┐
                │                                       │
                │      同一个 gatewayServer.Handler()    │
                │      ├─ handleChat: Gate.Acquire      │
                │      ├─ journal（含 device）           │
                │      ├─ blocked 判定 / observer        │
                │      └─ catalog 促销标注               │
                │                                       │
relay mux ──────┴─ PSK → 吊销 → device 入 ctx ───────────┘
(0.0.0.0:8790, TLS)
```

- **`Gate` 是 `*gateway.Server` 的字段，挂在 `handleChat` 入口**，两个入口共用同一个实例。
- **relay 不开第二条请求管线**：relay mux 把 `/v1/*` 挂到**同一个** `gatewayServer.Handler()`（同进程直挂，不做 HTTP 反代，省一跳也省一次缓冲）。
- 因此并发控制、journal、`blocked`、促销标注、observer 解析**自动一致**，不存在绕过，也不会日后漂移。
- **否决「Gate 放 Backend 层」**：client 模式的 Backend 就是 `relay.Client`，会被二次限流（客户端闸门 + 网关闸门），表现为莫名其妙的 503。
- **必须配套**：relay 请求跳过 `-api-key` 校验（`identity.FromRelay`），PSK 已鉴过权；否则配了 api-key 的用户客户端全体 401。

```go
// Gate 是全局并发闸门。n<=0 表示不限。
type Gate struct{ sem chan struct{} }
func New(n int) *Gate
func (g *Gate) Acquire(ctx context.Context) (release func(), err error)
```

- 等待上限 60s（或 ctx 先结束）→ `503` + `Retry-After: 5`，消息写明「网关并发已满，请稍后重试」。**不要让它落到后端去随机 429**。
- **只在 gateway 模式生效**；client 模式强制不限（网关已裁过，客户端再限只会让人以为网关坏了）。
- 默认 `-max-concurrent 4`。多设备共享一个账号，并发翻倍是必然（已知后端 429）。

### 2.6 `internal/relay` — 转发协议

```go
const (
    HeaderPSK        = "X-Plwg-Psk"
    HeaderDevice     = "X-Plwg-Device"
    HeaderDeviceName = "X-Plwg-Device-Name"
)

// 服务端（gateway 模式挂 relay 端口）
func NewServer(deps Deps) *Server
type Deps struct {
    Backend  http.Handler       // 就是 gatewayServer.Handler()
    Devices  *devices.Registry
    PSK      string
    Logger   *logging.Logger
}
// 路由：
//   POST /v1/chat/completions  —— 交给 Backend（同进程）
//   GET  /v1/models            —— 同上
//   GET  /relay/v1/ping        —— {ok, version, mode, device_count, tls_fingerprint}

// 客户端（client 模式实现 gateway.Backend）
func NewClient(c ClientConfig) *Client
type ClientConfig struct {
    BaseURL    string // https://host:8790
    PSK        string
    Device     identity.Device
    Fingerprint string // 空 = 未确认，需 TOFU
}
```

- **请求体原样转发，不解析、不改写**。`developer→system` 等改写已在客户端本地 `gateway.Server` 做过，relay 再做一遍是重复且危险的。
- 响应侧：删 `Content-Length`、置 `X-Accel-Buffering: no`、`io.Copy` + `http.Flusher` 周期 flush，与 `upstream` 透传同一套写法。
- 鉴权顺序：PSK 缺失/错误 → 401；设备已吊销 → 403；通过 → `Devices.Observe(id, name)` 后放行。
- **PSK 比对用 `subtle.ConstantTimeCompare`**，不做字符串 `==`。
- **relay 请求必须跳过 `-api-key` 校验**（`identity.FromRelay` 标记，PSK 已鉴过权），否则配了 `-api-key` 的用户客户端全体 401。

#### 2.6.1 两个端口的鉴权是**独立的两套**，不是一套的两个入口

| 端口 | 鉴权手段 | 保护对象 |
|---|---|---|
| API `127.0.0.1:8788` | `-api-key`（留空则不校验） | 本机 pi 的直连接入 |
| Relay `0.0.0.0:8790` | PSK + TLS 指纹钉扎 + 设备吊销 | 局域网客户端接入 |

**要明确写清、避免误判的三条**：

1. `-api-key` **只保护 8788，对 relay 零作用**。配置了 api-key 不等于 relay 被保护了。
2. relay 的 `-api-key` 校验是**显式跳过**的，不是「没配 api-key 所以恰好通过」——这是代码里的一个分支，不是默认行为。
3. 想让 relay 不可用，唯一办法是**不开 relay**（默认就是关的）。PSK 重置能踢掉当前所有客户端，但端口仍在监听并接受新配对尝试。

这条要同时写进控制台文案（relay 面板上标注「本机密钥不适用于此端口」）与 CHANGELOG。

**连接超时**：`http.Client` 整体不设 `Timeout`（流式，由 ctx 控制），但必须设：

```go
DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext
TLSHandshakeTimeout: 5 * time.Second
ResponseHeaderTimeout: 30 * time.Second  // 首字节上限；可调
```

否则网关离线时客户端会挂很久才报错，且「网关在线、后端卡住」也报不出来。

### 2.7 `internal/devices` — 设备注册表（gateway 侧）

```go
type Entry struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    FirstSeen time.Time `json:"first_seen"`
    LastSeen  time.Time `json:"last_seen"`
    Revoked   bool      `json:"revoked"`
}

func Open(path string) (*Registry, error)
func (r *Registry) Observe(id, name string)   // 首次见自动登记，之后只更新 LastSeen
func (r *Registry) IsRevoked(id string) bool
func (r *Registry) List() []Entry
func (r *Registry) Revoke(id string) error
```

- 观测而非预登记：PSK 是准入，设备 ID 是身份与审计。用户不必先加白名单。
- 落盘节流：`LastSeen` 每 30 秒最多刷一次，否则每个请求都写文件。

### 2.8 `internal/journal` — 设备分桶与并发安全

```go
// Device 是发起本次请求的设备 ID。本机直连（pi 直连网关）为空。
Device string `json:"device,omitempty"`
```

- 网关侧 relay 入口从 ctx 取（不是从 header 直接读——relay 中间件已注入 ctx）；本机直连留空，面板显示「本机」。
- 面板聚合新增 `by_device` 分组与 `?device=` 筛选；设备名由 registry 的 `id→name` 映射补全。**日志只存 ID 不存名字**（名字会改，存了就是过期数据）。
- **并发安全（现状已满足，不要重复造）**：`Journal.Append` 全程持 `j.mu`，两个 goroutine 不可能同时 `Write`，所以不会出现半行交错。多设备并发写是同一进程内的事，**已有锁足够，无需文件锁或单写 goroutine**。
- **真正要补的是多进程**：同一台机器上 gateway 与 client 模式同时跑时，默认事件文件是同一个路径（`gateway.jsonl`），跨进程就没有这把锁了。→ **client 模式默认事件文件名改为 `client.jsonl`**，从根上避开（行为说明见 §2.8.1）。

#### 2.8.1 模式切换后的事件文件行为（明确，不做迁移）

- gateway 模式写 `gateway.jsonl`；client 模式写 `client.jsonl`。两者互不干扰，可同时存在。
- **切换模式后：新记录写新文件，旧文件原样保留，不迁移、不合并、不改名。**
- 因此面板在 client 模式下读到的是**切换之后**的记录（P0 客户端不存凭证，面板就是本机请求记录，切模式后看新文件即可）。
- **不提供「查看另一个模式的旧记录」**。理由：两个文件的语义不同（一个是全网含设备分桶，一个是本机），合并展示会让人误读「哪个请求来自哪台设备」。要查旧记录直接读文件路径，面板状态页会列出两个路径。
- 这条要写进 CHANGELOG，避免用户切模式后以为「记录丢了」。
- 显式传 `-journal` 指向同一文件的用户后果自负（跨进程写同一文件），启动时若检测到同址已有另一个进程在写，仅打一行警告，不阻止。

### 2.9 `internal/config` — 新增参数

```go
Mode          string // gateway | client，默认 gateway
ConsoleAddr   string // 默认 127.0.0.1:8789
RelayAddr     string // 默认 0.0.0.0:8790
RelayEnabled  bool   // 默认 false（拍板：控制台手动开）；仅 gateway 模式
PSK           string // 留空则生成并持久化
MaxConcurrent int    // 默认 4，0=不限
GatewayURL    string // client 模式：https://host:8790
DeviceName    string // 默认 hostname
```

client 模式**忽略** `-auth-file` / `-at-rest-secret` / `-relay-*` / `-psk`，并且**不调用 `buildCredential`**。硬约束：P0 的客户端手上不该有凭证。

### 2.10 `internal/panel` — 控制台适配

`Deps` 新增：`Mode`、`ConsoleAddr`、`RelayAddr`、`TLSFingerprint`、`Device`、`Devices *devices.Registry`、`PSK`。

- `GET /api/status` 增：`mode` / `console_addr` / `relay_addr` / `relay_enabled` / `tls_fingerprint` / `device_id` / `device_name`。**绝不返回 PSK 明文**，只给「已配置/未配置」。
- 新增：`GET /api/devices`、`POST /api/devices/revoke`、`POST /api/relay/psk`（重置 PSK）、`POST /api/relay/enable`（手动开）。
- **pi 的 `baseUrl` 继续用 `-addr`（API 端口）**，不用 console 端口 —— 这是「pi 侧不受影响」的关键。
- client 模式控制台：显示「网关地址 / 在线状态 / 本设备 ID」；余额显示 P1 再补。
- 网关离线提示位 P0 埋好，文案「网关离线，客户端模式当前无法独立工作（离线凭证同步见后续版本）」，**不放自动切换按钮**。

---

## 3. 三个必须写清的流程

### 3.1 TOFU 指纹确认与变更

1. 客户端首次填网关地址 → 调 `ProbeFingerprint` 拿指纹（**不校验、不发业务数据、不落信任**）。
2. 控制台展示指纹 + 网关地址，用户点「确认并保存」→ 写入 `client.json` 的 `fingerprint`。
3. 之后每次连接严格比对。
4. **指纹不匹配 → 报错「网关证书已变化（期望 X，实际 Y），请重新确认指纹」，绝不静默接受、绝不降级为不校验**。
5. 无头机器：命令行 `-relay-trust <fp>` 直接注入。

证书 10 年有效，但用户删了 cert 重新生成就会走到第 4 步。这条流程是安全边界，不能省。

### 3.2 PSK 重置后的客户端恢复

1. 控制台重置 PSK → 覆写 `psk.txt` + 记一条 relay 事件。
2. 所有已配对客户端下次连接返 **401**。
3. 客户端面板提示「PSK 已失效，请重新填入」，并提供重新填入入口。
4. 重新填入后恢复；设备 ID 不变，历史日志不丢。

### 3.3 device_id 的安全定位（重要，别误会）

- `device_id` **由客户端自报，可伪造**。它不是安全机制。
- **安全边界是 PSK，只有 PSK**。

**吊销与重置的效力对比（必须写全）**：

| 手段 | 生效范围 | 能否被绕过 | 说明 |
|---|---|---|---|
| 吊销某个 device_id | 仅该 ID 的后续请求 | **能**——客户端改个 ID 再来即可 | 防君子。用于整理列表、暂时挡住某台误配置的机器，**不是安全机制** |
| 重置 PSK | **所有**已配对客户端 | 不能（新 PSK 未泄露时） | 真正有效的手段，但**无法只踢一台** |

**P0 阶段做不到「只踢一台」**，根因是 device_id 可伪造——任何基于自报身份的封禁都能换个 ID 绕过。这个限制要写进 CHANGELOG，别让用户以为「吊销」是安全机制。

控制台文案要求：
- 吊销按钮旁标注「仅阻止该设备 ID 继续请求；持有密钥的设备可换个 ID 重连」。
- 重置 PSK 按钮旁标注「将踢掉所有已配对设备，需逐个重新填入」。
- 后续若要做「只踢一台」，前提是**设备身份不可伪造**（每个设备配一把独立密钥或证书），那是 P2 之后的事，不在 P0 范围。

---

## 4. 落地顺序（严格按依赖）

| 步 | 内容 | 依赖 | 单独可验 |
|---|---|---|---|
| 0 | 控制台分离端口 + `/` 提示页 + 文档迁移说明 → **发 v0.3.0** | 无 | ✅ |
| 1 | `appdir` 扩展 + `identity`（设备 ID / PSK / ctx） | 无 | ✅ |
| 2 | `relaytls`（证书、指纹、Dialer、Probe） | 1 | ✅ |
| 3 | `gateway.Backend` 接口抽象 | 无 | ✅ 行为零变化 |
| 4 | `devices.Registry` | 1 | ✅ |
| 5 | `relay` 服务端（PSK + device + 挂同一个 gateway handler + ping） | 2,3,4 | ✅ |
| 6 | `relay` 客户端 + client 模式装配（main 分支） | 5 | ✅ 端到端 |
| 7 | `journal.Device` + 面板设备分桶 / 吊销 / PSK 重置 | 1,4 | ✅ |
| 8 | `Gate` + `-max-concurrent` | 3 | ✅ |
| 9 | CHANGELOG / README / 迁移说明 → 发 v0.4.0 | 全部 | — |

步 0 单独发版（拍板）：控制台分离是破坏性变更，单独一版让用户能立刻归因。

---

## 5. 验收标准（可测）

**控制台分离**
- API 端口请求 `/api/status` → 404（控制台不再挂在 API 端口）。
- API 端口 `/` → 200 文本，含控制台新地址。
- 控制台端口从另一台机器访问 → 连接被拒（只听 127.0.0.1）。
- `/health`、`/v1/models`、`POST /v1/chat/completions` 行为与分离前完全一致。

**设备 ID / 密钥文件权限**
- 设备 ID 首次生成，重启后不变；删文件才变。
- **`psk.txt` 与 `client.json` 权限均为 0600**（client.json 里存 PSK，权限必须同 PSK 文件）。
- 日志与 `/api/status` **绝不输出 PSK 明文**。
- 变异测试：把 0600 改成 0644，测试必须红。

**relay 鉴权（与 API 端口独立）**
- 无 PSK → 401；错 PSK → 401；对 PSK + 已吊销设备 → 403；正常 → 200。
- 错指纹 → 握手失败，报错含期望/实际指纹，**不降级接受**。
- **配置 `-api-key` 后，relay 端口不带 api-key 仍应放行**（PSK 已鉴权）；同时 API 端口不带 api-key 仍应 401。证明两套鉴权互不串扰。
- 变异测试：PSK 比对改 `==`、删掉吊销判断、**删掉 relay 跳过 api-key 的分支**，三条各自都必須让测试变红。

**并发闸门（两个入口共用）**
- `-max-concurrent 1`：本机 API 一个流式 + relay 一个流式 → 第二个排队，第一个结束才开始（**证明 relay 没绕过 Gate**）。
- 等待 60s 超时 → 503 + `Retry-After`，且**未**向后端发请求。
- `-max-concurrent 0` → 不限。

**超时**
- 网关离线（地址不可达）→ 客户端 **5s 内**报错，不挂起。
- 网关在线但后端卡住 → `ResponseHeaderTimeout` 触发报错。

**转发**
- 客户端非流式 → JSON 与直连一致。
- 客户端流式 → 首个 token 到达时间 ≤ 直连 + 200ms（验证没被缓冲；h2 关没关这条能测出来）。
- 工具调用分片、SSE 跨块边界与直连一致。
- 客户端断开 → 网关侧 ctx 取消，无 goroutine 泄漏。

**日志分桶**
- 两台客户端各发一次 → 网关 journal 两条 `device` 不同的记录。
- 面板按设备筛选正确；本机直连显示「本机」。
- 同机 gateway + client 同时跑 → 各写 `gateway.jsonl` / `client.jsonl`，无交错行。
- 切模式后旧文件保留：先以 gateway 记一条、切 client 再记一条 → 两个文件各自 1 条，**旧文件未被迁移或清空**。
- 变异测试：把 client 默认事件文件名改回 `gateway.jsonl`，测试必须红。

**通用**
- 全离线测试，`go test ./... -count=1` 通过；跨包覆盖率不低于当前 77.3%。
- 每条新判定至少做一次变异测试（AGENTS §4）。

---

## 6. 已拍板项

| # | 决定 |
|---|---|
| 1 | 三端口（API / 控制台 / relay）。单端口嗅探否决。 |
| 2 | **relay 默认关**，控制台手动开。 |
| 3 | `/relay/v1/billing` **不进 P0**，客户端余额放 P1。 |
| 4 | 步 0 单独发 `v0.3.0`。 |
| 5 | Gate 在 `handleChat` 入口，两个入口共用（relay 挂同一个 handler）。 |
| 6 | 两个端口的鉴权**彼此独立**：`-api-key` 只保护 8788，relay 靠 PSK + TLS 指纹 + 吊销。 |
| 7 | 事件文件按模式分开，切模式**不迁移旧文件**。 |
| 8 | 吊销是管理手段；P0 **做不到只踢一台**，真踢人只能重置 PSK。 |

---

## 7. 已知风险（写进 CHANGELOG，不藏）

- **控制台暴露风险**：`-console-addr 0.0.0.0:8789` 会把控制台开放到局域网 —— 请求日志（含被拦对话正文抽屉）、余额、模型列表全部暴露，且面板的停止/重启按钮能被别人按。页内 nonce + Origin 校验防的是**跨站脚本**，对「局域网另一台机器正常访问」完全无效。默认只听 `127.0.0.1`；远程访问必须在反代层加认证。
- **新增监听口**：开启 relay 后多一个 `0.0.0.0:8790`。有 PSK + TLS 指纹兜底，但仍是新攻击面，且 PSK 一旦泄露等于账号可被使用。
- **两个端口的鉴权是独立的两套**：`-api-key` 只保护本机 8788，**对 relay 零作用**。配置了 api-key 不代表 relay 被保护。想让 relay 不可用只能不开它（默认关）。
- **吊销不是安全机制**：device_id 由客户端自报、可伪造，换个 ID 就能绕过吊销。**P0 做不到只踢一台**——要踢就重置 PSK，代价是所有设备都要重新填入。
- **切模式后旧事件记录不在新面板里显示**：新记录写新文件（`gateway.jsonl` / `client.jsonl`），旧文件保留不迁移，需要的话直接读文件。这不是数据丢失。
- **PSK 重置无法只影响一台设备**：会踢掉全部已配对客户端。操作前控制台必须二次确认。
- **网关是单点**：网关挂了所有客户端一起不可用，P0 无兜底（这正是 P1 凭证冷同步要解决的）。
- **限流提前触发**：多设备共享一个账号并发翻倍，闸门只能排队，不能增加额度。
- **隐私前提变化**：客户端模式下请求正文会经过局域网（TLS 内），与「单机」不同。README 要补一句。
