<script>
  import Skeleton from "../lib/Skeleton.svelte";
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import Chart from "../lib/Chart.svelte";
  import { stackedBarOptions, lineOptions, colorFor, ink } from "../lib/chartTheme.js";
  import * as fmt from "../lib/format.js";

  let usage = $state(null);
  let billing = $state(null);
  // 签到提醒。后端判好了「该不该提」，这里只负责画。
  // 它拉不到时静默——首页的主要职责不是签到，缺一条提醒不该报错。
  let checkinReminder = $state("");
  let checkinAvailable = $state(false);
  let error = $state("");
  let loading = $state(true);
  let days = $state(14);

  // 会走的「现在」。限流卡片判的是「恢复时间过了没有」，
  // 而这个判断每过一秒结论都可能变——内部偷偷读时钟的话，
  // 到点的限流会一直挂在页面上（用户看到的就是「过期了还在显示」）。
  let now = $state(Date.now());
  onMount(() => {
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  async function load() {
    loading = true;
    error = "";
    // 三个接口并行拉：它们互不依赖，串行只是白等。
    const results = await Promise.allSettled([api.usage(days), api.billing(), api.checkin()]);

    if (results[0].status === "fulfilled") usage = results[0].value;
    if (results[1].status === "fulfilled") billing = results[1].value;
    if (results[2].status === "fulfilled") {
      checkinReminder = results[2].value?.reminder ?? "";
      checkinAvailable = results[2].value?.available ?? false;
    }

    // 余额与签到失败不致命（账单接口偶尔抽风、客户端模式本来就没签到），
    // 用量失败才值得报错。
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

  // 当前仍可能生效的限流。后端已按恢复时间过滤过一次，这里再按
  // 「恢复时间过了没有」实时过滤——否则一次拉到的数据里那些快到的限流
  // 会一直挂到下次刷新。解析不出恢复时间的一并排除：
  // 宁可少说一句，也不要告诉用户一个错的时间。
  const limits = $derived(
    (usage?.totals?.active_limits ?? []).filter((limit) => {
      if (!limit.reset_at) return false;
      const reset = new Date(limit.reset_at).getTime();
      return !Number.isNaN(reset) && reset > now;
    }),
  );

  // 距离恢复还有多久。恢复时间在未来，所以不能复用 fmt.relative——
  // 那是「多久以前」，用在未来时刻上会读出「刚刚」这种反话。
  // 精确时刻放进 title：倒计时给判断，绝对时间给对账。
  function countdown(resetAt) {
    const seconds = Math.max(0, Math.round((new Date(resetAt).getTime() - now) / 1000));
    return seconds < 60 ? "1 分钟内" : fmt.uptime(seconds);
  }

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

<!-- 通知区：首页的常驻提醒都放这里。
     以后新增一类首页提醒就多一个块，不另开一处——用户只需要在一个地方看。
     每块保持「一句话 + 去哪看详情」（design-v2 的提示框职责边界）。
     没有可说的就不渲染整个区，不占屏幕。 -->
{#if checkinReminder || limits.length > 0}
  <section class="section">
    <header>
      <h2>通知</h2>
      <span class="hint">首页常驻提醒</span>
    </header>

    {#if checkinReminder}
      <div class="notice warn">
        <strong>签到还没领</strong>：{checkinReminder}
        <a href="#/checkin">去签到 →</a>
      </div>
    {/if}

    {#if limits.length > 0}
      <div class="notice warn limits">
        <strong>有模型正在频率限制中</strong>（{limits.length} 个）：
        <ul>
          {#each limits as limit (limit.model)}
            <li>
              <code>{limit.model}</code> 预计 <strong title={fmt.time(limit.reset_at)}
                >{countdown(limit.reset_at)}</strong
              > 后恢复
              <span class="hint">（{limit.since} 撞墙；也可以换其他模型继续用）</span>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </section>
{/if}

<section class="section">
  <header>
    <h2>总览</h2>
    <span class="hint">统计来自事件文件，实时；余额来自账单接口，有延迟</span>
  </header>

  <div class="grid stats">
    <!-- 主角排在第一个：2fr 落在栅格的第一个子元素上，
         主角排第三的话只会拿到 1fr，数字就折了（design-v2.md 第九节）。 -->
    <div class="stat hero">
      <div class="label"><span>剩余额度</span></div>
      <div class="value">{billing ? fmt.credit(billing.total_remain) : "—"}</div>
      <div class="sub">
        {billing ? `${billing.packages.length} 个资源包` : "读取中"}
        {#if billing?.source === "gateway"}
          · 来自网关
        {/if}
      </div>
    </div>
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
    <div class="stat">
      <div class="label"><span>拦截 / 拒答</span></div>
      <div class="value">{totals.blocked}<small> / {totals.refused}</small></div>
      <div class="sub">确定拦截 {fmt.percent(totals.block_rate)} · 疑似拒答 {totals.refused} 次</div>
    </div>
  </div>
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
            <th class="num">周期结束</th>
            <th class="num">到期</th>
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
      <!-- 中间对齐：这一行里「命中」那一格是双行，其余都是单行。
           全局那档 top 是给多行说明列的，用在这里会让单行格子
           的内容贴在行首、和双行格的第一行错开。 -->
      <div class="card tight">
        <table class="middle">
          <thead>
            <tr>
              <th>模型</th>
              <th class="num">请求</th>
              <th class="num">输入</th>
              <th class="num">命中 / 命中率</th>
              <th class="num">消耗</th>
              <th class="num">占比</th>
            </tr>
          </thead>
          <tbody>
            {#each usage.models as model (model.model)}
              <tr>
                <td class="mono">{model.model}</td>
                <td class="num">{model.requests}</td>
                <td class="num">{fmt.tokens(model.in)}</td>
                <!-- 命中数与命中率各占一行：挤在同一行里，长一点的模型名
                     就会把这一格折成两行，于是每行高度都不一致。
                     拆成两行后，命中率永远有自己那一行，行高也就齐了。 -->
                <td class="num">
                  {#if model.cached}
                    <div>{fmt.tokens(model.cached)}</div>
                    <div class="dim">
                      {model.in > 0 ? fmt.percent(model.cached / model.in) : "—"}
                    </div>
                  {:else}
                    —
                  {/if}
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
