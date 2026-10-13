import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

const GATEWAY = "http://127.0.0.1:8788";

// 开发模式下从正在运行的网关借来页内凭据。
//
// 生产环境里这个凭据是网关生成并注入 HTML 的，而 `npm run dev` 时页面是
// Vite 发的，拿不到它。所以这里去网关首页抄一份——只在开发时生效，
// 而且要求网关已经跑着（本来也要跑着才有接口可用）。
function devNonce() {
  return {
    name: "plwg-dev-nonce",
    apply: "serve",
    async transformIndexHtml(html) {
      try {
        const body = await (await fetch(`${GATEWAY}/`)).text();
        const match = body.match(/name="plwg-nonce" content="([a-f0-9]+)"/);
        if (match) return html.replaceAll("__PLWG_NONCE__", match[1]);
      } catch {
        // 网关没起来，让它保持占位符——请求会拿到 403，
        // 页面上会显示明确的提示，比一个白屏好排查。
      }
      return html;
    },
  };
}

// 构建产物直接写进 Go 那个嵌入包。
// go:embed 不能引用上级目录，所以嵌入的资源必须由拥有它的包自己声明：
// 前端源码在这里，产物在 ../internal/webui/dist。
export default defineConfig({
  plugins: [svelte(), devNonce()],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    // 关掉 sourcemap：多出来的 .map 会被一起嵌进二进制，
    // 而用户拿到的发行版不需要反向调试前端。
    sourcemap: false,
    target: "es2022",
  },
  server: {
    proxy: { "/api": { target: GATEWAY, changeOrigin: true } },
  },
});
