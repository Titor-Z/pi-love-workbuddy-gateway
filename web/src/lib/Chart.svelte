<script>
  // Chart.js 的薄封装：负责建实例、换数据、销毁。
  // 各视图只描述「要画什么」，不碰生命周期。
  import { onMount, onDestroy } from "svelte";
  import Chart from "chart.js/auto";

  let { type, data, options, height = "320px" } = $props();

  let canvas;
  let chart;

  onMount(() => {
    chart = new Chart(canvas, { type, data, options });
  });

  // 数据换了就地更新，不重建实例——重建会闪一下。
  $effect(() => {
    if (!chart) return;
    chart.data = data;
    chart.update();
  });

  onDestroy(() => {
    if (chart) chart.destroy();
    chart = undefined;
  });
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
