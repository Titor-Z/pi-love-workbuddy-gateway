<script>
  import { onMount } from "svelte";
  import { api, ApiError } from "../lib/api.js";
  import * as fmt from "../lib/format.js";
  import Confirm from "../lib/Confirm.svelte";

  let relay = $state(null);
  let status = $state(null);
  let sync = $state(null);
  let switchCheck = $state(null);
  let switching = $state(false);
  // 配对表单：客户端首次连网关时用。
  let pairAddress = $state("");
  let pairKey = $state("");
  let pairCode = $state("");
  let pairStep = $state("form"); // form → confirm
  let pairFingerprint = $state("");
  let pairBusy = $state(false);
  let error = $state("");
  let notice = $state("");
  let loading = $state(true);
  // 重置后新生成的密钥，只在本页显示一次，刷新就看不到了。
  let freshKey = $state("");
  let pending = $state(null);

  // 相对时间需要一个会走的「现在」，否则「刚刚」会一直停在刚刚。
  // 30 秒一次：这一页判断的是「谁连着」，分钟级的准确度足够。
  let now = $state(Date.now());
  onMount(() => {
    const timer = setInterval(() => (now = Date.now()), 30000);
    return () => clearInterval(timer);
  });

  // 二次确认的文案与按钮，按动作查表。
  //
  // 写表而不是在模板里套三元：原先模板里是一串嵌套三元，缺了 `unrevoke`
  // 那一支，「恢复」会显示成「关闭转发服务？」。查表则每个动作必须显式写全，
  // 漏一个就是 undefined，模板会当场渲染不出来——错误在写的时候暴露，
  // 而不是等用户点到那一支才发现。
  const CONFIRMATIONS = {
    revoke: {
      title: "停用这台设备？",
      message:
        "这台设备将无法再经本网关转发请求。设备名由客户端自报，换个 ID 就能重连——要彻底挡住只能重置密钥。",
      label: "停用",
      danger: true,
    },
    unrevoke: {
      title: "恢复这台设备？",
      message: "这台设备可以重新经本网关转发请求。",
      label: "恢复",
    },
    reset: {
      title: "重置网关密钥？",
      message: "所有客户端的配对立即失效，每台都要重新填新密钥。新密钥只显示这一次。",
      label: "重置",
      danger: true,
    },
    enable: {
      title: "开启转发服务？",
      message: "局域网里的其他电脑就能用这个账号了。改完要重启网关才生效。",
      label: "开启",
    },
    disable: {
      title: "关闭转发服务？",
      message: "所有客户端立刻断开，重新开启前用不了。改完要重启网关才生效。",
      label: "关闭",
      danger: true,
    },
  };
  const confirmation = $derived(pending ? CONFIRMATIONS[pending.kind] : null);

  // 在线的判据：最近 2 分钟内有请求。
  // 网关每 30 秒才落盘一次设备表，但接口读的是内存里那份，是实时的，
  // 所以这个窗口不需要给落盘留余量。取 2 分钟而不是 30 秒：局域网的
  // 客户端不是长连接，空闲一会儿是正常的，判成离线会误导人。
  const ONLINE_WINDOW_MS = 2 * 60 * 1000;
  function deviceState(device) {
    if (device.revoked) return { text: "已停用", className: "off" };
    const seen = device.last_seen ? new Date(device.last_seen).getTime() : 0;
    if (seen && now - seen <= ONLINE_WINDOW_MS) return { text: "在线", className: "ok" };
    return { text: "离线", className: "rate" };
  }

  async function load() {
    loading = true;
    error = "";
    try {
      const statusPayload = await api.status();
      // 未配对时 relay / sync 会返回 501，忽略即可——
      // 此时页面显示的是配对表单，不是这些状态。
      const [relayPayload, syncPayload] = await Promise.all([
        api.relay().catch(() => null),
        api.sync().catch(() => null),
      ]);
      relay = relayPayload;
      status = statusPayload;
      sync = syncPayload;
    } catch (cause) {
      error = cause.message;
    }
    loading = false;
  }

  $effect(() => {
    load();
  });

  const isGateway = $derived(status?.mode === "gateway");
  const paired = $derived(status?.paired !== false);
  const deviceCount = $derived(relay?.devices?.length ?? 0);

  // 客户端要填的地址。后端给的是**真实网卡地址**；
  // 拿不到（离线机器、枚举失败）时退回配置里的监听地址——
  // 0.0.0.0 填不了，但至少是个线索，比空白强。
  const relayAddresses = $derived(
    relay?.addresses?.length ? relay.addresses : relay?.address ? [relay.address] : [],
  );
  const primaryAddress = $derived(relayAddresses[0] ?? "—");

  // 配对码的剩余秒数。用它做过期提示，避免用户拿一个已经失效的码去试。
  let clock = $state(Date.now());
  const pairSecondsLeft = $derived.by(() => {
    const raw = relay?.pair_code_expires_at;
    if (!raw) return 0;
    const left = Math.floor((new Date(raw).getTime() - clock) / 1000);
    return left > 0 ? left : 0;
  });
  const pairCodeLive = $derived(Boolean(relay?.pair_code) && pairSecondsLeft > 0);

  // 只在真的有码的时候才每秒跳一次，平时不占资源。
  $effect(() => {
    if (!relay?.pair_code_expires_at) return;
    const timer = setInterval(() => (clock = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  async function issuePairCode() {
    error = "";
    try {
      const payload = await api.issuePairCode();
      relay = { ...relay, pair_code: payload.pair_code, pair_code_expires_at: payload.pair_code_expires_at };
      clock = Date.now();
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
  }

  // 完整密钥默认不显示：它是账号访问权，不该一进页面就铺在屏幕上。
  // 点「显示」才去取，取回来只放在内存里。
  let fullKey = $state("");
  async function revealKey() {
    error = "";
    if (fullKey) {
      fullKey = "";
      return;
    }
    try {
      const payload = await api.revealPSK();
      fullKey = payload.psk || "";
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
  }

  // 复制按钮共用一个：三处要复制的东西格式不同，但反馈是同一套。
  let copied = $state("");
  async function copy(text, what) {
    try {
      await navigator.clipboard.writeText(text);
      copied = what;
      setTimeout(() => (copied = ""), 2000);
    } catch {
      error = "复制失败，请手动选中复制。";
    }
  }

  // 停用只挡一个自报的 ID——客户端换一个 ID 就能绕回来。
  // 所以「重置密钥」才是真正的踢人手段，代价是所有设备都要重新填。
  function askRevoke(device) {
    pending = { kind: "revoke", device };
  }

  function askReset() {
    pending = { kind: "reset" };
  }

  function askToggle() {
    pending = { kind: relay?.enabled ? "disable" : "enable" };
  }

  // 第一步：连上网关取指纹。这一步不建立信任，只把钥匙拿给用户看。
  async function probePairing() {
    pairBusy = true;
    error = "";
    try {
      const payload = await api.pairingProbe(pairAddress);
      if (payload.error) {
        error = payload.error;
      } else {
        pairFingerprint = payload.fingerprint;
        pairAddress = payload.address;
        pairStep = "confirm";
      }
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
    pairBusy = false;
  }

  // 第二步：用户比对过指纹，确认信任。
  async function confirmPairing() {
    pairBusy = true;
    error = "";
    try {
      await api.pairingConfirm(pairAddress, pairCode, pairKey, pairFingerprint);
      notice = "配对完成，正在重启以生效…";
      await api.restart();
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
    pairBusy = false;
  }

  async function syncNow() {
    error = "";
    notice = "";
    try {
      await api.syncNow();
      notice = "已同步。";
      await load();
    } catch (cause) {
      // 409 表示本机凭证比网关新——那是一次需要用户决定的事，
      // 不是网络错误，所以把网关的原话直接显示出来。
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
  }

  async function checkSwitch() {
    switching = true;
    error = "";
    try {
      switchCheck = await api.switchCheck();
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
    switching = false;
  }

  async function doSwitch() {
    switching = true;
    error = "";
    notice = "";
    try {
      const payload = await api.switchToGateway();
      notice = payload.message || "已写好本机凭证。";
      pairStep = "form";
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
    switching = false;
  }

  async function toggleSync() {
    error = "";
    notice = "";
    try {
      await api.setSync(!sync?.enabled);
      notice = sync?.enabled ? "已关闭凭证同步。" : "已开启凭证同步，重启网关后生效。";
      await load();
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
  }

  async function run() {
    const action = pending;
    pending = null;
    if (!action) return;
    error = "";
    notice = "";
    freshKey = "";
    try {
      if (action.kind === "revoke") {
        await api.revokeDevice(action.device.id);
      } else if (action.kind === "unrevoke") {
        await api.unrevokeDevice(action.device.id);
      } else if (action.kind === "reset") {
        const payload = await api.resetRelayPSK();
        freshKey = payload.psk;
      } else if (action.kind === "enable" || action.kind === "disable") {
        await api.setRelayEnabled(action.kind === "enable");
        notice = "已保存。重启网关后生效——页面底部有重启按钮。";
      }
      await load();
    } catch (cause) {
      error = cause instanceof ApiError ? cause.message : String(cause);
    }
  }

  function cancel() {
    pending = null;
  }

  function copyKey() {
    if (!freshKey) return;
    navigator.clipboard?.writeText(freshKey);
    notice = "已复制到剪贴板。";
  }
</script>

<section class="section">
  <header>
    <h2>多设备</h2>
    <button class="action" onclick={load} disabled={loading}>刷新</button>
  </header>

  {#if error}
    <div class="notice error">{error}</div>
  {/if}
  {#if notice}
    <div class="notice">{notice}</div>
  {/if}

  {#if !status}
    <div class="notice">正在读取…</div>
  {:else if isGateway}
    {#if !relay}
      <div class="notice">正在读取转发服务状态…</div>
    {:else if !relay.enabled}
    <div class="card tight">
      <p>
        转发服务<strong>未开启</strong>。开启之后，局域网里的其他电脑才能用
        <code>client</code> 子命令连过来，共用一个 WorkBuddy 账号。
      </p>
      <p class="dim">
        默认不开：只在一台机器上用的人不需要多一个对外监听口。
      </p>
      <button class="action primary" onclick={askToggle}>开启转发服务</button>
    </div>
    {:else}
    <div class="grid stats">
      <!-- 主角：这台机器在局域网里的地址。用户来这一页主要就是为了抄它。 -->
      <div class="stat hero">
        <div class="label"><span>转发地址</span></div>
        <div class="value address" title={primaryAddress}>{primaryAddress}</div>
        <div class="sub">客户端 client -addr 填这个</div>
      </div>
      <div class="stat">
        <div class="label"><span>已登记设备</span></div>
        <div class="value">{deviceCount}</div>
        <div class="sub">首次连上自动登记</div>
      </div>
      <div class="stat">
        <div class="label"><span>转发服务</span></div>
        <div class="value" style="font-size:20px">{relay.enabled ? "已开启" : "已关闭"}</div>
        <div class="sub">控制台可随时开关</div>
      </div>
      <div class="stat">
        <div class="label"><span>凭证同步</span></div>
        <div class="value" style="font-size:20px">{sync?.enabled ? "已开启" : "已关闭"}</div>
        <div class="sub">默认关闭</div>
      </div>
    </div>

    {#if freshKey}
      <div class="notice warn">
        <strong>新的网关密钥（只显示这一次）：</strong>
        <code>{freshKey}</code>
        <button class="action" onclick={() => copy(freshKey, "key")}>复制</button>
        {copied === "key" ? "已复制。" : "把它填进每一台客户端。此前的配对全部失效。"}
      </div>
    {/if}

    <div class="card" style="margin-top:24px">
      <div style="display:flex;align-items:baseline;justify-content:space-between;gap:16px;flex-wrap:wrap">
        <h3>配对码</h3>
        {#if pairCodeLive}
          <span class="dim">剩余 {Math.floor(pairSecondsLeft / 60)}:{String(pairSecondsLeft % 60).padStart(2, "0")}</span>
        {/if}
      </div>
      <p class="dim" style="margin-top:8px">
        在另一台机器的客户端控制台「多设备」页填这 6 位。5 分钟内有效，用过即废。
        不想用短码也可以复制下面的长密钥。
      </p>
      <div class="field-row" style="margin-top:12px">
        {#if pairCodeLive}
          <span class="pair-code" style="user-select:all">{relay.pair_code}</span>
          <button class="action" onclick={() => copy(relay.pair_code.replace("-", ""), "code")}>
            {copied === "code" ? "已复制" : "复制码"}
          </button>
          <button class="action" onclick={issuePairCode}>换一个</button>
        {:else}
          <span class="dim">当前没有有效的配对码。</span>
          <button class="action primary" onclick={issuePairCode}>生成配对码</button>
        {/if}
      </div>
    </div>

    <div class="card" style="margin-top:24px">
      <h3>连接信息</h3>
      <table class="middle" style="margin-top:12px">
        <tbody>
          <!-- 值 + 操作**同一行**。把按钮单独放一栏会让它们竖着堆起来，
               和左边的值对不齐；一行的东西就该在一行上。 -->
          <tr>
            <th style="width:120px">转发地址</th>
            <td>
              <div class="field-row">
                <span class="mono-small" style="color:var(--ink)">{primaryAddress}</span>
                {#if relayAddresses.length > 1}
                  <span class="dim">另有 {relayAddresses.length - 1} 个网卡地址</span>
                {/if}
                <button class="action" onclick={() => copy(primaryAddress, "addr")}>
                  {copied === "addr" ? "已复制" : "复制地址"}
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>网关密钥</th>
            <td>
              <div class="field-row">
                {#if fullKey}
                  <span class="mono-small" style="color:var(--ink);user-select:all">{fullKey}</span>
                {:else}
                  <span class="mono">{relay.psk_hint || "未配置"}</span>
                {/if}
                <button class="action" onclick={revealKey}>
                  {fullKey ? "隐藏" : "显示"}
                </button>
                <button
                  class="action"
                  onclick={() => copy(fullKey, "psk")}
                  disabled={!fullKey}
                >
                  {copied === "psk" ? "已复制" : "复制密钥"}
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>证书指纹</th>
            <td>
              <div class="field-row">
                <span class="mono-small">{relay.fingerprint}</span>
                <button class="action" onclick={() => copy(relay.fingerprint, "fp")}>
                  {copied === "fp" ? "已复制" : "复制指纹"}
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="dim" style="margin-top:12px">
        客户端首次连接时比对指纹，一致才建立信任。
      </p>
    </div>

    <div class="card tight" style="margin-top:24px">
      <table class="middle">
        <thead>
          <tr>
            <th style="width:180px">设备</th>
            <th style="width:120px">设备 ID</th>
            <th style="width:120px">最近在线</th>
            <th style="width:96px">状态</th>
            <th style="width:96px"></th>
          </tr>
        </thead>
        <tbody>
          {#each relay.devices as device (device.id)}
            {@const state = deviceState(device)}
            <tr>
              <th>{device.name || "未命名"}</th>
              <!-- ID 只给前 12 位：32 位十六进制谁也不会去读，却要占掉半张表。
                   12 位与日志页用的是同一个截法，两边能对上号；全文在 title 里。 -->
              <td class="mono" title={device.id}>{fmt.deviceLabel(device.id)}</td>
              <!-- 相对时间 + 悬停看精确值：一排绝对时间戳读不出「是不是刚连过」，
                   而那正是看这一页时要判断的事。nowrap 是因为折行会把行撑高，
                   半行时间戳看起来像坏了。 -->
              <td class="mono nowrap" title={device.last_seen ? fmt.time(device.last_seen) : ""}>
                {fmt.relative(device.last_seen, now)}
              </td>
              <td>
                <span class="tag {state.className}" title="最近 2 分钟内有请求才算在线">
                  {state.text}
                </span>
              </td>
              <td>
                {#if device.revoked}
                  <button class="action" onclick={() => (pending = { kind: "unrevoke", device })}>
                    恢复
                  </button>
                {:else}
                  <button class="action" onclick={() => askRevoke(device)}>停用</button>
                {/if}
              </td>
            </tr>
          {:else}
            <tr>
              <td colspan="5" class="dim">还没有设备连上来过。客户端配对成功后会自动出现在这里。</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <p class="dim" style="margin-top:12px">
      停用只是挡住这个 ID——设备名由客户端自报，换个名字就能重连。
      要让人彻底连不上，只能<strong>重置密钥</strong>，代价是所有设备都要重新填。
    </p>

    <div class="card" style="margin-top:24px">
      <div style="display:flex;align-items:baseline;justify-content:space-between;gap:16px;flex-wrap:wrap">
        <h3>凭证同步</h3>
        <span class="tag">{sync?.enabled ? "已开启" : "已关闭"}</span>
      </div>
      <p class="dim" style="margin-top:8px">
        网关凭证版本 epoch {sync?.gateway_epoch ?? "—"}。
        开启后，局域网内持有网关密钥的设备可以取走你的账号令牌（客户端加密存在自己机器上）。
        这是比「转发请求」敏感一个量级的操作，默认关闭。
      </p>
      <div class="actions" style="margin-top:12px">
        <button class="action" onclick={toggleSync}>
          {sync?.enabled ? "关闭凭证同步" : "开启凭证同步"}
        </button>
      </div>
    </div>

    <div class="actions" style="margin-top:32px">
      <button class="action" onclick={askToggle}>关闭转发服务</button>
    </div>

    {/if}
  {:else if !paired}
    <!-- 客户端首次使用：先配对。密钥与指纹都在这里填、在这里比对，
         不进命令行（会留在 shell history 与 ps 输出里）。 -->
    <div class="notice">
      本机还没和网关配对。填上网关地址，点「连接」——网页会去取网关证书的指纹；
      把它与网关控制台显示的<strong>逐位比对</strong>，一致再填配对码。
      配对码在网关控制台「多设备」页点「生成配对码」得到，6 位，5 分钟内有效。
    </div>
    {#if pairStep === "form"}
      <div class="card tight">
        <table class="middle">
          <tbody>
            <tr>
              <th style="width:200px">网关地址</th>
              <td>
                <input
                  type="text"
                  placeholder="192.168.1.5:8788"
                  bind:value={pairAddress}
                  style="width:100%"
                />
              </td>
            </tr>
            <tr>
              <th>配对码</th>
              <td>
                <!-- 6 位短码，网关控制台显示的那个。关掉拼写检查与自动大写，
                     否则输入框会在旁边画红波浪线，看着像填错了。 -->
                <input
                  type="text"
                  placeholder="4F7-K92"
                  bind:value={pairCode}
                  spellcheck="false"
                  autocomplete="off"
                  autocapitalize="off"
                  style="width:100%"
                />
                <p class="dim" style="margin-top:6px">
                  在网关控制台「多设备」页点「生成配对码」。5 分钟内有效、用过即废。
                </p>
                <details style="margin-top:6px">
                  <summary class="dim" style="cursor:pointer">改用长密钥</summary>
                  <input
                    type="text"
                    placeholder="网关控制台点「显示」后复制过来的那串"
                    bind:value={pairKey}
                    spellcheck="false"
                    autocomplete="off"
                    autocapitalize="off"
                    style="width:100%;margin-top:6px"
                  />
                </details>
              </td>
            </tr>
          </tbody>
        </table>
        <div class="actions" style="margin-top:12px">
          <button class="action primary" onclick={probePairing} disabled={pairBusy}>
            {pairBusy ? "连接中…" : "连接"}
          </button>
        </div>
      </div>
    {:else}
      <div class="card tight">
        <p>请到网关控制台的「多设备」页，核对下面这串指纹是否<strong>逐位一致</strong>：</p>
        <p><code class="copyable">{pairFingerprint}</code></p>
        <p class="dim">
          不一致就<strong>不要</strong>确认——那说明你连上的不是你的网关。
        </p>
        <div class="actions" style="margin-top:12px">
          <button class="action primary" onclick={confirmPairing} disabled={pairBusy}>
            {pairBusy ? "保存中…" : "指纹一致，确认"}
          </button>
          <button class="action" onclick={() => (pairStep = "form")} disabled={pairBusy}>
            返回
          </button>
        </div>
      </div>
    {/if}
  {:else if sync}
    <!-- 客户端模式：显示凭证同步状态与能否切回网关模式。 -->
    <div class="card tight">
      <p>
        本机是<strong>客户端</strong>，凭证由网关同步过来、加密存在本机。
        它只在切到网关模式时才被使用。
      </p>
      <table class="middle">
        <tbody>
          <tr>
            <th style="width:200px">网关凭证版本</th>
            <td class="mono">epoch {sync.gateway_epoch ?? "—"}</td>
          </tr>
          <tr>
            <th>本机凭证版本</th>
            <td class="mono">{sync.local ? `epoch ${sync.local.local_epoch}` : "尚无副本"}</td>
          </tr>
          <tr>
            <th>上次同步</th>
            <td class="mono">
              {sync.local?.last_sync_at ? fmt.time(sync.local.last_sync_at) : "—"}
            </td>
          </tr>
        </tbody>
      </table>
      <div class="actions" style="margin-top:12px">
        <button class="action" onclick={syncNow}>立即同步</button>
        <button class="action" onclick={checkSwitch} disabled={switching}>
          {switching ? "检查中…" : "检查能否切到网关模式"}
        </button>
      </div>
      {#if switchCheck}
        {#if switchCheck.can_switch}
          <div class="notice" style="margin-top:12px">
            可以切换。点下面的按钮会用本机这份凭证写一份本地登录态，
            之后停掉本进程、<strong>不带 <code>client</code> 子命令重新启动</strong>即可。
          </div>
          <div class="actions" style="margin-top:12px">
            <button class="action primary" onclick={doSwitch} disabled={switching}>
              {switching ? "处理中…" : "切换到网关模式"}
            </button>
          </div>
        {:else}
          <div class="notice warn" style="margin-top:12px">
            {switchCheck.reason}
          </div>
        {/if}
      {/if}
    </div>

    <div class="notice warn" style="margin-top:20px">
      切到网关模式后，本机会<strong>开始自己刷新令牌</strong>。
      回连局域网时若面板提示凭证冲突，说明网关那份更旧——由你选择用哪一份。
      本机凭证比网关新时，同步不会覆盖它，也不会悄悄丢掉。
    </div>
  {:else}
    <div class="notice">正在读取凭证同步状态…</div>
  {/if}
</section>

{#if confirmation}
  <Confirm
    title={confirmation.title}
    message={confirmation.message}
    confirmLabel={confirmation.label}
    danger={confirmation.danger ?? false}
    onConfirm={run}
    onCancel={cancel}
  />
{/if}
