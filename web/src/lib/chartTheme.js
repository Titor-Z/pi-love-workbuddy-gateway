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

// 有人把「减少动态效果」打开了就没有入场动画。
// 注意不是「把动画调快」——前庭敏感的人要的是不动，不是动得快。
function motionOff() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

// baseOptions 是所有图表共享的配置。
export function baseOptions() {
  const { ink, paper } = tokens();
  return {
    responsive: true,
    maintainAspectRatio: false,
    // 数据更新用 180ms（见 design.md 的动效表）。入场是另一回事，
    // 由 lineOptions 单独覆盖。
    animation: { duration: 180 },
    // 悬停才算「有反应」。默认的 intersect:true 要求指针正好压在
    // 那个数据元素上——折线只有 3px 宽，等于点不中。
    // index 模式：指针落在这一列的任意高度都给出该点的提示。
    interaction: { mode: "index", intersect: false },
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

/**
 * lineOptions 给「请求数随时间」用。
 *
 * 这张图之前是死的，罪魁是 `point: { radius: 0 }`：点全部关掉之后，
 * 折线只剩一条 3px 宽的线段，Chart.js 默认又要求指针正好压在线段上
 * 才给提示——两个条件叠起来，鼠标怎么扫都没有任何反应。
 * 数据里 14 天有 13 天是 0，那条线就贴着 x 轴一动不动，看着像张图。
 *
 * 所以：点必须画出来，而且方块不圆（本系统圆角一律为 0）。
 */
export function lineOptions() {
  const options = baseOptions();
  // 用日期而不是「第几天」，跟上面那张消耗柱状图的横轴对齐。
  // 同一页两张图两套横轴，读的人得自己在脑子里换算。
  //
  // autoSkipPadding 那条路走不通：抽稀是「每两个留一个」，
  // 14 个刻度留下 1/3/5/7/9/11/13——**正好把唯一有数据的那天（14 号）抽掉**，
  // 于是图上最高的那个点没有日期。宁可挤一点也不能丢最后一天。
  options.scales.x.ticks.autoSkip = false;
  options.scales.y.ticks.precision = 0;
  options.elements = {
    line: { borderWidth: 3, tension: 0 },
    point: {
      radius: 5,
      hoverRadius: 7,
      // 命中范围比画出来的方块大得多：5px 的方块直接用指针去够是够不准的。
      hitRadius: 16,
      pointStyle: "rect",
      borderWidth: 2,
      backgroundColor: tokens().paper,
      borderColor: tokens().ink,
    },
  };

  if (!motionOff()) {
    // 入场：线从左往右画出来，点跟着长出来。
    //
    // 这里给动效是因为它**传达了数据本身的含义**——横轴是时间，
    // 线自左向右展开，正对应「时间在推进」。不是为了点缀。
    // x 的 from 用 NaN：Chart.js 会让每个点从「上一个点的位置」开始，
    // 于是逐段展开而不是整体平移。
    options.animations = {
      x: { type: "number", easing: "linear", duration: 520, from: NaN },
      y: {
        type: "number",
        easing: "linear",
        duration: 520,
        from: (ctx) => {
          // xStarted 保证每个点只从基线起跳一次，否则整条线会一直重画。
          if (ctx.type !== "data" || ctx.xStarted) return 0;
          ctx.xStarted = true;
          return ctx.chart.scales.y.getPixelForValue(0);
        },
      },
    };
  }
  return options;
}
