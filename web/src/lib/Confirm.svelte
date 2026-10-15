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
  // 焦点陷阱要查对话框里的按钮，所以得有一个对话框本身的引用。
  // 原来用 event.currentTarget 查——而在 <svelte:window> 上监听时
  // currentTarget 是 window，window 没有 querySelectorAll，一按 Tab 就抛异常。
  let dialog;

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
    const order = [...(dialog?.querySelectorAll("button") ?? [])];
    const index = order.indexOf(document.activeElement);
    const next = event.shiftKey
      ? order[(index - 1 + order.length) % order.length]
      : order[(index + 1) % order.length];
    next?.focus();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- 点遮罩等于取消。role=presentation 是因为遮罩本身不是内容。
     判断「点的是遮罩本身」而不是给对话框加一个空 click 处理器：
     后者会让对话框变成一个「可点击的非交互元素」，无障碍检查也会这么说。 -->
<div
  class="scrim"
  role="presentation"
  onclick={(event) => event.target === event.currentTarget && onCancel()}
>
  <div
    class="dialog"
    bind:this={dialog}
    role="alertdialog"
    tabindex="-1"
    aria-modal="true"
    aria-labelledby="dialog-title"
    aria-describedby="dialog-message"
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
  /* 入场。模态是罕见事件，而且「突然出现」本身就是一个生硬的变化——
     动效的职责正是消除它。
     用 160ms + ease-out：UI 动效必须短，而且进入用 ease-out（ease-in 会
     把用户最关注的那一刻往后拖）。
     从 scale(0.97) 起而不是 scale(0)：没有东西是从「无」里出现的。
     模态在原点上是豁免的（保持居中，不锚定触发物）。 */
  .scrim {
    opacity: 1;
    transition: opacity 160ms ease-out;
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
    opacity: 1;
    transform: scale(1);
    transition: opacity 160ms ease-out, transform 160ms ease-out;
    max-width: 460px;
    width: 100%;
    /* 玻璃 + 轻层圆角。背后是被压暗的页面内容——这里才是它真正显形的地方。 */
    background: var(--paper);
    background: color-mix(in srgb, var(--paper) 82%, transparent);
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
    border: 5px solid var(--rule);
    border-radius: var(--r-layer);
    /* 硬投影：位移，不是柔光。T3 模态用 8px。 */
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

  @starting-style {
    .scrim {
      opacity: 0;
    }
    .dialog {
      opacity: 0;
      transform: scale(0.97);
    }
  }

  /* 减少动态效果时要「更轻」而不是「全无」：
     去掉位移，保留透明度——渐变本身帮助理解，不造成不适。 */
  @media (prefers-reduced-motion: reduce) {
    .dialog {
      transform: none;
      transition: opacity 120ms ease-out;
    }
    @starting-style {
      .dialog {
        transform: none;
      }
    }
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
