<script>
  // 抽屉：从右侧滑出的详情面板，替掉「点行只能看一行摘要」的窘境。
  //
  // 设计约束（design.md）：零圆角、硬投影、遮罩只压暗不模糊。
  // 抽屉本身是**不透明**的纸面（身后没有滚动内容可糊），所以不用玻璃——
  // 玻璃在纯色底上糊不出任何东西，只有顶栏和确认框那种「有内容经过」的位置才显形。
  // 这里和卡片、按钮一致：不用玻璃。
  //
  // 动效：入场 180ms ease-out，从 translateX(8px) 起（不是从「无」出现）。
  import { onMount } from "svelte";
  import * as fmt from "./format.js";

  let { event = null, onClose } = $props();

  let closeButton = $state(undefined);

  // 抽屉打开时焦点给关闭按钮；Esc 关闭、点遮罩关闭、Tab 在抽屉内做最小焦点陷阱。
  onMount(() => closeButton?.focus());

  function onKeydown(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
      return;
    }
    if (e.key !== "Tab") return;
    e.preventDefault();
    const order = [...e.currentTarget.querySelectorAll("button, [href], [tabindex]:not([tabindex='-1'])")].filter(
      (el) => !el.disabled,
    );
    if (order.length === 0) return;
    const index = order.indexOf(document.activeElement);
    const next = e.shiftKey
      ? order[(index - 1 + order.length) % order.length]
      : order[(index + 1) % order.length];
    next?.focus();
  }

  // 明细行里「请求正文 / 回复正文」默认折叠，避免长内容把抽屉撑爆。
  let showRequest = $state(false);
  let showResponse = $state(false);

  const hasPayload = $derived(Boolean(event?.request) || Boolean(event?.response));
</script>

<svelte:window onkeydown={onKeydown} />

<!-- 遮罩只压暗，不模糊；点遮罩等于关闭。
     判断「点的是遮罩本身」而不是给抽屉加一个空 click 处理器。 -->
<div
  class="scrim"
  role="presentation"
  onclick={(event) => event.target === event.currentTarget && onClose()}
>
  <div class="drawer" role="dialog" tabindex="-1" aria-modal="true" aria-label="事件详情">
    {#if event}
      <header class="head">
        <div class="title">
          <span class="bracket">[</span> 事件详情 <span class="bracket">]</span>
        </div>
        <button class="action" bind:this={closeButton} onclick={onClose}>关闭 ✕</button>
      </header>

      <div class="body">
        <!-- 概览 -->
        <section class="block">
          <div class="block-title">概览</div>
          <dl class="kv">
            <div><dt>时间</dt><dd>{fmt.time(event.t)}</dd></div>
            <div>
              <dt>设备</dt>
              <dd title={event.device || "本机直连"}>
                {event.device || "本机"}
              </dd>
            </div>
            <div><dt>模型</dt><dd>{event.model ?? "—"}</dd></div>
            <div><dt>方式</dt><dd>{event.stream ? "流式" : "非流式"}</dd></div>
            <div><dt>状态</dt><dd class:err={event.status >= 400}>{event.status ?? "—"}</dd></div>
            <div><dt>耗时</dt><dd>{fmt.duration(event.ms)}</dd></div>
            <div><dt>花费</dt><dd>{fmt.credit(event.credit)}</dd></div>
          </dl>
        </section>

        <!-- 拦截 / 拒答判定 -->
        <section class="block">
          <div class="block-title">判定</div>
          <dl class="kv">
            <div>
              <dt>确定拦截</dt>
              <dd>
                {#if event.blocked}<span class="tag blocked">{event.blocked_reason || "拦截"}</span>{:else}—{/if}
              </dd>
            </div>
            <div>
              <dt>疑似拒答</dt>
              <dd>
                {#if event.refused}
                  <span class="tag promo" title={event.refuse_rule}>疑似拒答</span>
                {:else}—{/if}
              </dd>
            </div>
            <div><dt>结束原因</dt><dd>{event.finish || "—"}</dd></div>
          </dl>
          {#if !hasPayload && !event.blocked && !event.refused}
            <p class="note">这次是正常的对话，请求成功、也没被拦——按设计不记录收发正文，只看得到用量。</p>
          {/if}
        </section>

        <!-- 请求侧 -->
        <section class="block">
          <div class="block-title">请求</div>
          <dl class="kv">
            <div><dt>消息数</dt><dd>{event.req?.messages ?? "—"}</dd></div>
            <div><dt>带工具</dt><dd>{event.req?.has_tools ? "是" : "否"}</dd></div>
            <div><dt>最大输出</dt><dd>{event.req?.max_tokens ? fmt.tokens(event.req.max_tokens) : "—"}</dd></div>
          </dl>
          {#if event.request}
            <button class="disclosure" onclick={() => (showRequest = !showRequest)}>
              {showRequest ? "▾" : "▸"} 请求正文（失败/被拦才留存）
            </button>
            {#if showRequest}
              <pre class="payload">{event.request}</pre>
            {/if}
          {/if}
        </section>

        <!-- 响应侧 -->
        <section class="block">
          <div class="block-title">响应</div>
          <dl class="kv">
            <div><dt>输入 token</dt><dd>{fmt.tokens(event.in)}</dd></div>
            <div><dt>缓存命中</dt><dd>{fmt.tokens(event.cached)}</dd></div>
            <div><dt>输出 token</dt><dd>{fmt.tokens(event.out)}</dd></div>
            {#if event.tools?.length}
              <div><dt>调用工具</dt><dd>{event.tools.join(" · ")}</dd></div>
            {/if}
          </dl>
          {#if event.response}
            <button class="disclosure" onclick={() => (showResponse = !showResponse)}>
              {showResponse ? "▾" : "▸"} 回复正文（失败/被拦才留存）
            </button>
            {#if showResponse}
              <pre class="payload">{event.response}</pre>
            {/if}
          {/if}
        </section>

        <!-- 限流 -->
        {#if event.rate_limited}
          <section class="block">
            <div class="block-title">频率限制</div>
            <dl class="kv">
              <div><dt>模型</dt><dd>{event.model}</dd></div>
              <div><dt>恢复时间</dt><dd class="reset">{event.rate_reset_at || "后端未给出"}</dd></div>
            </dl>
            <p class="note">
              这不是内容拦截，是后端对<strong>单个模型</strong>的频率上限。可以等上面那个时间到，
              或<strong>直接换一个模型</strong>继续用——其他模型不受影响。
            </p>
          </section>
        {/if}

        <!-- 上游错误 -->
        {#if event.upstream}
          <section class="block">
            <div class="block-title">上游错误</div>
            <dl class="kv">
              <div><dt>HTTP</dt><dd class:err={event.upstream.http >= 400}>{event.upstream.http}</dd></div>
              <div><dt>业务码</dt><dd>{event.upstream.code || "—"}</dd></div>
              <div><dt>信息</dt><dd>{event.upstream.msg || "—"}</dd></div>
            </dl>
          </section>
        {/if}
      </div>
    {:else}
      <div class="empty">没有选中任何事件。</div>
    {/if}
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 100;
    background: color-mix(in srgb, var(--paper) 55%, transparent);
    display: flex;
    justify-content: flex-end;
  }

  .drawer {
    width: min(560px, 92vw);
    height: 100%;
    background: var(--paper);
    border-left: 5px solid var(--rule);
    /* 硬投影：抽屉浮在页面之上靠的是错位，不是柔光。 */
    box-shadow: -8px 0 0 color-mix(in srgb, var(--rule) 22%, transparent);
    display: flex;
    flex-direction: column;
    transform: translateX(0);
    transition: transform 180ms ease-out;
  }

  @starting-style {
    .drawer {
      transform: translateX(8px);
    }
  }

  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 20px;
    border-bottom: 3px solid var(--rule);
  }

  .title {
    font-size: 18px;
    letter-spacing: -0.01em;
  }

  .bracket {
    color: var(--muted);
  }

  .body {
    overflow-y: auto;
    padding: 20px;
    flex: 1;
  }

  .block {
    margin-bottom: 24px;
  }

  .block-title {
    font-family: var(--mono);
    font-size: 12px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    border-left: 3px solid var(--rule);
    padding-left: 8px;
    margin-bottom: 12px;
  }

  .kv {
    display: grid;
    grid-template-columns: 1fr;
    gap: 8px;
    margin: 0;
  }

  .kv > div {
    display: flex;
    gap: 12px;
    border-bottom: 1px solid var(--line);
    padding-bottom: 8px;
  }

  .kv dt {
    width: 88px;
    flex: none;
    color: var(--muted);
    font-size: 13px;
  }

  .kv dd {
    margin: 0;
    font-size: 14px;
    word-break: break-word;
  }

  .kv dd.err {
    color: var(--danger-ink);
  }

  .note {
    font-size: 13px;
    color: var(--muted);
    line-height: 1.6;
  }

  .disclosure {
    margin-top: 12px;
    background: transparent;
    border: 1px solid var(--line);
    padding: 6px 10px;
    font-size: 13px;
    cursor: pointer;
    color: var(--ink);
  }

  .disclosure:hover {
    background: color-mix(in srgb, var(--ink) 6%, transparent);
  }

  .payload {
    margin-top: 12px;
    background: color-mix(in srgb, var(--ink) 4%, transparent);
    border: 1px solid var(--line);
    padding: 12px;
    font-family: var(--mono);
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 320px;
    overflow-y: auto;
  }

  .empty {
    margin: auto;
    padding: 40px;
    color: var(--muted);
  }

  .kv dd.reset {
    font-family: var(--mono);
    color: var(--danger-ink);
  }

  @media (prefers-reduced-motion: reduce) {
    .drawer {
      transition: none;
    }
    @starting-style {
      .drawer {
        transform: none;
      }
    }
  }
</style>
