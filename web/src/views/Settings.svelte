<script>
  import { api } from "../lib/api.js";
  import { theme, THEMES } from "../lib/theme.svelte.js";

  // 日志偏好。
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
    settingsError = "";
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
  }

  $effect(() => {
    load();
  });

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
