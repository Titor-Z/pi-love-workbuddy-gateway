# WorkBuddy 上游协议 —— 实测笔记与探查方法

> 来源讨论：`DISC-20261017-0300`。
>
> 本文只记**在某台机器上真打过后端、并有原始回显**的结论。
> 凡是转述别人的、或从字段名推测出来的，一律标「**未证实**」。
>
> 参考实现：`~/dsh-workbuddy-connect`（MIT，0.7.6）。它的价值是**方法**与
> **国际版的记录**，不是可以直接借的代码——它是 Electron 插件，我们是独立二进制。
>
> 配套阅读：`docs/workbuddy-checkin.md`（签到专项）。

---

## 0. 最重要的一条方法论：**哨兵法**

**要判断「某个参数是否真的被上游校验」，先发一个绝不可能的值。**

```
reasoning_effort = "probe_sentinel_abc123"
```

- 被拒 → 这个字段**真校验**，后续逐档测试才有意义。
- 被接受 → 这个字段**根本不校验**，那么「所有档位都 OK」是**假阳性**，
  不能推出「这些档位都可用」。

实测结果（同一账号，同一时刻）：

| 模型 | 发哨兵值 | 结论 |
|---|---|---|
| `deepseek-v4.1-flash` | 400 `11133` | 真校验 |
| `glm-5.3` | **200 OK** | **不校验** |
| `space-bunny` | **200 OK** | **不校验** |

这条如果不做，就会得出「`glm-5.3` 支持所有档位」这种错结论——
它只是**什么都不校验**而已。

> 同一个错误我们上一轮已经犯过一次：从 `streak` 接口的字段名推出「用补签卡补签」，
> 结果那个功能根本不存在。**字段存在 ≠ 功能存在；请求成功 ≠ 参数生效。**

---

## 1. 抓真实请求，不要推断

我一度靠读 pi-ai 源码推断「pi 会发什么」，得出过一个**错的**结论
（以为 `reasoning_effort=off` 影响每一次请求）。真正的做法是**抓包**。

```
# 1. 起一个透明代理，记录并把请求转给真网关
node proxy.mjs            # 监听 9913，转发到 127.0.0.1:8788

# 2. 把 models.json 的 baseUrl 临时指过去
# 3. 跑真 pi，然后看记录
pi -p --provider workbuddy --model glm-5.1 --thinking off "hi"
```

**踩到的坑（两层缓存，缺一不可）**：

1. pi 把 `~/.pi/agent/models.json` 合并进 **`~/.pi/agent/models-store.json`**。
   只改 `models.json` **不生效**——pi 读的是 store。要验证时必须把 store 一起写。
2. `models-store.json` 里那份可能停留在很久以前（我们的 `checkedAt` 是两天前），
   所以「配置改了、行为没变」不一定是你改错了。

**诊断顺序**：先确认 store 里那份是什么 → 再看请求 → 最后才怀疑代码。

---

## 2. 模型目录：两个端点，两种 roster

### 2.1 `/console/enterprises/personal/models`（我们现在用的）

GET，凭证头即可（`Authorization` / `X-User-Id` / `X-Domain`，**不需要** Origin / Referer / 桌面 UA）。

返回结构：

```
data.models[]            32 行 —— 全量模型行（含非对话模型）
data.agents[]            各 agent 的 roster
  name == "cli"          只有 17 个 id ← 这份才是「能被 pi 用的」
  name == "general-purpose" 等   0 个模型
data.productFeatures / stagingEndpoint / endpoint / links / mergeStrategy
```

`data.models`（32）与 `cli` roster（17）的差集里，**有一半根本调不通**：

| 状态 | 模型 |
|---|---|
| 能调 | `hunyuan-chat`、`kimi-k2.5`、`deepseek-v4-flash` 等 |
| 400 `11102 model service info not found` | `glm-5.0`、`glm-4.7`、`glm-4.6`、`glm-4.6v`、`minimax-m2.5`、`kimi-k2-thinking`、`deepseek-v3-2-volc`、`hy4-preview-x` |
| 400 `11103` | `hunyuan-image-alpha`（不是对话模型） |

**推论**：直接拿 `data.models` 当列表会给出**一堆点了就报错**的模型。
参考实现的做法是**取 `cli` roster 与可用行的交集**——值得采纳。

### 2.2 `/v3/config`（另一份目录，**必须带桌面端 UA**）

| User-Agent | 结果 |
|---|---|
| 裸的（我们现在的 `codebuddy2openai/2.0`） | **400 `12403 check ua, get coding copilot version error`** |
| 浏览器 UA | 400 `12403` |
| `CLI/2.63.2 CodeBuddy/2.63.2` | 200，`cli` agent **18 个** |
| `WorkBuddy/5.5.6 WorkBuddy/5.5.6` | 200，`craft` agent **10 个** |

**同一条路径按 UA 分流给不同的 roster**（CLI 一份、桌面端一份）。
在 CN 上它是可用的，但**我们当前没有正当理由伪造桌面端 UA**去拿 `craft` 那份
（见 §6）。

---

## 3. `reasoning` 字段：两种形状，决定「能不能发」

原始行里 `reasoning` 有两种写法，**这是本次修 bug 的全部依据**：

```json
// 形状 A：只有默认档位（没有 supportedEfforts）
"reasoning": { "effort": "medium", "summary": "auto" }

// 形状 B：声明了可选档位
"reasoning": { "canDisableThinking": false, "defaultEffort": "high",
               "summary": "auto", "supportedEfforts": ["low", "high", "max"] }
```

实测出的行为（**同一账号、逐个模型打**）：

| 模型 | 形状 | 不发 `reasoning_effort` | 发 `"off"` |
|---|---|---|---|
| `glm-5.1` | A（`effort:medium`） | **400 `11150`** | 200 OK |
| `deepseek-v4.1-flash` | B（`[low,high,max]`） | 200 OK | **400 `11133`** |
| `deepseek-v4-pro` | A（`effort:high`） | OK | **400 `11133`** |
| `glm-5.3` / `space-bunny` / `hy3` / `step-5-preview` | B / A | OK | OK（**不校验**，哨兵法已证） |

**两个方向都不能裸奔**：

- `glm-5.1` 这类**不发就 400**（它只有唯一档位，后端要你显式给）。
- `deepseek` 系**发 `off` 就 400**。

`onlyReasoning` / `canDisableThinking` 这两个 flag **不能用来预测**：
`kimi-k2.8-preview` 与 `deepseek-v4.1-flash` 的 flag 完全相同
（`onlyReasoning:true` + `canDisableThinking:true`），但一个接受 `off`、一个拒绝。
**实测值只能一个模型一个模型打出来**。

### 3.1 顺带发现的其它坑

| 现象 | 结论 |
|---|---|
| `tool_choice: {"type":"auto"}` | 400 `11101` `cannot unmarshal object into field tool_choice of type string` —— 只认**字符串** |
| `tool_choice: "auto"` | 200 OK |
| `stream: false` | 400 `11101 Non-stream chat request is currently not supported` —— **必须流式** |
| `max_tokens: 4` | 会返回 `content: null`（思考占满了），不是错误，是预算太小 |

---

## 4. 错误码表（实测到的）

| code | HTTP | 含义 | 出处 |
|---|---|---|---|
| `10001` | 400 | 今天已签到（**是成功**） | 签到 |
| `11101` | 400 | 请求体不合法（`tool_choice` 形状、非流式） | 对话 |
| `11102` | 400 | `model service info not found` —— 模型不在可用 roster | 对话 |
| `11103` | 400 | 不是对话模型（`hunyuan-image-alpha`） | 对话 |
| `11128` | 4xx | 策略拦截 / 渠道校验（`developer` role 也会触发） | 对话 |
| `11133` | 400 | 参数被模型提供方拒绝（`off` 档位、超出范围的参数） | 对话 |
| `11150` | 400 | 思考档位不被当前模型支持（**少发/错发都会**） | 对话 |
| `12403` | 400 | UA 校验失败（`/v3/config` 缺桌面端 UA） | 目录 |
| `6004` | 429 | 频率限制 | 对话 |

**错误信封里还有 `displayMsg`**：

```json
{"code":11150,"msg":"the reasoning effort value is not supported ...",
 "displayMsg":{"en":"Reasoning effort not supported. Please adjust",
               "zh":"模型不支持该思考强度，请调整","zh-hant":"..."},
 "displayTips":{...}}
```

我们目前只读 `msg`（英文），面板上用户看到的是英文文案。
**`displayMsg.zh` 才是给中文用户看的**——这是一处明确的可改项。

---

## 5. pi 侧的语义（本次 bug 的另一半）

### 5.1 `thinkingLevelMap` 里**省略的键等于"支持"**

pi-ai 的 `getSupportedThinkingLevels`（`models.js:680`）：

```js
return EXTENDED_THINKING_LEVELS.filter((level) => {
  const mapped = model.thinkingLevelMap?.[level];
  if (mapped === null) return false;          // ← 只有显式 null 才排除
  if (level === "xhigh" || level === "max") return mapped !== undefined;
  return true;                                 // ← undefined 也算可用
});
```

所以「只写支持的档位」是**不够的**——省略掉的 `off` 仍是可用档位。

### 5.2 用户选 `off` 时，pi **什么都不发**

`openai-completions.js:525`：

```js
const reasoningEffort = clampedReasoning === "off" ? undefined : clampedReasoning;
```

以及 `:718`（`off` 但有映射字符串时才发）：

```js
else if (!options?.reasoningEffort && model.reasoning && compat.supportsReasoningEffort) {
  const offValue = model.thinkingLevelMap?.off;
  if (typeof offValue === "string") params.reasoning_effort = offValue;
}
```

两条合起来就是那个 400：`glm-5.1` + `--thinking off` → 不发 → `11150`。

### 5.3 抓包实测的最终对照表

| 配置 | `--thinking off` 实际发出 |
|---|---|
| `reasoning:true`，无 map | **不发** |
| `thinkingLevelMap.off = null` | `"low"`（clamp 到最近的可用档位） |
| `thinkingLevelMap.off = "off"` | **`"off"`** ← 唯一会发出 `off` 的情况 |
| 7 个键齐备（含 `off:null`） | 正常映射，**永不发裸 `off`** |

---

## 6. 我们**不**做的事（划清与参考实现的界线）

参考实现 `dsh-workbuddy-connect` 为了伪装成官方客户端做了大量工作，我们**不跟进**：

| 它做的 | 我们为什么不做 |
|---|---|
| 从 App bundle / `.plist` / Windows 注册表读真实版本，拼 `WorkBuddy/<v> <product>/<v> CLI/<v>` 的 UA | 我们的**定位就是不想装 WorkBuddy 客户端**（README 首条）。伪造它等于推翻项目存在的理由，也踩本项目从 v0.1.0 起就守住的那条线：**不伪装成官方客户端** |
| 拉起 Electron 二进制取 at-rest 密钥 | 我们已经在用「每台装一次、登录后即可卸载」+ 客户端自己那把密钥的兜底（`DISC-20261017-0100`） |
| 用 `craft` roster（桌面端 UA 的 `/v3/config`） | 见上一行，拿它就意味着伪造 UA |

**一句话**：它的 UA/roster 结论对我们有价值的是**「原来还有第二份目录」这个事实**，
不是「照着它的 UA 发」。真要拿证据，得先解决「我们凭什么去要桌面端那份」这个问题。

---

## 7. 未证实的条目（转述自参考实现，我们没账号/没环境）

以下**不要当作事实**，需要时先自己打一遍：

- 国际版 `www.workbuddy.ai` 的任何行为（我们只做国内版）。
- `X-Conversation-ID` / `X-Conversation-Request-ID` / `X-Conversation-Message-ID`
  三件套「自 2026-10-16 起国际版强制要求，缺了就 400 code 11128」。
- 企业账号走 `/v2/billing/meter/get-enterprise-user-usage`。
- `X-No-User-Id` / `X-No-Enterprise-Id` / `X-No-Department-Info` 的语义。
- `/v3/config` 在个人账号 / 企业账号下的 roster 差异（它记的是 18 vs 19）。

---

## 8. 本次据此修掉的 bug

**症状**：`pi --provider workbuddy --model glm-5.1 --thinking off` 返回
`400 code 11150 模型不支持该思考强度`。

**根因**：我们生成的 pi 配置里，模型只有 `reasoning: true`、**没有 `thinkingLevelMap`**。
pi 于是把 `off` 当成有效档位，用户选它时 pi 什么都不发，而后端 `glm-5.1`
恰恰是「不发就拒」。

**修法**（`internal/catalog/catalog.go` 的 `ThinkingLevelMap`）：

- **7 个档位键全部显式写出**，不支持的一律 `null`（利用 §5.1 的语义）。
- `off` / `minimal` 恒为 `null`——我们从不主动发这两个拼写。
- 有 `supportedEfforts` 就只映射集合内的；只有 `effort` 就只映射那一个值。
- 用户覆盖表逐键合并优先；全关掉时退回推导结果（避免全 null 又触发同一个 400）。

**验证**：抓真 pi 的请求，确认 `<absent>` 彻底消失——

| 模型 | `--thinking` 7 档依次发出 |
|---|---|
| `glm-5.1` | `medium medium medium medium medium medium medium` |
| `deepseek-v4.1-flash` | `low low high high max max low` |
| `hy3` | 全 `high` |

再真机端到端：`glm-5.1` 的 `off/low/medium/high` **四个档位全部 200 OK**，
且 `deepseek-v4.1-flash` / `deepseek-v4-pro` / `glm-5.3` / `space-bunny`
在 `--thinking off` 下也不再被拒。
