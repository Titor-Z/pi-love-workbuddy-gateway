// svelte-check 需要一份配置才知道怎么解析 .svelte / .js。
// 这里不引 adapter：面板是嵌进 Go 二进制的静态产物，不走 SvelteKit。
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";

export default {
  preprocess: vitePreprocess(),
};
