<script>
  import Skeleton from "../lib/Skeleton.svelte";
  import { api } from "../lib/api.js";
  import * as fmt from "../lib/format.js";

  let events = $state([]);
  let error = $state("");
  let loading = $state(true);
  let onlyBlocked = $state(false);
  let onlyRefused = $state(false);
  let onlyChat = $state(false);
  let view = $state("timeline"); // timeline | detail
  let page = $state(1);

  // 每页条数。列表不做内部滚动（外层页面已经是滚动容器，套两层会互相抢滚轮），
  // 所以长列表靠分页。
  const PAGE_SIZE = 50;

  async function load() {
    loading = true;
    error = "";
    try {
      // 倒序显示：最近的在上面。
      // 一次取够再在前端分页：50 条一页，300 条只有 6 页太浅。
      events = (await api.events(1000)).events.reverse();
    } catch (cause) {
      error = cause.message;
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  // 换筛选条件或换视图时回到第一页，否则会停在一个已经不存在的位置。
  // 这个 effect 不读 page，所以不会自己触发自己。
  $effect(() => {
    void onlyChat;
    void onlyBlocked;
    void view;
    page = 1;
  });

  const visible = $derived(
    events.filter((event) => {
      if (onlyBlocked && !event.blocked) return false;
      if (onlyRefused && !event.refused) return false;
      if (onlyChat && event.kind !== "chat") return false;
      return true;
    }),
  );

  const refusedTotal = $derived(events.filter((e) => e.refused).length);

  // 明细表只列对话。其它事件（启动、续期）没有 token 和费用，
  // 混进来会留一堆空列，反而看不清想问的那件事。
  const chats = $derived(visible.filter((event) => event.kind === "chat"));
  const pagedChats = $derived(chats.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE));

  const blockedTotal = $derived(events.filter((e) => e.blocked).length);

  // 页码基数跟当前视图走：明细视图只列对话，时间线列全部。
  const total = $derived(view === "detail" ? chats.length : visible.length);
  const pageCount = $derived(Math.max(1, Math.ceil(total / PAGE_SIZE)));
  const paged = $derived(visible.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE));

  // 翻页后把列表顶端带进视野，否则翻完还在页面底部，看不出内容换了。
  function goPage(next) {
    page = Math.min(Math.max(1, next), pageCount);
    document.querySelector(".section")?.scrollIntoView({ block: "start" });
  }

  // 明细表底下的小结：只统计当前筛出来的这些。
  const summary = $derived.by(() => {
    const total = { requests: chats.length, credit: 0, in: 0, out: 0, cached: 0, blocked: 0, refused: 0 };
    for (const event of chats) {
      total.credit += event.credit ?? 0;
      total.in += event.in ?? 0;
      total.out += event.out ?? 0;
      total.cached += event.cached ?? 0;
      if (event.blocked) total.blocked++;
      if (event.refused) total.refused++;
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
    <span class="hint">最近 {events.length} 条 · {blockedTotal} 次确定拦截 · {refusedTotal} 次疑似拒答</span>
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
      只看确定拦截
    </button>
    <button class="action" aria-pressed={onlyRefused} onclick={() => (onlyRefused = !onlyRefused)}>
      只看疑似拒答
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
        <div class="sub">{summary.blocked} 次确定拦截 · {summary.refused} 次疑似拒答</div>
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
      <Skeleton rows={5} />
    {:else if pagedChats.length === 0}
      <div class="empty">没有符合条件的请求。</div>
    {:else}
      <div class="card tight">
        <table>
          <thead>
            <tr>
              <th style="width:96px">时间</th>
              <th>模型</th>
              <th>结束</th>
              <th>拦截原因</th>
              <th class="num">输入</th>
              <th class="num">命中</th>
              <th class="num">输出</th>
              <th class="num">消耗</th>
              <th class="num">耗时</th>
              <th style="width:80px">方式</th>
            </tr>
          </thead>
          <tbody>
            {#each pagedChats as event, index (index)}
              <tr>
                <td class="num">{fmt.clock(event.t)}</td>
                <td class="num">
                  {event.model}
                  {#if event.blocked}<span class="tag blocked">拦截</span>{/if}
                  {#if event.refused}<span class="tag promo">拒答</span>{/if}
                  {#if event.err}<span class="tag promo">失败</span>{/if}
                </td>
                <td class="num">{event.finish ?? "—"}</td>
                <td class="num">
                  {#if event.blocked_reason}<span class="tag blocked">{event.blocked_reason}</span>{/if}
                  {#if event.refused}<span class="tag promo" title={event.refuse_rule}>疑似拒答</span>{/if}
                  {#if !event.blocked && !event.refused}—{/if}
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
      <strong>关于「拦截」与「拒答」</strong>：本工具把判定分成两栏，请分开理解：
      <ul>
        <li><strong>确定拦截</strong>：后端亲口承认拦了——流式通道的
          <code>finish_reason=content_filter</code> / 非空 <code>refusal</code>，或硬阻断通道的
          策略码（如非 200 的 <code>11128 安全策略拦截</code>）。这部分是准的。</li>
        <li><strong>疑似拒答</strong>：模型礼貌说「抱歉我不能……」时，后端给的也是
          <code>finish_reason=stop</code>，和正常回复没有结构区别，协议层<strong>无法</strong>可靠识别。
          这栏是本工具的<strong>文本启发式猜测</strong>，可能误报也可能漏报，<strong>不计入拦截率</strong>。</li>
      </ul>
      <strong>不改动任何字节</strong>——pi 收到的内容与后端发来的完全一致。拦截逻辑集中在
      <code>internal/blocked</code> 一个模块里，日后发现新的策略码或拒答说法，只改那一处即可。
    </div>

    {#if loading && events.length === 0}
      <Skeleton rows={5} />
    {:else if visible.length === 0}
      <div class="empty">没有符合条件的事件。</div>
    {:else}
      <div class="log">
        {#each paged as event, index (index)}
          <div class="log-row" class:blocked={event.blocked}>
            <span class="t">{fmt.clock(event.t)}</span>
            <span class="kind">{fmt.kindLabel(event.kind)}</span>
            <span>{event.msg || "—"}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}

  {#if pageCount > 1}
    <nav class="pager" aria-label="分页">
      <button class="action" onclick={() => goPage(1)} disabled={page === 1}>首页</button>
      <button class="action" onclick={() => goPage(page - 1)} disabled={page === 1}>上一页</button>
      <span class="pager-info num">
        第 {page} / {pageCount} 页 · 共 {total} 条
      </span>
      <button class="action" onclick={() => goPage(page + 1)} disabled={page === pageCount}>
        下一页
      </button>
      <button class="action" onclick={() => goPage(pageCount)} disabled={page === pageCount}>
        末页
      </button>
    </nav>
  {/if}
</section>

<style>
  .cache-rate {
    font-size: 10px;
    color: var(--muted);
    margin-left: 4px;
  }
</style>
