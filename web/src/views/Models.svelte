<script>
  import Skeleton from "../lib/Skeleton.svelte";
  import { api, downloadPiConfig, fetchPiConfigText } from "../lib/api.js";
  import * as fmt from "../lib/format.js";

  let models = $state([]);
  let error = $state("");
  let loading = $state(true);
  let notice = $state("");
  let filter = $state("");
  let piConfig = $state("");
  let showConfig = $state(false);

  // 编辑模式：一页一个主角，查看与编辑不挤在同一张宽表里。
  let editing = $state(false);
  let draft = $state({});
  let savingOverrides = $state(false);
  let overridesPath = $state("");

  // 冷同步状态。网关模式下这个接口 501，卡片整块不显示。
  let syncStatus = $state(null);
  let syncing = $state(false);

  // pi 的思考档位。只暴露最常用的低/中/高——
  // 全都摊开会变成每行七个下拉，反而没人愿意看。
  const THINKING_LEVELS = [
    { key: "low", label: "低" },
    { key: "medium", label: "中" },
    { key: "high", label: "高" },
  ];

  async function load() {
    loading = true;
    error = "";
    try {
      const [modelPayload, overridePayload] = await Promise.all([api.models(), api.overrides()]);
      models = modelPayload.models;
      overridesPath = overridePayload.path || "";
      draft = buildDraft(models, overridePayload.overrides);
    } catch (cause) {
      error = cause.message;
    }
    try {
      syncStatus = await api.overridesSync();
    } catch {
      syncStatus = null;
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  // 草稿里每个模型都先占一个位置，渲染时直接绑到已有对象上——
  // 边渲染边建对象会掉进「渲染期间改状态」的坑。
  function buildDraft(list, table) {
    const next = {};
    const saved = table?.models || {};
    for (const model of list) {
      const item = saved[model.id] || {};
      // null（不支持）在 select 里没有对应的 option，必须换成哨兵值，
      // 否则浏览器会回落到第一项，把「不支持」显示成「默认」。
      const thinking = {};
      for (const [level, value] of Object.entries(item.thinking_level_map || {})) {
        thinking[level] = value === null ? "__unsupported__" : value;
      }
      next[model.id] = {
        alias: item.alias || "",
        free_until: item.free_until || "",
        note: item.note || "",
        hidden: !!item.hidden,
        thinking,
      };
    }
    return next;
  }

  // 空值一律不写进文件：写进去只会让「哪些是用户真设的」变模糊，
  // 而三方合并恰恰要靠这个。
  function buildPayload() {
    const out = {};
    for (const [id, item] of Object.entries(draft)) {
      const entry = {};
      if (item.alias.trim()) entry.alias = item.alias.trim();
      if (item.free_until.trim()) entry.free_until = item.free_until.trim();
      if (item.note.trim()) entry.note = item.note.trim();
      if (item.hidden) entry.hidden = true;
      const map = {};
      for (const [level, value] of Object.entries(item.thinking || {})) {
        if (value === undefined || value === "") continue;
        map[level] = value === "__unsupported__" ? null : value;
      }
      if (Object.keys(map).length) entry.thinking_level_map = map;
      if (Object.keys(entry).length) out[id] = entry;
    }
    return out;
  }

  async function saveOverrides() {
    notice = "";
    error = "";
    savingOverrides = true;
    try {
      await api.saveOverrides(buildPayload());
      notice = "已保存。模型页与 pi 拿到的配置立即生效，不需要重启。";
      await load();
    } catch (cause) {
      error = cause.message;
    }
    savingOverrides = false;
  }

  async function runSync(prefer) {
    notice = "";
    error = "";
    syncing = true;
    try {
      syncStatus = await api.syncOverrides(prefer);
      if (syncStatus.conflicts?.length) {
        notice = `有 ${syncStatus.conflicts.length} 条两边都改过，需要你选一边。`;
      } else if (syncStatus.pushed) {
        notice = "已把本机的改动合并回网关。";
      } else if (syncStatus.adopted) {
        notice = "本机没有改动，已跟上网关那一份。";
      } else {
        notice = "已与网关一致。";
      }
      await load();
    } catch (cause) {
      error = cause.message;
    }
    syncing = false;
  }

  async function discard() {
    notice = "";
    error = "";
    await load();
  }

  const visible = $derived(
    filter
      ? models.filter((m) =>
          `${m.id} ${m.name} ${m.display_name}`.toLowerCase().includes(filter.toLowerCase()),
        )
      : models,
  );

  const freeCount = $derived(models.filter((m) => m.free).length);
  const hiddenCount = $derived(models.filter((m) => m.hidden).length);

  async function copyConfig() {
    notice = "";
    try {
      piConfig = await fetchPiConfigText();
      await navigator.clipboard.writeText(piConfig);
      notice = "已复制到剪贴板。粘贴进 ~/.pi/agent/models.json 即可。";
    } catch (cause) {
      notice = `复制失败：${cause.message}。可以点「下载」。`;
    }
  }

  async function download() {
    notice = "";
    try {
      await downloadPiConfig();
      notice = "已下载 models.json。";
    } catch (cause) {
      notice = cause.message;
    }
  }

  async function preview() {
    notice = "";
    try {
      piConfig = await fetchPiConfigText();
      showConfig = !showConfig;
    } catch (cause) {
      notice = cause.message;
    }
  }
</script>

{#if error}
  <div class="notice error">{error}</div>
{/if}
{#if notice}
  <div class="notice ok">{notice}</div>
{/if}

<section class="section">
  <header>
    <h2>模型目录</h2>
    <span class="hint">
      {models.length} 个模型，其中 {freeCount} 个免费{#if hiddenCount}，已隐藏 {hiddenCount} 个{/if} · 实时来自 WorkBuddy
    </span>
  </header>

  <div class="card" style="margin-bottom:16px">
    <div class="bar">
      {#if editing}
        <button class="action primary" onclick={saveOverrides} disabled={savingOverrides}>
          {savingOverrides ? "保存中…" : "保存覆盖"}
        </button>
        <button class="action" onclick={discard} disabled={savingOverrides}>放弃改动</button>
        <button class="action" onclick={() => (editing = false)} disabled={savingOverrides}>退出编辑</button>
      {:else}
        <button class="action" onclick={() => (editing = true)} disabled={loading}>编辑覆盖</button>
        <button class="action" onclick={copyConfig} disabled={loading}>复制 pi 配置</button>
        <button class="action" onclick={download} disabled={loading}>下载 models.json</button>
        <button class="action" onclick={preview} disabled={loading}>
          {showConfig ? "收起预览" : "预览配置"}
        </button>
      {/if}
      <button class="action" onclick={load} disabled={loading || savingOverrides}>刷新</button>
    </div>
    <p style="font-size:12px;margin:10px 0 0;color:var(--muted)">
      {#if editing}
        这里写的都是<strong>我们自己的</strong>信息：后端不提供，只能人工维护。
        保存后会写进 <code>{overridesPath || "model-overrides.json"}</code>，
        并立即进入模型列表与 pi 配置。
      {:else}
        配置指向 <code>http://127.0.0.1:8788/v1</code>。模型名里带着费用标记，
        pi 的模型选择器会原样显示，一眼能看出哪个免费。
      {/if}
    </p>
  </div>

  {#if syncStatus}
    <div class="card" style="margin-bottom:16px">
      <div class="bar">
        <button class="action primary" onclick={() => runSync("")} disabled={syncing || loading}>
          {syncing ? "同步中…" : "与网关同步"}
        </button>
        <span class="tag {syncStatus.dirty ? "promo" : "free"}">
          {syncStatus.dirty ? `本机有 ${syncStatus.dirty} 处改动` : "本机无改动"}
        </span>
        <span class="dim" style="font-size:12px">
          本机 revision {syncStatus.local_revision} · 同步基准 {syncStatus.base_revision}
        </span>
      </div>
      {#if syncStatus.conflicts?.length}
        <div class="conflicts">
          <p style="margin:0 0 8px">
            下面这些<strong>两台机器都改过</strong>，工具不替你决定。选一边之后会立刻合并回网关：
          </p>
          <table class="middle">
            <thead>
              <tr><th>模型</th><th>本机</th><th>网关</th><th></th></tr>
            </thead>
            <tbody>
              {#each syncStatus.conflicts as conflict (conflict.model)}
                <tr>
                  <td class="num">{conflict.model}</td>
                  <td>{conflict.local.alias || conflict.local.free_until || "—"}</td>
                  <td>{conflict.remote.alias || conflict.remote.free_until || "—"}</td>
                  <td><span class="dim" style="font-size:11px">用下面的按钮整体选边</span></td>
                </tr>
              {/each}
            </tbody>
          </table>
          <div class="bar" style="margin-top:12px">
            <button class="action" onclick={() => runSync("local")} disabled={syncing}>以本机为准</button>
            <button class="action" onclick={() => runSync("remote")} disabled={syncing}>以网关为准</button>
          </div>
        </div>
      {:else}
        <p class="dim" style="margin:10px 0 0;font-size:12px">
          网关是权威，但你在这台机器上的改动不会丢：同步时会做三方合并，
          只有<strong>两边都改过同一条</strong>才会来问你。
        </p>
      {/if}
    </div>
  {/if}

  {#if showConfig && !editing}
    <pre style="margin-bottom:16px">{piConfig}</pre>
  {/if}

  <div class="card tight">
    <div style="padding:12px;border-bottom:3px solid var(--rule)">
      <input
        type="search"
        bind:value={filter}
        placeholder="过滤模型名…"
        style="width:100%;font-family:var(--font-mono);font-size:13px;border:3px solid var(--rule);padding:8px 10px;background:var(--paper);color:var(--ink)"
      />
    </div>

    {#if loading}
      <Skeleton rows={5} shape="table" cols={3} />
    {:else if visible.length === 0}
      <div class="empty">没有匹配的模型。</div>
    {:else if editing}
      <table class="edit">
        <thead>
          <tr>
            <th>模型</th>
            <th>别名</th>
            <th>免费截止</th>
            <th>备注</th>
            <th>思考档位</th>
            <th>隐藏</th>
          </tr>
        </thead>
        <tbody>
          {#each visible as model (model.id)}
            <tr>
              <td>
                <div class="num" style="font-size:12px">{model.id}</div>
                <div style="font-size:11px;color:var(--muted)">{model.display_name}</div>
              </td>
              <td>
                <input bind:value={draft[model.id].alias} placeholder={model.name} />
              </td>
              <td>
                <input
                  bind:value={draft[model.id].free_until}
                  placeholder="YYYY-MM-DD"
                  class="mono"
                />
              </td>
              <td>
                <input bind:value={draft[model.id].note} placeholder="（可选）" />
              </td>
              <td>
                {#if model.thinking && model.efforts?.length}
                  <div class="thinking">
                    {#each THINKING_LEVELS as level (level.key)}
                      <label>
                        <span>{level.label}</span>
                        <select bind:value={draft[model.id].thinking[level.key]}>
                          <option value="">默认</option>
                          <option value="__unsupported__">不支持</option>
                          {#each model.efforts as effort (effort)}
                            <option value={effort}>{effort}</option>
                          {/each}
                        </select>
                      </label>
                    {/each}
                  </div>
                {:else}
                  <span style="color:var(--muted)">—</span>
                {/if}
              </td>
              <td>
                <input type="checkbox" bind:checked={draft[model.id].hidden} />
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <table>
        <thead>
          <tr>
            <th>模型</th>
            <th>费用</th>
            <th>能力</th>
            <th class="num">上下文</th>
            <th class="num">输出上限</th>
          </tr>
        </thead>
        <tbody>
          {#each visible as model (model.id)}
            <tr>
              <td>
                <div class="num" style="font-size:13px">{model.id}</div>
                {#if model.name.toLowerCase() !== model.id.toLowerCase()}
                  <div style="font-size:11px;color:var(--muted)">{model.name}</div>
                {/if}
                {#if model.alias}
                  <div style="font-size:11px;color:var(--muted)">别名：{model.alias}</div>
                {/if}
              </td>
              <!-- 费用标记排成一行：多行会让整张表的高度被最啰嗦的那一行决定。 -->
              <td>
                <div class="fee">
                  {#if model.free}
                    <span class="tag free">免费</span>
                  {:else if model.credits}
                    <span class="tag paid">{model.credits}</span>
                  {:else}
                    <span style="color:var(--muted)">—</span>
                  {/if}
                  {#if model.badge}
                    <span class="tag promo">{model.badge}</span>
                  {/if}
                  {#if model.free_until}
                    <span class="num fee-until">至 {fmt.compactDate(model.free_until)}</span>
                  {/if}
                </div>
              </td>
              <td>
                <div style="display:flex;gap:4px;flex-wrap:wrap">
                  {#if model.thinking}<span class="tag">思考</span>{/if}
                  {#if model.images}<span class="tag">图片</span>{/if}
                  {#if model.hidden}<span class="tag blocked">已隐藏</span>{/if}
                </div>
              </td>
              <td class="num">{fmt.tokens(model.context)}</td>
              <td class="num">{fmt.tokens(model.max_output)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>
</section>

<style>
  /* 费用列不分行。窄屏时允许横向滚动，也不能把标签拆开——
     「限时免费」拆成两行读起来像两个词。 */
  .fee {
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
  .fee-until {
    font-size: 11px;
    color: var(--muted);
  }

  /* 编辑表允许横向滚动：列多且都是输入框，挤窄了全都看不清。 */
  table.edit {
    table-layout: auto;
  }
  table.edit input[type="text"],
  table.edit input:not([type]) {
    width: 100%;
    min-width: 120px;
  }
  table.edit input.mono {
    font-family: var(--font-mono);
    min-width: 110px;
  }
  /* 冲突清单：横向可滚，三列内容都可能很长。 */
  .conflicts table {
    width: 100%;
  }

  /* 思考档位：三个小下拉并排，标签在上，跟设置页的参数表格一致。 */
  .thinking {
    display: flex;
    gap: 6px;
  }
  .thinking label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--muted);
  }
  .thinking select {
    font-size: 12px;
  }
</style>
