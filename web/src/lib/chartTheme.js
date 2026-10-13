// Chart.js 的 RawBlock 主题。
//
// 设计系统要求无圆角、厚边框、纯黑白网格，数字用等宽字体。
// Chart.js 的默认样式与此完全相反，所以这些默认值要逐个覆盖掉。
//
// 颜色一律从 CSS 变量现取，不在模块加载时算一次——那样切换主题后
// 图表还挂着旧颜色。tokens() 里读了 theme.resolved，调用它的
// $derived 就会建立依赖，换主题时自动重算。

import { theme } from "./theme.svelte.js";

// 兜底值只在 CSS 变量取不到时用（例如样式表还没加载完）。
const FALLBACK_CHARTS = [
  "#000000",
  "#0000ff",
  "#800080",
  "#ff00ff",
  "#008080",
  "#8b4513",
  "#4b0082",
  "#808000",
];

function tokens() {
  theme.resolved; // 关键：把「当前是亮还是暗」变成调用方的依赖
  const style = getComputedStyle(document.documentElement);
  const read = (name, fallback) => style.getPropertyValue(name).trim() || fallback;
  return {
    ink: read("--ink", "#000000"),
    paper: read("--paper", "#ffffff"),
    muted: read("--muted", "#4d4d4d"),
    charts: FALLBACK_CHARTS.map((fallback, index) => read(`--chart-${index + 1}`, fallback)),
  };
}

// ink 返回当前主题下的前景色，柱子的描边与折线都用它。
export function ink() {
  return tokens().ink;
}

// colorFor 按序号取分类色，超出一轮就循环。
export function colorFor(index) {
  const palette = tokens().charts;
  return palette[index % palette.length];
}

const TICK_FONT = '11px "Space Mono", ui-monospace, monospace';

// baseOptions 是所有图表共享的配置。
export function baseOptions() {
  const { ink, paper } = tokens();
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: { duration: 180 },
    plugins: {
      legend: { display: false },
      tooltip: {
        // 黑底白字的方块，无圆角。暗色下反过来：底色用「纸」色，
        // 字用「墨」色——反正要的是跟页面形成反差。
        backgroundColor: ink,
        titleColor: paper,
        bodyColor: paper,
        borderColor: ink,
        borderWidth: 0,
        cornerRadius: 0,
        padding: 10,
        displayColors: true,
        boxWidth: 10,
        boxHeight: 10,
        titleFont: { family: '"Work Sans", sans-serif', size: 12, weight: "bold" },
        bodyFont: { family: '"Space Mono", monospace', size: 12 },
      },
    },
    scales: {
      x: {
        grid: { display: false },
        border: { color: ink, width: 3 },
        ticks: { color: ink, font: TICK_FONT, maxRotation: 0, autoSkipPadding: 12 },
      },
      y: {
        beginAtZero: true,
        grid: { color: ink, lineWidth: 1, drawTicks: false },
        border: { color: ink, width: 3 },
        ticks: { color: ink, font: TICK_FONT, padding: 6 },
      },
    },
  };
}

// stackedBarOptions 给「按模型堆叠的每日消耗」用。
export function stackedBarOptions() {
  const options = baseOptions();
  const { ink } = tokens();
  options.scales.x.stacked = true;
  options.scales.y.stacked = true;
  options.scales.y.title = {
    display: true,
    text: "消耗",
    color: ink,
    font: '11px "Work Sans", sans-serif',
  };
  return options;
}

// lineOptions 给「请求数随时间」用。请求数是整数，y 轴不要出现 0.5。
export function lineOptions() {
  const options = baseOptions();
  options.scales.x.ticks.autoSkip = false;
  options.scales.y.ticks.precision = 0;
  options.elements = { line: { borderWidth: 3, tension: 0 }, point: { radius: 0 } };
  return options;
}
