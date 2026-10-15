<script>
  import { api } from "../lib/api.js";
  import { theme, THEMES } from "../lib/theme.svelte.js";

  let text = $state("{}");
  let path = $state("");
  let error = $state("");
  let notice = $state("");
  let loading = $state(true);
  let saving = $state(false);

  // 日志偏好与促销配置分开管理：各有自己的提示与保存按钮，
  // 免得用户以为保存一个会把另一个也写进去。
  let logSettings = $state({ log_page_size: 10, log_retention_days: 7 });
  let ranges = $state({
    min_page_size: 5,
    max_page_size: 500,
    min_retention_days: 1,
    max_retention_days: 365,
  });
  let settingsPath = $state("");
  let settingsNotice = $state("");
  let settingsError = $state("");
  let savingSettings = $state(false);

  async function load() {
    loading = true;
    error = "";
    try {
      const payload = await api.promotions();
      // 序列化回文本时保留缩进，让这个框里看到的和文件里的一致。
      text = JSON.stringify(payload.promotions, null, 2);
      path = payload.path;
    } catch (cause) {
      error = cause.message;
    }
    try {
      // 区间由后端下发，不在前端重写一份——重写就会慢慢漂移。
      const payload = await api.settings();
      if (payload?.settings) {
        logSettings = payload.settings;
        ranges = payload;
        settingsPath = payload.path;
      }
    } catch (cause) {
      settingsError = cause.message;
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  // 本地先解析一次：语法错误要在点保存之前就告诉用户，
  // 而不是等接口返回 400。
  const parsed = $derived.by(() => {
    try {
      const value = JSON.parse(text);
      if (value === null || typeof value !== "object" || Array.isArray(value)) {
        return { error: "最外层必须是一个对象，键是模型 id。" };
      }
      for (const [key, entry] of Object.entries(value)) {
        if (entry === null || typeof entry !== "object" || Array.isArray(entry)) {
          return { error: `「${key}」的值必须是一个对象。` };
        }
      }
      return { value };
    } catch (cause) {
      return { error: `JSON 语法错误：${cause.message}` };
    }
  });

  async function save() {
    notice = "";
    error = "";
    if (parsed.error) {
      error = parsed.error;
      return;
    }
    saving = true;
    try {
      await api.savePromotions(parsed.value);
      notice = "已保存，模型页的费用标记立即生效，不需要重启。";
      await load();
    } catch (cause) {
      error = cause.message;
    }
    saving = false;
  }

  function format() {
    if (parsed.error) {
      error = parsed.error;
      return;
    }
    error = "";
    text = JSON.stringify(parsed.value, null, 2);
  }

  async function saveLogSettings() {
    settingsNotice = "";
    settingsError = "";
    savingSettings = true;
    try {
      const payload = await api.saveSettings(logSettings);
      logSettings = payload.settings;
      settingsNotice = "已保存。日志页立即按新的每页条数显示；超出保留期的日志已经清掉。";
    } catch (cause) {
      settingsError = cause.message;
    }
    savingSettings = false;
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
    <h2>外观</h2>
    <span class="hint">保存在本机浏览器里，不上传</span>
  </header>

  <div class="card">
    <div class="bar">
      {#each THEMES as item (item.id)}
        <button
          class="action"
          aria-pressed={theme.preference === item.id}
          onclick={() => theme.set(item.id)}
        >
          {item.label}
        </button>
      {/each}
    </div>
    <p style="font-size:12px;margin:10px 0 0;color:var(--muted)">
      当前显示：{theme.resolved === "dark" ? "暗色" : "亮色"}。
      {#if theme.preference === "system"}
        跟随系统时会随系统设置自动切换。
      {/if}
    </p>
  </div>
</section>

<section class="section">
  <header>
    <h2>日志</h2>
    <span class="hint">{settingsPath || "读取中"}</span>
  </header>

  {#if settingsError}
    <div class="notice error">{settingsError}</div>
  {/if}
  {#if settingsNotice}
    <div class="notice ok">{settingsNotice}</div>
  {/if}

  <div class="card">
    <div class="bar" style="gap:32px;align-items:flex-end">
      <label class="field">
        <span>列表显示条数</span>
        <input
          type="number"
          bind:value={logSettings.log_page_size}
          min={ranges.min_page_size}
          max={ranges.max_page_size}
        />
      </label>
      <label class="field">
        <span>日志保留天数</span>
        <input
          type="number"
          bind:value={logSettings.log_retention_days}
          min={ranges.min_retention_days}
          max={ranges.max_retention_days}
        />
      </label>
      <button class="action primary" onclick={saveLogSettings} disabled={savingSettings}>
        {savingSettings ? "保存中…" : "保存"}
      </button>
    </div>
    <p class="dim" style="margin:12px 0 0">
      日志页每页显示 {logSettings.log_page_size} 条；超过保留期的日志会被删除。
      日志按天分片存放（一个文件就是一天），所以清理是直接删掉整个旧文件，
      不会去重写当前日志。<strong>删掉就找不回来了。</strong>
    </p>
    <p class="dim" style="margin:6px 0 0">
      允许范围：条数 {ranges.min_page_size}～{ranges.max_page_size}，
      保留 {ranges.min_retention_days}～{ranges.max_retention_days} 天。
    </p>
  </div>
</section>

<section class="section">
  <header>
    <h2>促销配置</h2>
    <span class="hint">{path || "读取中"}</span>
  </header>

  <div class="notice warn">
    这里写的是 <strong>WorkBuddy 官方的活动</strong>，不是本工具的功能。
    填写之后模型页会多出「免费 / 限时免费 / 至某日」的标记，纯粹为了好看，
    不改变任何计费行为——真实扣减以服务端返回的 <code>credit</code> 为准。
    活动随时可能变，请以官方公告为准。
  </div>

  <div class="card" style="margin-bottom:16px">
    <p style="font-size:13px;margin:0 0 10px">
      键是模型 id，值支持三个字段：<code>free</code>（是否免费）、
      <code>free_until</code>（截止日期，<code>YYYY-MM-DD</code>）、
      <code>note</code>（备注）。示例：
    </p>
    <!-- 花括号在这里是 JSON 的字面字符，不参与模板求值，所以用实体转义。 -->
    <pre style="font-size:12px">&#123;
  "glm-5.3": &#123; "free_until": "2026-10-31", "note": "限时免费" &#125;,
  "minimax-m3": &#123; "free": true &#125;
&#125;</pre>
  </div>

  <div class="bar" style="margin-bottom:12px">
    <button class="action" onclick={save} disabled={loading || saving || !!parsed.error}>
      {saving ? "保存中…" : "保存"}
    </button>
    <button class="action" onclick={format} disabled={!!parsed.error}>格式化</button>
    <button class="action" onclick={load} disabled={loading || saving}>放弃改动并重读</button>
    {#if parsed.error}
      <span class="tag blocked">有错误，不能保存</span>
    {:else}
      <span class="tag free">{Object.keys(parsed.value).length} 条</span>
    {/if}
  </div>

  <textarea bind:value={text} rows="20" spellcheck="false"></textarea>
</section>

<style>
  /* 设置项：标签在上、输入框在下，多项横排。
     用 flex 而不是表格——这里不是「标签: 值」的键值表，是一组可编辑的参数。 */
  .field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .field > span {
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.12em;
  }
  .field input {
    width: 140px;
  }
</style>
