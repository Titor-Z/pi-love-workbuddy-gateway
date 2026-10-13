<script>
  /**
   * 骨架屏。
   *
   * 为什么不用「读取中…」四个字：
   * 一、它和「无数据」长得一样（同一个斜纹盒子），你分不清是在读还是已经读完，
   *    而这两件事该做的事完全相反——一个等，一个去发请求。
   * 二、文字加载态不占位，数据一到页面就整块跳一下。骨架屏按最终形状占好位，
   *    数据填进来时布局不动。
   *
   * 形状按 rows 排，宽度按 widths 给（省略就用默认的错落宽度）——
   * 每行一样宽会像个整齐的色块，交错一点才像文字。
   */
  let {
    rows = 4,
    widths = [92, 74, 84, 61, 88, 70],
    /* rows = 文字段落；table = 表格；block = 一整块（图表那种）。
       表格的骨架必须长成表格的样子（第一列宽、后面几列等宽），
       一整条横杠占位，等真数据进来时布局会整块重排。 */
    shape = "rows",
    cols = 5,
  } = $props();

  const grid = $derived(`2fr repeat(${cols - 1}, 1fr)`);
</script>

<div class="skeleton" aria-busy="true" aria-live="polite">
  <span class="sr-only">读取中</span>
  {#if shape === "block"}
    <div class="block"></div>
  {/if}
  {#each shape === "block" ? [] : Array(rows) as _, index (index)}
    {#if shape === "table"}
      <div class="trow" style="grid-template-columns:{grid}">
        {#each Array(cols) as _, col (col)}
          <div class="bar" style="width:{col === 0 ? 72 : 46}%"></div>
        {/each}
      </div>
    {:else}
      <div class="bar" style="width:{widths[index % widths.length]}%"></div>
    {/if}
  {/each}
</div>

<style>
  .skeleton {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 10px 10px 12px;
  }

  /* 图表的位置就该是一个大方块。摆一排细条等于在说「这里是文字」，
     真图表进来时整块高度会跳。 */
  .block {
    height: 260px;
    background: var(--line);
    animation: skeleton-breathe 1.4s ease-in-out infinite;
  }

  .trow {
    display: grid;
    gap: 16px;
    align-items: center;
  }

  /* 表格版：数值列靠右，跟真表格的数字对齐一致。 */
  .trow .bar:not(:first-child) {
    justify-self: end;
  }

  .bar {
    height: 13px;
    background: var(--line);
    border-radius: 0;
    /* 幅度很小的明暗呼吸。再大就变成闪烁了。
       它的作用只有一个：告诉人「在动」而不是「坏了」。 */
    animation: skeleton-breathe 1.4s ease-in-out infinite;
  }

  /* 错开相位，不然整块一起亮一起灭，像在闪。 */
  .bar:nth-child(2n) {
    animation-delay: 0.22s;
  }
  .bar:nth-child(3n) {
    animation-delay: 0.44s;
  }

  @keyframes skeleton-breathe {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.45;
    }
  }

  /* 前庭敏感的人不该被一块一直呼吸的东西干扰。
     去掉呼吸，但保留占位条——「在加载」这个信息本身必须留下。 */
  @media (prefers-reduced-motion: reduce) {
    .bar {
      animation: none;
      opacity: 0.7;
    }
  }
</style>
