<script>
  import Skeleton from "../lib/Skeleton.svelte";
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

  // 当前仍可能生效的限流。后端给出恢复时间才算，解析不出就不显示——
  // 宁可少说一句，也不要告诉用户一个错的恢复时间。
  const limits = $derived(usage?.totals?.active_limits ?? []);

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
  // 横轴用完整日期（10-14），跟上面那张消耗柱状图同一种写法——
  // 两张图并排在同一条页面上，横轴不该有两套读法。
  const requestsChart = $derived({
    labels: usage?.days?.map((d) => fmt.shortDate(d.date)) ?? [],
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
      <div class="label"><span>拦截 / 拒答</span></div>
      <div class="value">{totals.blocked}<small> / {totals.refused}</small></div>
      <div class="sub">确定拦截 {fmt.percent(totals.block_rate)} · 疑似拒答 {totals.refused} 次</div>
    </div>
  </div>

  {#if limits.length > 0}
    <div class="notice warn limits">
      <strong>有模型正在频率限制中</strong>（{limits.length} 个）：
      <ul>
        {#each limits as limit (limit.model)}
          <li>
            <code>{limit.model}</code> 预计 <strong>{limit.reset_at}</strong> 恢复
            <span class="hint">（{limit.since} 撞墙；也可以换其他模型继续用）</span>
          </li>
        {/each}
      </ul>
    </div>
  {/if}
</section>

<section class="section">
  <header>
    <h2>资源包</h2>
    <span class="hint">账单结算有 1～3 分钟延迟，按批到账</span>
  </header>

  {#if !billing}
    <Skeleton rows={5} shape="table" cols={6} />
  {:else}
    <!-- 这里是表格，不是卡片。
         四个长得一模一样的卡片格是典型的「AI 味」（impeccable-design-polish 的清单里
         明确列了这一条），而且它本来就是表格数据：名称 / 剩余 / 总量 / 已用 / 到期。
         表格更密、更省地方，而且数字能对着列比——这才是有用的部分。 -->
    <div class="card tight">
      <table>
        <thead>
          <tr>
            <th>资源包</th>
            <th class="num">本轮剩余</th>
            <th class="num">本轮总量</th>
            <th class="num">本轮已用</th>
            <th>周期结束</th>
            <th>到期</th>
          </tr>
        </thead>
        <tbody>
          {#each billing.packages as pack, index (index)}
            {@const ratio = pack.size > 0 ? pack.remain / pack.size : 0}
            <tr>
              <td>
                {packageLabels[index]}
                {#if pack.remain <= 0}
                  <span class="tag blocked">已用完</span>
                {:else if ratio < 0.2}
                  <span class="tag promo">偏低</span>
                {/if}
              </td>
              <td class="num">{fmt.credit(pack.remain)}</td>
              <td class="num">{fmt.credit(pack.size)}</td>
              <td class="num">{fmt.credit(pack.use)}</td>
              <td class="num">{fmt.date(pack.cycle_end)}</td>
              <td class="num">{fmt.date(pack.expire)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
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
    <Skeleton shape="block" />
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
