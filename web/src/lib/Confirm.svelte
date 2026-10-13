<script>
  // 确认对话框。
  //
  // 替掉原生 confirm()：那个既丑又阻塞，而且和界面是两套风格。
  //
  // 这里是玻璃**真正能显形**的地方 —— 背后是密集的表格与图表，
  // 模糊它才看得出是玻璃。顶栏之外，这是第二个也是最后一个用玻璃的位置。
  //
  // 设计约束（design.md 第八节）：零圆角、硬投影、遮罩不模糊。
  // 遮罩若也做模糊，对话框再模糊一次就没东西可糊了。
  import { onMount } from "svelte";

  let {
    title = "确认",
    message = "",
    confirmLabel = "确定",
    cancelLabel = "取消",
    danger = false,
    onConfirm,
    onCancel,
  } = $props();

  let cancelButton;

  // 焦点给「取消」而不是「确定」：动作用键盘连按两下回车不该造成破坏。
  onMount(() => cancelButton?.focus());

  function onKeydown(event) {
    if (event.key === "Escape") {
      event.preventDefault();
      onCancel();
      return;
    }
    // 最小限度的焦点陷阱：Tab 在两个按钮之间循环，不许跑到后面的页面去。
    if (event.key !== "Tab") return;
    event.preventDefault();
    const order = [...event.currentTarget.querySelectorAll("button")];
    const index = order.indexOf(document.activeElement);
    const next = event.shiftKey
      ? order[(index - 1 + order.length) % order.length]
      : order[(index + 1) % order.length];
    next?.focus();
  }
</script>

<svelte:window {onkeydown} />

<!-- 点遮罩等于取消。role=presentation 是因为遮罩本身不是内容。 -->
<div class="scrim" role="presentation" onclick={onCancel}>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="dialog"
    role="alertdialog"
    aria-modal="true"
    aria-labelledby="dialog-title"
    aria-describedby="dialog-message"
    onclick={(event) => event.stopPropagation()}
  >
    <h2 id="dialog-title" class="dialog-title">[ {title} ]</h2>
    <p id="dialog-message" class="dialog-message">{message}</p>
    <div class="dialog-actions">
      <button class="action" bind:this={cancelButton} onclick={onCancel}>{cancelLabel}</button>
      <button class="action" class:danger onclick={onConfirm}>{confirmLabel}</button>
    </div>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    /* 遮罩只压暗、不模糊：模糊留给对话框自己，
       否则对话框再糊一层就已经没有细节可糊了。 */
    background: color-mix(in srgb, var(--paper) 55%, transparent);
  }

  .dialog {
    max-width: 460px;
    width: 100%;
    /* 玻璃。背后是被压暗的页面内容 —— 这里才是它真正显形的地方。 */
    background: var(--paper);
    background: color-mix(in srgb, var(--paper) 72%, transparent);
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
    border: 5px solid var(--rule);
    /* 硬投影：位移，不是柔光。浮层浮起来靠的是错位。 */
    box-shadow: 8px 8px 0 var(--rule);
    padding: 24px;
  }

  .dialog-title {
    font-size: 20px;
    letter-spacing: -0.01em;
    margin-bottom: 12px;
  }

  .dialog-message {
    font-size: 14px;
    line-height: 1.6;
    margin: 0 0 24px;
  }

  .dialog-actions {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
  }

  /* 有人把系统的「减少透明度」打开了，就别硬上。 */
  @media (prefers-reduced-transparency: reduce) {
    .dialog {
      background: var(--paper);
      backdrop-filter: none;
      -webkit-backdrop-filter: none;
    }
  }
</style>
