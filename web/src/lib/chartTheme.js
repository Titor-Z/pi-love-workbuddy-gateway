// Chart.js 的 RawBlock 主题。
//
// 设计系统要求无圆角、厚边框、纯黑白网格，Number 用等宽字体。
// Chart.js 的默认样式与此完全相反，所以这些默认值要逐个覆盖掉。

export const CHART_COLORS = [
  css("--chart-1", "#000000"),
  css("--chart-2", "#0000ff"),
  css("--chart-3", "#800080"),
  css("--chart-4", "#ff00ff"),
  css("--chart-5", "#008080"),
  css("--chart-6", "#8b4513"),
  css("--chart-7", "#4b0082"),
  css("--chart-8", "#808000"),
];

function css(name, fallback) {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name);
  return value.trim() || fallback;
}

// colorFor 按序号取分类色，超出一轮就循环。
export function colorFor(index) {
  return CHART_COLORS[index % CHART_COLORS.length];
}

const FONT = '11px "Space Mono", ui-monospace, monospace';
const TITLE_FONT = '11px "Work Sans", sans-serif';

// baseOptions 是所有图表共享的配置。
export function baseOptions() {
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: { duration: 180 },
    plugins: {
      legend: { display: false },
      tooltip: {
        // 黑底白字的方块，无圆角——与界面其余部分一致。
        backgroundColor: "#000000",
        titleColor: "#ffffff",
        bodyColor: "#ffffff",
        borderColor: "#000000",
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
        border: { color: "#000000", width: 3 },
        ticks: { color: "#000000", font: FONT, maxRotation: 0, autoSkipPadding: 12 },
      },
      y: {
        beginAtZero: true,
        grid: { color: "#000000", lineWidth: 1, drawTicks: false },
        border: { color: "#000000", width: 3 },
        ticks: { color: "#000000", font: FONT, padding: 6 },
      },
    },
  };
}

// stackedBarOptions 给「按模型堆叠的每日消耗」用。
export function stackedBarOptions() {
  const options = baseOptions();
  options.scales.x.stacked = true;
  options.scales.y.stacked = true;
  options.scales.y.title = {
    display: true,
    text: "消耗",
    color: "#000000",
    font: TITLE_FONT,
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
