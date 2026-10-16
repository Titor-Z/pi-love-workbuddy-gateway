# WorkBuddy 签到与补签 —— 实测知识汇总

> 讨论来源：`DISC-20261017-0200`。
> 所有条目都是**在某台装了 WorkBuddy、登录态为网关凭证的机器上真机实测**的结果，
> 不是从客户端源码猜的、也不是从第三方项目抄的。凡未实测的都明确标注为「未证实」。

---

## 1. 签到接口（已跑通）

| 动作 | 请求 |
|---|---|
| 查状态 | `POST https://www.codebuddy.cn/v2/billing/meter/checkin-activity-status`，body `{}` |
| 签到 | `POST https://www.codebuddy.cn/v2/billing/meter/daily-checkin`，body `{}` |

请求头（实测足够，**不需要 Origin / Referer / X-Device-Token**）：

```
authorization: Bearer <accessToken>
content-type: application/json
x-client-platform: web
```

### 三条必须记住的坑

1. **路径必须带 `/v2/` 前缀。** 不带 v2 的 `/billing/meter/checkin-activity-status`
   虽然也返回 HTTP 200，但回的是**空壳**：`active:false`、`streak_days:0`、
   `checkin_dates:null`。空壳与「这个账号没签到活动」长得一模一样，
   照它判断会得到一个永远错的结论。
2. **不需要 Turing 反爬令牌。** 客户端 JS 里签到走
   `buildHeadersWithTuringToken()`，会带一个 `X-Device-Token` 头
   （缺失时带 `X-Device-Token-Error`）。**直连裸凭证头实测能过**，
   这意味着签到不依赖 CDP、不依赖桌面端在跑、不依赖抓包。
3. **签到 POST 是幂等的。** 今天已签到时返回 HTTP 400 + `code: 10001`。
   所以「签到」这个动作可以无脑重试，不需要「先查状态再决定签不签」的状态机。

### 正常响应

签到：

```json
{"code":0,"msg":"OK","data":{"credit":100,"streak_days":4,"is_streak_day":false}}
```

状态：

```json
{"code":0,"data":{
  "active":true,"today_checked_in":true,"streak_days":4,
  "daily_credit":100,"today_credit":100,"total_credits":400,
  "checkin_dates":["2026-10-17","2026-10-16","2026-10-15","2026-10-14"],
  "week_checkin_days":4,
  "week_progress":[false,false,true,true,true,true,false],
  "start_time":"2026-09-30 00:00:00","end_time":"2026-10-31 23:59:59",
  "theme_name":"Buddy加油站","season":10,"activity_name":"Buddy应用",
  "claim_button_text":"立即领取",
  "action_button":{"show":true,"text":"体验Buddy应用",
                   "action":"workbuddy://home?openBuddySwitcher=true"}
}}
```

字段含义（按用途分三类）：

- **判断用**：`active`（活动是否在期）、`today_checked_in`、`streak_days`、`checkin_dates[]`。
- **展示用**：`daily_credit`、`total_credits`、`week_progress[]`（周一到周日）、
  `theme_name`、`season`、`activity_name`、`start_time` / `end_time`。
- **给客户端的动作入口**：`action_button.action` 是 `workbuddy://` 深链，
  只对桌面端有意义，网关用不上。

### 每日积分口径

`daily_credit` 是**当天**签到给多少（实测 100），`total_credits` 是本活动期内累计
（实测 300 → 签到后 400）。两者都不是「余额」——余额看账单接口
（`internal/billing`）。**不要把 `total_credits` 当成资源包剩余量。**

---

## 2. 补签与兑换：机制不明，动作不存在（已查证）

### 2.1 一个要纠正的说法：「用补签卡补签」是推测，不是事实

`GET /activity/growth/streak` 返回里确实有这些字段：

```json
{"code":0,"data":{
  "streak":{"days":3,"month_total_days":3,"next_tier":"7d",
            "next_tier_remaining":4,"makeup_dates":[]},
  "makeup_cards":{"balance":0,"max":4},
  "redemption_status":{
    "tier_7d_status":"locked","tier_14d_status":"locked","tier_28d_status":"locked",
    "remaining_days":3,
    "tiers":[
      {"tier":"7d","days":7,"credit":0,"energy":2,"cards":1,"chances":1},
      {"tier":"14d","days":14,"credit":50,"energy":3,"cards":1,"chances":1},
      {"tier":"28d","days":28,"credit":150,"energy":5,"cards":1,"chances":1}
    ]},
  "timezone":"Asia/Shanghai","launch_date":"2026-06-17"
}}
```

**但「这些字段怎么用」没有任何证据。** 拿 `makeup_cards{balance,max}`、
`tiers[].cards/chances`、`tier_*_status` 拼出「用补签卡补签」「用积分兑换档位」——
那是**看字段名推测的**。字段可能是预留的、也可能只是「连签到达成后自动发放」的状态记录。

（之所以一度以为有，是因为 `~/RuiC-freebuddy` 解析了这些字段。查证：它只在
`official.mjs:152-168` 把它们转成数字，`panel.js` 里**从未渲染、从未调用**——
是我看到解析代码就当成功能存在了。）

### 2.2 查证结果：客户端与网页端都没有这两个动作

| 位置 | 结果 |
|---|---|
| 客户端 `app.asar` | `补签` / `兑换` / `redeem` / `makeup_dates` 均**零命中**（`兑换` 的命中全是 Excel 函数、货币名、emoji 关键字之类的噪音） |
| 客户端所有 `activity/growth/*` 路径 | 只有 `buddy/info`、`buddy/travel/claim`；没有补签、没有档位兑换 |
| 网页端 `codebuddy.cn` 主包 | 有「兑换码」弹窗（`/portal/operation-platform/client/redeem/consume`），但那只是**输入兑换码**，与签到无关 |
| 网页端其他 | `/billing/meter/claim-gift`、`/claim-compensation` 是「礼包领取」与「任务失败补偿」，也与签到无关 |
| 18 个候选端点（9 补签 + 9 兑换） | **全部 404** |

### 2.3 结论：不提供补签，也不提供兑换

**不猜端点。** 本工具：

1. 不实现补签动作、不实现积分兑换。
2. 不在 UI 上留禁用的按钮（猜错了对用户是误导）。
3. 日历上的「漏签」只陈述一个事实（那天没签），不代表能补。

如果日后真的在官方界面上看到了这两个入口，拿那条请求的 **Copy as cURL** 过来，
就可以把这一节补成事实。在那之前，这里就是空的。

### 2.4 相关接口（这些是实测到的）

| 用途 | 请求 |
|---|---|
| 连续天数 / 补签卡余额 / 档位 | `GET /activity/growth/streak` |
| 活跃热力图 | `GET /activity/growth/heatmap` |
| 成长任务列表 | `GET /v2/activity/growth/tasks`（不带 v2 也通） |

---

## 3. 对网关的结论

1. **签到可以独立于客户端**：网关持有凭证，直连即可，无需 CDP、无需桌面端在跑。
2. **签到模块应当是「执行一次」的无状态单元**，调度交给调用方
   （网关自己的定时器、控制台按钮、外部 MCP 看板）。
   理由：调度器只能有一个；模块自己带 interval 会和看板打架。
3. **状态查询走 `checkin-activity-status`，绝不用旧的 `checkin-status`。**
4. **补签与兑换都不做**——详见第 2 节。
