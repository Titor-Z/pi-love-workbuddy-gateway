<script>
  import { api } from "./lib/api.js";
  import Overview from "./views/Overview.svelte";
  import Models from "./views/Models.svelte";
  import Settings from "./views/Settings.svelte";
  import Logs from "./views/Logs.svelte";
  import Status from "./views/Status.svelte";

  // 路由用 hash：单二进制里的静态页不好做服务端路由，
  // 而 hash 刷新、分享、前后退都能正常工作。
  const ROUTES = [
    { id: "overview", label: "总览", component: Overview },
    { id: "models", label: "模型", component: Models },
    { id: "logs", label: "日志", component: Logs },
    { id: "settings", label: "设置", component: Settings },
    { id: "status", label: "状态", component: Status },
  ];

  function routeFromHash() {
    const id = location.hash.replace(/^#\/?/, "");
    return ROUTES.find((route) => route.id === id) ?? ROUTES[0];
  }

  let route = $state(routeFromHash());
  let online = $state(false);
  let version = $state("");
  let busy = $state("");

  $effect(() => {
    const onHashChange = () => (route = routeFromHash());
    addEventListener("hashchange", onHashChange);

    // 每 10 秒探一次后端：重启或崩溃之后页面能自己反映出来，
    // 而不是等用户点了某个按钮才发现。
    const probe = async () => {
      try {
        const response = await fetch("/api/ping");
        const payload = await response.json();
        online = true;
        version = payload.version;
      } catch {
        online = false;
      }
    };
    probe();
    const timer = setInterval(probe, 10000);

    return () => {
      removeEventListener("hashchange", onHashChange);
      clearInterval(timer);
    };
  });

  async function control(kind) {
    const verb = kind === "stop" ? "停止" : "重启";
    if (!confirm(`确认${verb}网关？pi 那边会短暂不可用。`)) return;
    busy = kind;
    try {
      await api[kind]();
      // 停或重启之后连不上了，这是预期结果，等下轮探测把状态翻过来。
      online = false;
    } catch (cause) {
      alert(`${verb}请求失败：${cause.message}`);
    }
    busy = "";
  }
</script>

<div class="shell">
  <header class="topbar">
    <div class="brand">
      WorkBuddy
      {#if version}<span class="ver">{version}</span>{/if}
    </div>

    <nav class="nav">
      {#each ROUTES as item (item.id)}
        <button
          aria-current={route.id === item.id ? "page" : undefined}
          onclick={() => (location.hash = `#/${item.id}`)}
        >
          {item.label}
        </button>
      {/each}
    </nav>

    <div class="spacer"></div>

    <div class="health">
      <span class="dot" class:on={online} class:off={!online}></span>
      {online ? "运行中" : "连不上"}
    </div>
  </header>

  <main>
    {#if !online}
      <div class="notice error">
        连不上网关。它可能没在运行，或者刚刚被停止 / 重启——稍等几秒，
        或者去终端里重新启动一次。
      </div>
    {/if}
    <route.component />
  </main>

  <footer>
    <span>本工具与腾讯 / WorkBuddy 无官方关联</span>
    <span>·</span>
    <span>凭据只存在本机</span>
    <div class="spacer" style="flex:1"></div>
    <button class="action" onclick={() => control("restart")} disabled={!!busy || !online}>
      {busy === "restart" ? "重启中…" : "重启"}
    </button>
    <button class="action danger" onclick={() => control("stop")} disabled={!!busy || !online}>
      {busy === "stop" ? "停止中…" : "停止"}
    </button>
  </footer>
</div>
