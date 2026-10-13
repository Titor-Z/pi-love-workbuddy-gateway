<script>
  // Chart.js 的薄封装：负责建实例、换数据、在换主题时重建。
  // 各视图只描述「要画什么」，不碰生命周期。
  import { onDestroy, untrack } from "svelte";
  import Chart from "chart.js/auto";
  import { theme } from "./theme.svelte.js";

  let { type, data, options, height = "320px" } = $props();

  let canvas;
  let chart;

  function build() {
    chart?.destroy();
    chart = new Chart(canvas, { type, data, options });
  }

  // 只依赖主题：网格线、刻度、柱子的描边颜色都是建实例时定下的，
  // 就地改 options 容易漏掉 Chart.js 内部派生出的缓存，重建最省心。
  // untrack 是为了不让 data / options 也变成依赖——那两个走下面的更新路径。
  $effect(() => {
    theme.resolved;
    untrack(build);
  });

  // 数据换了就地更新，不重建——重建会闪一下。
  $effect(() => {
    if (!chart || !data) return;
    chart.data = data;
    chart.update();
  });

  onDestroy(() => chart?.destroy());
</script>

<div class="chart-box" class:short={height === "260px"} style="height:{height}">
  <canvas bind:this={canvas}></canvas>
</div>

<style>
  /* 让画布自适应容器高度：Chart.js 的 responsive 只认父元素尺寸。 */
  .chart-box {
    position: relative;
  }
  canvas {
    position: absolute;
    inset: 0;
  }
</style>
