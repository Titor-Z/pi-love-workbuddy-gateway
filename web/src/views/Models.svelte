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

  async function load() {
    loading = true;
    error = "";
    try {
      models = (await api.models()).models;
    } catch (cause) {
      error = cause.message;
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  const visible = $derived(
    filter
      ? models.filter((m) =>
          `${m.id} ${m.name} ${m.display_name}`.toLowerCase().includes(filter.toLowerCase()),
        )
      : models,
  );

  const freeCount = $derived(models.filter((m) => m.free).length);

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
      {models.length} 个模型，其中 {freeCount} 个免费 · 实时来自 WorkBuddy
    </span>
  </header>

  <div class="card" style="margin-bottom:16px">
    <div class="bar">
      <button class="action" onclick={copyConfig} disabled={loading}>复制 pi 配置</button>
      <button class="action" onclick={download} disabled={loading}>下载 models.json</button>
      <button class="action" onclick={preview} disabled={loading}>
        {showConfig ? "收起预览" : "预览配置"}
      </button>
      <button class="action" onclick={load} disabled={loading}>刷新</button>
    </div>
    <p style="font-size:12px;margin:10px 0 0;color:var(--muted)">
      配置指向 <code>http://127.0.0.1:8788/v1</code>。模型名里带着费用标记，
      pi 的模型选择器会原样显示，一眼能看出哪个免费。
    </p>
  </div>

  {#if showConfig}
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
</style>
