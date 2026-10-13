<script>
  import { api } from "../lib/api.js";
  import Chart from "../lib/Chart.svelte";
  import { stackedBarOptions, lineOptions, colorFor, ink } from "../lib/chartTheme.js";
  import * as fmt from "../lib/format.js";

  let usage = $state(null);
  let billing = $state(null);
  let error = $state("");
  let loading = $state(true);
  let days = $state(14);

  async function load() {
    loading = true;
    error = "";
    // 两个接口并行拉：余额与用量互不依赖，串行只是白等。
    const results = await Promise.allSettled([api.usage(days), api.billing()]);

    if (results[0].status === "fulfilled") usage = results[0].value;
    if (results[1].status === "fulfilled") billing = results[1].value;

    // 余额失败不致命（账单接口偶尔抽风），用量失败才值得报错。
    const failures = results.filter((r) => r.status === "rejected").map((r) => r.reason.message);
    if (results[0].status === "rejected") error = failures[0];
    else if (failures.length) error = `余额读取失败：${failures[0]}`;

    loading = false;
  }

  $effect(() => {
    days; // 建立依赖：切换窗口时重新拉数据
    load();
  });

  // 后端可能给出两个完全同名的资源包（例如两个「国内运营裂变包」）。
  // 同名时补一个序号，否则看起来像是渲染重复了。
  const packageLabels = $derived.by(() => {
    const packs = billing?.packages ?? [];
    const total = {};
    for (const pack of packs) total[pack.name] = (total[pack.name] ?? 0) + 1;
    const seen = {};
    return packs.map((pack) => {
      if (total[pack.name] === 1) return pack.name;
      seen[pack.name] = (seen[pack.name] ?? 0) + 1;
      return `${pack.name} #${seen[pack.name]}`;
    });
  });

  const today = $derived(usage?.days?.at(-1) ?? null);
  const totals = $derived(usage?.totals ?? { requests: 0, credit: 0, blocked: 0, block_rate: 0 });

  // 堆叠柱状图：x 轴是日期，每个模型一条数据集。
  const chartData = $derived.by(() => {
    if (!usage) return { labels: [], datasets: [] };
    const models = usage.models.map((m) => m.model);
    return {
      labels: usage.days.map((d) => fmt.shortDate(d.date)),
      datasets: models.map((model, index) => ({
        label: model,
        data: usage.days.map((d) => d.by_model[model] ?? 0),
        backgroundColor: colorFor(index),
        // 柱子的描边跟前景色走，暗色下才是白线。
        borderColor: ink(),
        borderWidth: 2,
        // 无圆角是设计系统的硬要求，Chart.js 默认会圆角。
        borderRadius: 0,
        borderSkipped: false,
      })),
    };
  });

  // 请求数折线：与消耗分开画，因为两个量的单位完全不同，
  // 塞进同一张图会需要一个没人看得懂的右轴。
  const requestsChart = $derived({
    labels: usage?.days?.map((d) => fmt.dayOnly(d.date)) ?? [],
    datasets: [
      {
        label: "请求数",
        data: usage?.days?.map((d) => d.requests) ?? [],
        borderColor: ink(),
        backgroundColor: ink(),
        fill: false,
        pointBackgroundColor: ink(),
      },
    ],
  });
</script>

{#if error}
  <div class="notice error">{error}</div>
{/if}

<section class="section">
  <header>
    <h2>总览</h2>
    <span class="hint">统计来自事件文件，实时；余额来自账单接口，有延迟</span>
  </header>

  <div class="grid stats">
    <div class="stat">
      <div class="label"><span>今日请求</span></div>
      <div class="value">{today?.requests ?? 0}</div>
      <div class="sub">最近 {days} 天 {totals.requests} 次</div>
    </div>
    <div class="stat">
      <div class="label"><span>今日消耗</span></div>
      <div class="value">{fmt.credit(today?.credit)}</div>
      <div class="sub">最近 {days} 天 {fmt.credit(totals.credit)}</div>
    </div>
    <div class="stat hero">
      <div class="label"><span>剩余额度</span></div>
      <div class="value">{billing ? fmt.credit(billing.total_remain) : "—"}</div>
      <div class="sub">{billing ? `${billing.packages.length} 个资源包` : "读取中"}</div>
    </div>
    <div class="stat">
      <div class="label"><span>拦截率</span></div>
      <div class="value">{fmt.percent(totals.block_rate)}</div>
      <div class="sub">{totals.blocked} 次命中内容审核</div>
    </div>
  </div>
</section>

<section class="section">
  <header>
    <h2>资源包</h2>
    <span class="hint">账单结算有 1～3 分钟延迟，按批到账</span>
  </header>

  {#if !billing}
    <div class="empty">读取中…</div>
  {:else}
    <div class="grid two">
      <!-- 用下标当 key：后端可能给出两个同名的资源包（例如两个「国内运营裂变包」），
           拿名字当 key 会让 Svelte 直接抛错，整页渲染中断。 -->
      {#each billing.packages as pack, index (index)}
        {@const ratio = pack.size > 0 ? pack.remain / pack.size : 0}
        <div class="card">
          <div style="display:flex;justify-content:space-between;gap:12px;align-items:flex-start">
            <strong style="font-size:14px">{packageLabels[index]}</strong>
            {#if pack.remain <= 0}
              <span class="tag blocked">已用完</span>
            {:else if ratio < 0.2}
              <span class="tag promo">偏低</span>
            {/if}
          </div>
          <div class="num" style="font-size:28px;margin-top:8px">
            {fmt.credit(pack.remain)}<small style="font-size:15px"> / {fmt.credit(pack.size)}</small>
          </div>
          <div class="sub num" style="font-size:11px;color:var(--muted)">
            本轮已用 {fmt.credit(pack.use)}
          </div>
          <div class="sub num" style="font-size:11px;color:var(--muted)">
            周期结束 {fmt.date(pack.cycle_end)} · 到期 {fmt.date(pack.expire)}
          </div>
        </div>
      {/each}
    </div>
    <p class="hint" style="font-family:var(--font-mono);font-size:11px;color:var(--muted);margin-top:12px">
      读取于 {fmt.time(billing.fetched_at)} · {billing.settle_hint}
    </p>
  {/if}
</section>

<section class="section">
  <header>
    <h2>消费趋势</h2>
    <div class="bar">
      {#each [7, 14, 30] as option}
        <button class="action" aria-pressed={days === option} onclick={() => (days = option)}>
          {option} 天
        </button>
      {/each}
    </div>
  </header>

  {#if loading && !usage}
    <div class="empty">读取中…</div>
  {:else if !usage || usage.models.length === 0}
    <div class="empty">还没有记录。通过网关发一次请求就会出现在这里。</div>
  {:else}
    <Chart type="bar" data={chartData} options={stackedBarOptions()} />
    <div class="legend">
      {#each usage.models as model, index (model.model)}
        <span><i style="background:{colorFor(index)}"></i>{model.model} {fmt.credit(model.credit)}</span>
      {/each}
    </div>
  {/if}
</section>

{#if usage && usage.models.length > 0}
  <section class="section">
    <header>
      <h2>模型用量</h2>
      <span class="hint">最近 {days} 天</span>
    </header>

    <div class="grid two">
      <div class="card tight">
        <table>
          <thead>
            <tr>
              <th>模型</th>
              <th class="num">请求</th>
              <th class="num">输入</th>
              <th class="num">命中</th>
              <th class="num">消耗</th>
              <th class="num">占比</th>
            </tr>
          </thead>
          <tbody>
            {#each usage.models as model (model.model)}
              <tr>
                <td class="num">{model.model}</td>
                <td class="num">{model.requests}</td>
                <td class="num">{fmt.tokens(model.in)}</td>
                <td class="num">
                  {model.cached
                    ? `${fmt.tokens(model.cached)} ${model.in > 0 ? fmt.percent(model.cached / model.in) : ""}`
                    : "—"}
                </td>
                <td class="num">{fmt.credit(model.credit)}</td>
                <td class="num">
                  {totals.credit > 0 ? fmt.percent(model.credit / totals.credit) : "—"}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <Chart type="line" data={requestsChart} options={lineOptions()} height="260px" />
    </div>
  </section>
{/if}
