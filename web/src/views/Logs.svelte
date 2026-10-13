<script>
  import { api } from "../lib/api.js";
  import * as fmt from "../lib/format.js";

  let events = $state([]);
  let error = $state("");
  let loading = $state(true);
  let onlyBlocked = $state(false);
  let onlyChat = $state(false);
  let view = $state("timeline"); // timeline | detail

  async function load() {
    loading = true;
    error = "";
    try {
      // 倒序显示：最近的在上面。
      events = (await api.events(300)).events.reverse();
    } catch (cause) {
      error = cause.message;
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  const visible = $derived(
    events.filter((event) => {
      if (onlyBlocked && !event.blocked) return false;
      if (onlyChat && event.kind !== "chat") return false;
      return true;
    }),
  );

  // 明细表只列对话。其它事件（启动、续期）没有 token 和费用，
  // 混进来会留一堆空列，反而看不清想问的那件事。
  const chats = $derived(visible.filter((event) => event.kind === "chat"));

  const blockedTotal = $derived(events.filter((e) => e.blocked).length);

  // 明细表底下的小结：只统计当前筛出来的这些。
  const summary = $derived.by(() => {
    const total = { requests: chats.length, credit: 0, in: 0, out: 0, cached: 0, blocked: 0 };
    for (const event of chats) {
      total.credit += event.credit ?? 0;
      total.in += event.in ?? 0;
      total.out += event.out ?? 0;
      total.cached += event.cached ?? 0;
      if (event.blocked) total.blocked++;
    }
    total.cacheRate = total.in > 0 ? total.cached / total.in : 0;
    return total;
  });
</script>

{#if error}
  <div class="notice error">{error}</div>
{/if}

<section class="section">
  <header>
    <h2>运行事件</h2>
    <span class="hint">最近 {events.length} 条 · {blockedTotal} 次命中内容审核</span>
  </header>

  <div class="bar" style="margin-bottom:12px">
    <button class="action" aria-pressed={view === "timeline"} onclick={() => (view = "timeline")}>
      时间线
    </button>
    <button class="action" aria-pressed={view === "detail"} onclick={() => (view = "detail")}>
      请求明细
    </button>
    <span style="width:1px;height:24px;background:var(--rule);margin:0 4px"></span>
    <button class="action" aria-pressed={onlyChat} onclick={() => (onlyChat = !onlyChat)}>
      只看对话
    </button>
    <button class="action" aria-pressed={onlyBlocked} onclick={() => (onlyBlocked = !onlyBlocked)}>
      只看被拦截
    </button>
    <button class="action" onclick={load} disabled={loading}>刷新</button>
  </div>

  {#if view === "detail"}
    <div class="notice">
      一行一次请求。<strong>缓存命中</strong>是输入里由后端缓存命中的 token 数。它是同一段对话
      时贵时便宜的主要原因，命中率掉下来，同样的对话就会多花钱。
      <span style="color:var(--muted)">这里只记录用量数字，不记录任何对话内容。</span>
    </div>

    <div class="grid stats" style="margin-bottom:16px">
      <div class="stat">
        <div class="label"><span>请求</span></div>
        <div class="value" style="font-size:24px">{summary.requests}</div>
        <div class="sub">{summary.blocked} 次被拦截</div>
      </div>
      <div class="stat">
        <div class="label"><span>合计消耗</span></div>
        <div class="value" style="font-size:24px">{fmt.credit(summary.credit)}</div>
        <div class="sub">当前筛选范围</div>
      </div>
      <div class="stat">
        <div class="label"><span>输入 / 输出</span></div>
        <div class="value" style="font-size:24px">
          {fmt.tokens(summary.in)}<small> / {fmt.tokens(summary.out)}</small>
        </div>
        <div class="sub">token</div>
      </div>
      <div class="stat">
        <div class="label"><span>缓存命中</span></div>
        <div class="value" style="font-size:24px">{fmt.percent(summary.cacheRate)}</div>
        <div class="sub">{fmt.tokens(summary.cached)} token 走了缓存</div>
      </div>
    </div>

    {#if loading && chats.length === 0}
      <div class="empty">读取中…</div>
    {:else if chats.length === 0}
      <div class="empty">没有符合条件的请求。</div>
    {:else}
      <div class="card tight">
        <table>
          <thead>
            <tr>
              <th style="width:96px">时间</th>
              <th>模型</th>
              <th class="num">输入</th>
              <th class="num">命中</th>
              <th class="num">输出</th>
              <th class="num">消耗</th>
              <th class="num">耗时</th>
              <th style="width:80px">方式</th>
            </tr>
          </thead>
          <tbody>
            {#each chats as event, index (index)}
              <tr>
                <td class="num">{fmt.clock(event.t)}</td>
                <td class="num">
                  {event.model}
                  {#if event.blocked}<span class="tag blocked">拦截</span>{/if}
                  {#if event.err}<span class="tag promo">失败</span>{/if}
                </td>
                <td class="num">{event.in ?? 0}</td>
                <td class="num">
                  {#if event.cached}
                    {event.cached}
                    <span class="cache-rate">
                      {event.in > 0 ? fmt.percent(event.cached / event.in) : ""}
                    </span>
                  {:else}
                    —
                  {/if}
                </td>
                <td class="num">{event.out ?? 0}</td>
                <td class="num">{fmt.credit(event.credit)}</td>
                <td class="num">{fmt.duration(event.ms)}</td>
                <td>{event.stream ? "流式" : "非流式"}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {:else}
    <div class="notice warn">
      <strong>关于「被拦截」</strong>：WorkBuddy 后端命中内容审核时，有时会在流里
      嵌入提示文字而不是返回错误码。本工具会在转发的同时扫一遍响应，命中就标记出来，
      <strong>不改动任何字节</strong>——pi 收到的内容与后端发来的完全一致。
      因为这个标记是事后打上的，偶尔会有误报或漏报，只能当参考。
    </div>

    {#if loading && events.length === 0}
      <div class="empty">读取中…</div>
    {:else if visible.length === 0}
      <div class="empty">没有符合条件的事件。</div>
    {:else}
      <div class="log">
        {#each visible as event, index (index)}
          <div class="log-row" class:blocked={event.blocked}>
            <span class="t">{fmt.clock(event.t)}</span>
            <span class="kind">{fmt.kindLabel(event.kind)}</span>
            <span>{event.msg || "—"}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .cache-rate {
    font-size: 10px;
    color: var(--muted);
    margin-left: 4px;
  }
</style>
