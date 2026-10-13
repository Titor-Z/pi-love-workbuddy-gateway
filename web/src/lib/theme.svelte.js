// 主题偏好：亮色 / 暗色 / 跟随系统。
//
// 解析结果写到 <html data-theme>，样式表只需要一份 [data-theme="dark"] 覆盖，
// 不必为「跟随系统」再抄一遍 CSS 或用媒体查询写两份。
//
// 这个文件用 .svelte.js 后缀：里面用了 runes，不这样命名编译不过。

const STORAGE_KEY = "plwg-theme";

// THEMES 的顺序就是设置页里按钮的顺序。
export const THEMES = [
  { id: "light", label: "亮色" },
  { id: "dark", label: "暗色" },
  { id: "system", label: "跟随系统" },
];

function readStored() {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    return THEMES.some((item) => item.id === saved) ? saved : "system";
  } catch {
    // 隐私模式下 localStorage 会抛错。读不到就用跟随系统，不阻断界面。
    return "system";
  }
}

let preference = $state(readStored());
let systemPrefersDark = $state(false);

const query = window.matchMedia("(prefers-color-scheme: dark)");
systemPrefersDark = query.matches;
query.addEventListener("change", (event) => {
  systemPrefersDark = event.matches;
  // 系统自己切了主题，也要淡过去。
  apply({ animate: true });
});

// THEME_FADE_MS 是换主题时给颜色过渡留的时长。
// Apple 的原则里明说了要避免亮暗切换时的亮度突变：白底直接跳黑底是一记
// 眼睛的闷棍，淡过去就没事了。只在切换的那一瞬间挂上过渡，
// 平时不挂——否则悬停、按下这些需要立刻响应的反馈会被拖慢。
const THEME_FADE_MS = 260;

// apply 把解析结果落到 DOM 上。
// 顺带改 color-scheme：原生滚动条、输入框、下拉框的明暗跟它走，
// 不改的话暗色页面里会嵌一条刺眼的白色滚动条。
function apply({ animate = false } = {}) {
  const resolved = preference === "system" ? (systemPrefersDark ? "dark" : "light") : preference;
  fade(animate);
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
}

// fade 临时给全局挂上颜色过渡。
function fade(animate) {
  const root = document.documentElement;
  if (!animate || prefersReducedMotion()) return;
  root.classList.add("theme-fading");
  clearTimeout(fadeTimer);
  fadeTimer = setTimeout(() => root.classList.remove("theme-fading"), THEME_FADE_MS + 40);
}

let fadeTimer;

function prefersReducedMotion() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export const theme = {
  get preference() {
    return preference;
  },
  // resolved 是「现在到底是亮还是暗」，跟随系统时由系统决定。
  get resolved() {
    return preference === "system" ? (systemPrefersDark ? "dark" : "light") : preference;
  },
  set(next) {
    if (!THEMES.some((item) => item.id === next)) return;
    preference = next;
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // 存不下就算了，本次会话仍然生效。
    }
    apply({ animate: true });
  },
};

apply();
