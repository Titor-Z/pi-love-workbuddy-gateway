<script>
  import { api } from "./lib/api.js";
  import "./lib/theme.svelte.js"; // 引入即应用主题，不必等某个视图加载
  import Overview from "./views/Overview.svelte";
  import Models from "./views/Models.svelte";
  import Settings from "./views/Settings.svelte";
  import Logs from "./views/Logs.svelte";
  import Devices from "./views/Devices.svelte";
  import Status from "./views/Status.svelte";
  import Confirm from "./lib/Confirm.svelte";
  import Logo from "/public/header-logo.png";

  // 路由用 hash：单二进制里的静态页不好做服务端路由，
  // 而 hash 刷新、分享、前后退都能正常工作。
  const ROUTES = [
    { id: "overview", label: "总览", component: Overview },
    { id: "models", label: "模型", component: Models },
    { id: "logs", label: "日志", component: Logs },
    { id: "devices", label: "多设备", component: Devices },
    { id: "settings", label: "设置", component: Settings },
    { id: "status", label: "状态", component: Status },
  ];

  function routeFromHash() {
    const id = location.hash.replace(/^#\/?/, "");
    return ROUTES.find((route) => route.id === id) ?? ROUTES[0];
  }

  let route = $state(routeFromHash());
  let online = $state(false);
  let failure = $state("");
  let version = $state("");
  let busy = $state("");
  // 待确认的动作。null 表示没有对话框。
  let pending = $state(null);

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

  // 两个动作都要先问一次：pi 那边会短暂不可用，而且停止是不可逆的
  // （停了就得回命令行拉起来，面板也跟着没了）。
  const CONFIRMATIONS = {
    stop: {
      title: "停止网关",
      message: "停止之后这个面板也会一起消失，只能回命令行重新启动。pi 那边立刻不可用。",
      confirmLabel: "停止",
      danger: true,
    },
    restart: {
      title: "重启网关",
      message: "网关会退出再拉起来，大约几秒。pi 那边这几秒里不可用。",
      confirmLabel: "重启",
      danger: false,
    },
  };

  async function control(kind) {
    pending = kind;
  }

  async function runControl(kind) {
    pending = null;
    busy = kind;
    try {
      await api[kind]();
      // 停或重启之后连不上了，这是预期结果，等下轮探测把状态翻过来。
      online = false;
    } catch (cause) {
      // 失败要看得到，但不用原生 alert()：放进顶部那条错误带里。
      failure = `${kind === "stop" ? "停止" : "重启"}请求失败：${cause.message}`;
    }
    busy = "";
  }
</script>

<div class="shell">
  <!-- 键盘用户的跳转链接。Tab 第一下就能跳过整个导航。
       平时藏起来，拿到焦点才出现——放在屏幕外而不是 display:none，
       因为 display:none 的元素根本收不到焦点。 -->
  <a class="skip" href="#main">跳到正文</a>

  <header class="topbar">
    <div class="brand">
      <!-- WorkBuddy -->
      <img width="100px" src="{Logo}" alt="pi-love-workbuddy-gateway" />
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

  <main id="main" tabindex="-1">
    {#if failure}
      <div class="notice error">{failure}</div>
    {/if}
    {#if !online}
      <div class="notice error">
        连不上网关。它可能没在运行，或者刚刚被停止 / 重启——稍等几秒，
        或者去终端里重新启动一次。
      </div>
    {/if}
    <route.component />
  </main>

  {#if pending}
    <Confirm
      title={CONFIRMATIONS[pending].title}
      message={CONFIRMATIONS[pending].message}
      confirmLabel={CONFIRMATIONS[pending].confirmLabel}
      danger={CONFIRMATIONS[pending].danger}
      onConfirm={() => runControl(pending)}
      onCancel={() => (pending = null)}
    />
  {/if}

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
