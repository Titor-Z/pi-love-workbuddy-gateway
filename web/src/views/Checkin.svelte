<script>
  import { onMount } from "svelte";
  import { api, ApiError } from "../lib/api.js";
  import * as fmt from "../lib/format.js";

  let payload = $state(null);
  let error = $state("");
  let notice = $state("");
  let loading = $state(true);
  let running = $state(false);

  // 日历的「现在」要会走：页面开着跨过午夜时，今天那一格得跟着挪。
  // 一分钟一次就够——签到按天算，秒级没有意义。
  let now = $state(new Date());
  onMount(() => {
    const timer = setInterval(() => (now = new Date()), 60000);
    return () => clearInterval(timer);
  });

  async function load() {
    loading = true;
    error = "";
    try {
      payload = await api.checkin();
      // 首次拿到数据时把日历停在「今天」所在的月。
      if (payload?.status && !viewMonth) {
        const today = parseDateKey(todayKey);
        if (today) {
          viewYear = today.year;
          viewMonth = today.month;
        }
      }
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  async function runCheckin() {
    running = true;
    error = "";
    notice = "";
    try {
      const result = await api.runCheckin();
      const detail = result?.result;
      if (result?.ok) {
        notice = detail?.already
          ? `今天已经签过了（连续 ${detail.streak_days} 天）。`
          : `签到成功，+${fmt.credit(detail?.credit)} 积分，连续 ${detail?.streak_days} 天。`;
      } else {
        error = detail?.message || "签到没有成功。";
      }
      // 状态与记录都会变，重新拉一次，不本地拼。
      await load();
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
    running = false;
  }

  const status = $derived(payload?.status ?? null);
  const recent = $derived(payload?.recent ?? []);

  // 日历当前显示的月份。null 表示还没定，等数据回来落到今天那一月。
  let viewYear = $state(null);
  let viewMonth = $state(null);

  // 今天是本地日期。签到的「一天」按本机时区算，与后端一致。
  const todayKey = $derived.by(() => {
    const pad = (n) => String(n).padStart(2, "0");
    return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
  });

  // 已签到日期的集合。后端给的是 "YYYY-MM-DD"，字符串直接比大小即可。
  const signedDays = $derived(new Set(status?.checkin_dates ?? []));

  // 活动区间。两个时间戳都是 "YYYY-MM-DD HH:MM:SS"，
  // 只取日期部分——签到按天算。
  const rangeStart = $derived(dateKeyOf(status?.start_time));
  const rangeEnd = $derived(dateKeyOf(status?.end_time));

  const grid = $derived.by(() => {
    if (!viewYear || !viewMonth) return [];
    return buildMonthGrid(viewYear, viewMonth);
  });

  // 翻月的边界：活动区间之外的月份没有意义，不给翻过去。
  const canPrev = $derived.by(() => {
    if (!rangeStart || !viewYear) return true;
    const start = parseDateKey(rangeStart);
    if (!start) return true;
    return viewYear > start.year || (viewYear === start.year && viewMonth > start.month);
  });
  const canNext = $derived.by(() => {
    if (!rangeEnd || !viewYear) return true;
    const end = parseDateKey(rangeEnd);
    if (!end) return true;
    return viewYear < end.year || (viewYear === end.year && viewMonth < end.month);
  });

  function shiftMonth(delta) {
    if (!viewYear) return;
    let month = viewMonth + delta;
    let year = viewYear;
    if (month < 1) {
      month = 12;
      year -= 1;
    } else if (month > 12) {
      month = 1;
      year += 1;
    }
    viewYear = year;
    viewMonth = month;
  }

  // 一格的状态。判据都在这里，模板只负责挑样式。
  function dayState(day) {
    if (!day) return "blank";
    const key = dateKeyOf(viewYear, viewMonth, day);
    // 活动区间之外：不判「漏签」——那时候根本没开活动。
    if (rangeStart && key < rangeStart) return "outside";
    if (rangeEnd && key > rangeEnd) return "outside";
    if (key > todayKey) return "future";
    if (signedDays.has(key)) return key === todayKey ? "today-signed" : "signed";
    if (key === todayKey) return "today";
    return "missed";
  }

  const legend = [
    { className: "signed", text: "已签到" },
    { className: "today", text: "今天待签" },
    { className: "missed", text: "漏签" },
    { className: "future", text: "未到" },
  ];

  const monthLabel = $derived(
    viewYear ? `${viewYear} 年 ${viewMonth} 月` : "",
  );
</script>

<script module>
  // "2026-10-17" → { year, month, day }。
  //
  // 刻意不用 Date 解析：这些是「日期」不是「时刻」，交给 Date 会被当成
  // 本地零点，遇到时区或夏令时可能漂一天。拆成数字最稳。
  function parseDateKey(value) {
    if (typeof value !== "string") return null;
    const matched = /^(\d{4})-(\d{2})-(\d{2})/.exec(value.trim());
    if (!matched) return null;
    return { year: Number(matched[1]), month: Number(matched[2]), day: Number(matched[3]) };
  }

  // 把日期拼成键。
  //   dateKeyOf("2026-09-30 00:00:00") → "2026-09-30"
  //   dateKeyOf(2026, 10, 3)          → "2026-10-03"
  function dateKeyOf(a, b, c) {
    if (typeof a === "number" && typeof b === "number" && typeof c === "number") {
      const pad = (n) => String(n).padStart(2, "0");
      return `${a}-${pad(b)}-${pad(c)}`;
    }
    const parsed = parseDateKey(a);
    if (!parsed) return "";
    const pad = (n) => String(n).padStart(2, "0");
    return `${parsed.year}-${pad(parsed.month)}-${pad(parsed.day)}`;
  }

  // 生成一个月的格子：周一在第一列（与 week_progress 的口径一致），
  // 前后用 null 补齐整行。
  function buildMonthGrid(year, month) {
    // getDay() 是 0=周日；换算成「周一起算」的列偏移。
    const firstWeekday = new Date(year, month - 1, 1).getDay();
    const offset = (firstWeekday + 6) % 7;
    const daysInMonth = new Date(year, month, 0).getDate();
    const cells = [];
    for (let i = 0; i < offset; i++) cells.push(null);
    for (let day = 1; day <= daysInMonth; day++) cells.push(day);
    while (cells.length % 7 !== 0) cells.push(null);
    return cells;
  }
</script>

<section class="section">
  <header>
    <h2>签到</h2>
    <div class="bar">
      <button class="action" onclick={load} disabled={loading}>刷新</button>
      <button
        class="action primary"
        onclick={runCheckin}
        disabled={running || !payload?.available || status?.checked_in_today}
      >
        {running ? "签到中…" : status?.checked_in_today ? "今天已签到" : "立即签到"}
      </button>
    </div>
  </header>

  {#if error}
    <div class="notice error">{error}</div>
  {/if}
  {#if notice}
    <div class="notice ok">{notice}</div>
  {/if}
  {#if payload?.error}
    <div class="notice warn">读取实时状态失败：{payload.error}</div>
  {/if}

  {#if loading}
    <div class="notice">正在读取…</div>
  {:else if !payload?.available}
    <div class="card tight">
      <p>当前模式没有签到能力。</p>
      <p class="dim">
        {payload?.error || "签到需要用账号凭证，而客户端模式按设计不持有凭证。"}
      </p>
    </div>
  {:else if !status}
    <div class="notice">暂时读不到签到状态。</div>
  {:else if !status.active}
    <div class="card tight">
      <p>当前没有进行中的签到活动。</p>
      <p class="dim">
        {status.activity_name ? `最近一次活动：${status.activity_name}` : "官方未开放签到活动。"}
      </p>
    </div>
  {:else}
    <div class="grid stats">
      <!-- 主角：连续天数。这是签到页唯一真正要盯的数字。 -->
      <div class="stat hero">
        <div class="label"><span>连续签到</span></div>
        <div class="value">{status.streak_days}</div>
        <div class="sub">天</div>
      </div>
      <div class="stat">
        <div class="label"><span>今天</span></div>
        <div class="value" style="font-size:20px">{status.checked_in_today ? "已签到" : "未签到"}</div>
        <div class="sub">{status.checked_in_today ? "明天再来" : "右上角可以立刻签"}</div>
      </div>
      <div class="stat">
        <div class="label"><span>今日积分</span></div>
        <div class="value">{fmt.credit(status.daily_credit)}</div>
        <div class="sub">单日签到奖励</div>
      </div>
      <div class="stat">
        <div class="label"><span>活动期内累计</span></div>
        <div class="value">{fmt.credit(status.total_credit)}</div>
        <div class="sub">{status.activity_name}</div>
      </div>
    </div>

    <div class="card" style="margin-top:24px">
      <div class="cal-head">
        <h3>{monthLabel}</h3>
        <div class="cal-nav">
          <button class="action" onclick={() => shiftMonth(-1)} disabled={!canPrev}>上月</button>
          <button class="action" onclick={() => shiftMonth(1)} disabled={!canNext}>下月</button>
        </div>
      </div>

      <div class="cal-weekdays">
        {#each ["一", "二", "三", "四", "五", "六", "日"] as name (name)}
          <span>{name}</span>
        {/each}
      </div>

      <div class="cal-grid">
        {#each grid as day, index (index)}
          {@const state = dayState(day)}
          <div class="cal-cell {state}">
            {#if day}
              <span class="cal-day">{day}</span>
              {#if state === "signed" || state === "today-signed"}
                <span class="cal-mark">✓</span>
              {:else if state === "missed"}
                <span class="cal-mark">·</span>
              {/if}
            {/if}
          </div>
        {/each}
      </div>

      <div class="cal-legend">
        {#each legend as item (item.className)}
          <span class="legend-item">
            <span class="legend-box {item.className}"></span>{item.text}
          </span>
        {/each}
      </div>

      <p class="dim" style="margin-top:12px">
        活动：{status.theme_name || status.activity_name}，{status.start_time} 起。
        签到按自然日算（北京时间）。
      </p>
    </div>
  {/if}

  {#if payload?.available}
    <div class="card" style="margin-top:24px">
      <h3>最近记录</h3>
      {#if recent.length === 0}
        <p class="dim" style="margin-top:8px">本机还没有签到记录。自动签到开启后，签过的每一天都会记在这里。</p>
      {:else}
        <table class="middle" style="margin-top:8px">
          <thead>
            <tr>
              <th style="width:120px">日期</th>
              <th style="width:100px">结果</th>
              <th style="width:100px">积分</th>
              <th style="width:100px">连续</th>
              <th>说明</th>
            </tr>
          </thead>
          <tbody>
            {#each recent as item (item.date + item.at)}
              <tr>
                <td class="mono-small">{item.date}</td>
                <td>
                  {#if item.done}
                    <span class="tag">已签到</span>
                  {:else}
                    <span class="tag rate">失败</span>
                  {/if}
                </td>
                <!-- 纯数字列用等宽，不用右对齐：右对齐只在竖着比一列数字时才有意义。 -->
                <td class="mono-small">{item.credit ? fmt.credit(item.credit) : "—"}</td>
                <td class="mono-small">{item.streak_days || "—"}</td>
                <td class="dim" style="font-weight:400">{item.message}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  {/if}

  <div class="card" style="margin-top:24px">
    <h3>自动签到</h3>
    <p class="dim" style="margin-top:8px">
      在「设置」页打开后，网关每天在指定时刻替你签到一次。
      网关没在运行时不会签到——它不假装能保证「每天必到」。
    </p>
    <p class="dim" style="margin-top:6px">
      日历上的「漏签」只是那一天没签，<strong>不代表能补</strong>。
      官方补签入口还没找到（客户端里也没有），所以本工具不提供补签操作。
    </p>
  </div>
</section>

<style>
  .cal-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 16px;
  }
  .cal-nav {
    display: flex;
    gap: 8px;
  }

  /* 星期表头与格子用同一套列宽，否则上下对不齐。 */
  .cal-weekdays,
  .cal-grid {
    display: grid;
    grid-template-columns: repeat(7, 1fr);
    gap: 4px;
    margin-top: 12px;
  }
  .cal-weekdays span {
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    text-align: center;
    color: var(--muted);
  }

  /* 格子用固定高度，**不能用 aspect-ratio**：
     七列铺满宽容器时，等比正方形会把每格撑到 170px 高，
     一个月的日历就占满整屏了（已踩过）。日历是索引，不是主角。 */
  .cal-cell {
    height: 44px;
    border: 2px solid var(--line);
    border-radius: var(--r-control);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1px;
  }
  .cal-day {
    font-family: var(--font-mono);
    font-size: 13px;
    line-height: 1;
  }
  .cal-mark {
    font-size: 11px;
    line-height: 1;
  }

  /* 已签到：反白块是这套设计里最强的信号。 */
  .cal-cell.signed,
  .cal-cell.today-signed {
    background: var(--invert-bg);
    color: var(--invert-fg);
    border-color: var(--ink);
  }
  /* 今天待签：只描重边，不填充——还没发生的事不该和已签的一样重。 */
  .cal-cell.today {
    border-color: var(--ink);
    border-width: 3px;
  }
  /* 漏签：虚线。它不是「完成」，也不该像空白一样被忽略。 */
  .cal-cell.missed {
    border-style: dashed;
    color: var(--muted);
  }
  /* 未到 / 活动外 / 空白：留白，不参与判断。 */
  .cal-cell.future,
  .cal-cell.outside,
  .cal-cell.blank {
    border-color: transparent;
    color: var(--muted);
  }
  .cal-cell.outside {
    opacity: 0.4;
  }

  .cal-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 16px;
    margin-top: 16px;
    font-size: 12px;
    color: var(--muted);
  }
  .legend-item {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .legend-box {
    width: 14px;
    height: 14px;
    border: 2px solid var(--line);
    border-radius: 2px;
  }
  .legend-box.signed {
    background: var(--invert-bg);
    border-color: var(--ink);
  }
  .legend-box.today {
    border-color: var(--ink);
    border-width: 3px;
  }
  .legend-box.missed {
    border-style: dashed;
  }
  .legend-box.future {
    border-color: transparent;
    opacity: 0.45;
  }
</style>
