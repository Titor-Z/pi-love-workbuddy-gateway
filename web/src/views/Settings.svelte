<script>
  import { api } from "../lib/api.js";
  import { theme, THEMES } from "../lib/theme.svelte.js";

  let text = $state("{}");
  let path = $state("");
  let error = $state("");
  let notice = $state("");
  let loading = $state(true);
  let saving = $state(false);

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
