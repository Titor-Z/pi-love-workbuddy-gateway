<script>
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
  let pairStep = $state("form"); // form → confirm
  let pairFingerprint = $state("");
  let pairBusy = $state(false);
  let error = $state("");
  let notice = $state("");
  let loading = $state(true);
  // 重置后新生成的密钥，只在本页显示一次，刷新就看不到了。
  let freshKey = $state("");
  let pending = $state(null);

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
      const payload = await api.pairingProbe(pairAddress, pairKey);
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
      await api.pairingConfirm(pairAddress, pairKey, pairFingerprint);
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
      <h3>连接信息</h3>
      <table style="margin-top:12px">
        <tbody>
          <tr>
            <th style="width:120px">转发地址</th>
            <td>
              <span class="mono-small" style="color:var(--ink)">{primaryAddress}</span>
              {#if relayAddresses.length > 1}
                <span class="dim">　（另有 {relayAddresses.length - 1} 个网卡地址）</span>
              {/if}
            </td>
            <td style="width:120px">
              <button class="action" onclick={() => copy(primaryAddress, "addr")}>
                复制地址
              </button>
            </td>
          </tr>
          <tr>
            <th>网关密钥</th>
            <td class="num">{relay.psk_hint || "未配置"}</td>
            <td>
              <button class="action" onclick={askReset}>重置密钥</button>
            </td>
          </tr>
          <tr>
            <th>证书指纹</th>
            <td><span class="mono-small">{relay.fingerprint}</span></td>
            <td>
              <button class="action" onclick={() => copy(relay.fingerprint, "fp")}>
                复制指纹
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="dim" style="margin-top:12px">
        客户端首次连接时比对指纹，一致才建立信任。
      </p>
    </div>

    <div class="card tight" style="margin-top:24px">
      <table>
        <thead>
          <tr>
            <th style="width:200px">设备</th>
            <th>设备 ID</th>
            <th>最近在线</th>
            <th style="width:120px">状态</th>
            <th style="width:120px"></th>
          </tr>
        </thead>
        <tbody>
          {#each relay.devices as device (device.id)}
            <tr>
              <th>{device.name || "未命名"}</th>
              <td class="num">{device.id}</td>
              <td class="num">{device.last_seen ? fmt.time(device.last_seen) : "—"}</td>
              <td>
                {#if device.revoked}
                  <span class="tag">已停用</span>
                {:else}
                  <span class="tag ok">可用</span>
                {/if}
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
      本机还没和网关配对。填入网关控制台「多设备」页显示的<strong>网关密钥</strong>，
      并比对证书指纹——比对过之后连接才成立。
    </div>
    {#if pairStep === "form"}
      <div class="card tight">
        <table>
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
              <th>网关密钥</th>
              <td>
                <input
                  type="text"
                  placeholder="网关控制台显示的那串"
                  bind:value={pairKey}
                  style="width:100%"
                />
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
      <table>
        <tbody>
          <tr>
            <th style="width:200px">网关凭证版本</th>
            <td class="num">epoch {sync.gateway_epoch ?? "—"}</td>
          </tr>
          <tr>
            <th>本机凭证版本</th>
            <td class="num">{sync.local ? `epoch ${sync.local.local_epoch}` : "尚无副本"}</td>
          </tr>
          <tr>
            <th>上次同步</th>
            <td class="num">
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

{#if pending}
  <Confirm
    title={
      pending.kind === "reset"
        ? "重置网关密钥？"
        : pending.kind === "revoke"
          ? "停用这台设备？"
          : pending.kind === "enable"
            ? "开启转发服务？"
            : "关闭转发服务？"
    }
    message={CONFIRM_MESSAGES[pending.kind]}
    confirmLabel={pending.kind === "reset" ? "重置" : "确定"}
    danger={pending.kind === "reset" || pending.kind === "revoke"}
    onConfirm={run}
    onCancel={cancel}
  />
{/if}
