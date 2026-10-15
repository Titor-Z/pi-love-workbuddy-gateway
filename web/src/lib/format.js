// 展示层的格式化。原始数值不做任何有损转换，这里只管怎么读。

// 积分：最多两位小数，去掉无意义的尾零。
// 后端给的是 8 位小数（1.34000001 这种），直接显示会很难读。
export function credit(value) {
  if (value === null || value === undefined) return "—";
  return Number(value).toFixed(2).replace(/\.?0+$/, "");
}

// percent 把 0.0725 读成 7.25%。
export function percent(value) {
  if (!value) return "0%";
  return `${(value * 100).toFixed(value < 0.01 ? 2 : 1)}%`;
}

// tokens 用 k 分隔，8612 读成 8.6k。
export function tokens(value) {
  if (!value) return "0";
  if (value < 1000) return String(value);
  return `${(value / 1000).toFixed(1)}k`;
}

export function duration(millis) {
  if (!millis) return "—";
  if (millis < 1000) return `${millis}ms`;
  return `${(millis / 1000).toFixed(1)}s`;
}

// uptime 把秒数读成「3 小时 12 分」这种。
export function uptime(seconds) {
  if (!seconds) return "刚刚";
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days) return `${days} 天 ${hours} 小时`;
  if (hours) return `${hours} 小时 ${minutes} 分`;
  if (minutes) return `${minutes} 分 ${seconds % 60} 秒`;
  return `${seconds} 秒`;
}

// 时间戳按本地时区显示，精确到秒。
export function time(value) {
  if (!value) return "—";
  const date = new Date(value);
  return date.toLocaleString("zh-CN", { hour12: false });
}

// stamp 是日志里的时间戳：年-月-日 时:分:秒。
//
// 必须带年：日志会跨年留存（保留期上限 365 天），只写「时:分:秒」
// 的话用户无从判断这是哪一年的事。
export function stamp(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const pad = (part) => String(part).padStart(2, "0");
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ` +
    `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
  );
}

// deviceLabel 把设备 ID 缩成一眼能读的前缀。
// 本机直连（pi 直接连网关）没有设备 ID，用「本机」区分于「某台客户端」。
// 完整 ID 由调用方放进 title，悬停可见——日志表里不需要占一整个宽度。
export function deviceLabel(id) {
  if (!id) return "本机";
  return id.length > 12 ? `${id.slice(0, 12)}…` : id;
}

// date 只留年月日。
export function date(value) {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value; // 促销配置里允许直接写日期字面量
  return parsed.toLocaleDateString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" });
}

// shortDate 把 2026-10-14 读成 10-14。
export function shortDate(value) {
  if (!value) return "";
  return value.slice(5);
}

// compactDate 在年份与今年相同时把 2026-10-31 缩成 10-31。
// 费用列要塞在一行里，年份几乎总是今年，占地方又没用；
// 真跨年了还是老老实实写全——省掉会让「01-01」读起来是错的。
export function compactDate(value) {
  if (!value) return "";
  const text = String(value);
  const year = new Date().getFullYear();
  return text.startsWith(`${year}-`) ? text.slice(5) : text;
}

// dayOnly 把 2026-10-14 读成 14。
// 窄容器里的折线图用这个：14 个「10-14」放不下会被 Chart.js 抽稀，
// 而抽稀会把最后一天吞掉，图上就出现了「线跳在 10-13 上」的错觉。
export function dayOnly(value) {
  if (!value) return "";
  return value.slice(8).replace(/^0/, "");
}

// 事件类型的中文名。
const KIND_LABELS = {
  start: "启动",
  stop: "停止",
  chat: "对话",
  models: "目录",
  refresh: "续期",
  error: "异常",
};

export function kindLabel(kind) {
  return KIND_LABELS[kind] || kind;
}
