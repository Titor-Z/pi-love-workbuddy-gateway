<script>
  import { api } from "../lib/api.js";
  import * as fmt from "../lib/format.js";

  let events = $state([]);
  let error = $state("");
  let loading = $state(true);
  let onlyBlocked = $state(false);
  let onlyChat = $state(false);

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

  const blockedTotal = $derived(events.filter((e) => e.blocked).length);
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
    <button class="action" onclick={() => (onlyChat = !onlyChat)} aria-pressed={onlyChat}>
      只看对话
    </button>
    <button class="action" onclick={() => (onlyBlocked = !onlyBlocked)} aria-pressed={onlyBlocked}>
      只看被拦截
    </button>
    <button class="action" onclick={load} disabled={loading}>刷新</button>
  </div>

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
      <!-- 同样用下标：同一毫秒的两条事件完全可能撞在一起。 -->
      {#each visible as event, index (index)}
        <div class="log-row" class:blocked={event.blocked}>
          <span class="t">{fmt.clock(event.t)}</span>
          <span class="kind">{fmt.kindLabel(event.kind)}</span>
          <span>{event.msg || "—"}</span>
        </div>
      {/each}
    </div>
  {/if}
</section>
