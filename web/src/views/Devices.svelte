<script>
  import { api, ApiError } from "../lib/api.js";
  import * as fmt from "../lib/format.js";
  import Confirm from "../lib/Confirm.svelte";

  let relay = $state(null);
  let status = $state(null);
  let sync = $state(null);
  let switchCheck = $state(null);
  let switching = $state(false);
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
      const [relayPayload, statusPayload, syncPayload] = await Promise.all([
        api.relay(),
        api.status(),
        // 客户端模式才有意义；网关模式下这个端点返回 501，忽略即可。
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
  const deviceCount = $derived(relay?.devices?.length ?? 0);

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
  {:else if !isGateway}
    <div class="notice">
      当前是<strong>客户端模式</strong>，这台机器自己不提供多设备接入。
      要连的网关地址、本设备名称见下方。
    </div>
    <div class="card tight">
      <table>
        <tbody>
          <tr>
            <th style="width:200px">运行模式</th>
            <td class="num">客户端（转发到网关，不持有凭证）</td>
          </tr>
          <tr>
            <th>网关地址</th>
            <td class="num">{status.listen_addr ? "见启动参数 -gateway" : "—"}</td>
          </tr>
        </tbody>
      </table>
    </div>
  {:else if !relay}
    <div class="notice">正在读取转发服务状态…</div>
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
            可以切换。在客户端进程的终端里执行
            <code class="copyable">-switch-to-gateway</code>，
            它会用本机这份凭证写一份本地登录态，然后以
            <code class="copyable">-mode gateway</code> 启动。
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
  {:else if !relay.enabled}
    <div class="card tight">
      <p>
        转发服务<strong>未开启</strong>。开启之后，局域网里的其他电脑才能用
        <code>-mode client</code> 连过来，共用一个 WorkBuddy 账号。
      </p>
      <p class="dim">
        默认不开：只在一台机器上用的人不需要多一个对外监听口。
      </p>
      <button class="action primary" onclick={askToggle}>开启转发服务</button>
    </div>
  {:else}
    <div class="grid stats" style="margin-bottom:20px">
      <div class="stat">
        <div class="label"><span>已登记设备</span></div>
        <div class="value" style="font-size:24px">{deviceCount}</div>
        <div class="sub">首次连上自动登记</div>
      </div>
      <div class="stat">
        <div class="label"><span>转发地址</span></div>
        <div class="value" style="font-size:24px">https://{relay.address}</div>
        <div class="sub">客户端用 -gateway 填这个</div>
      </div>
      <div class="stat">
        <div class="label"><span>证书指纹</span></div>
        <div class="value" style="font-size:14px">{relay.fingerprint}</div>
        <div class="sub">客户端首次连接时比对</div>
      </div>
    </div>

    {#if freshKey}
      <div class="notice warn">
        <strong>新的网关密钥（只显示这一次）：</strong>
        <code class="copyable">{freshKey}</code>
        <button class="action" onclick={copyKey}>复制</button>
        把它填进每一台客户端。此前的配对全部失效。
      </div>
    {/if}

    <div class="card tight">
      <table>
        <tbody>
          <tr>
            <th style="width:200px">网关密钥</th>
            <td class="num">{relay.psk_hint || "未配置"}</td>
          </tr>
          <tr>
            <th>接入凭据</th>
            <td class="num">客户端必须带上这个密钥才能连上</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card tight" style="margin-top:20px">
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
              <td colspan="5">还没有设备连上来过。</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <div class="notice warn" style="margin-top:20px">
      <strong>停用不是封禁。</strong>设备名是客户端自己报的，
      持有密钥的人换一个名字就能重连。所以<strong>无法只踢一台</strong>——
      要让人彻底连不上，只能重置密钥，代价是所有设备都要重新填入。
    </div>

    <div class="card tight" style="margin-top:20px">
      <table>
        <tbody>
          <tr>
            <th style="width:200px">凭证同步</th>
            <td class="num">{sync?.enabled ? "已开启" : "已关闭（默认）"}</td>
          </tr>
          <tr>
            <th>网关凭证版本</th>
            <td class="num">epoch {sync?.gateway_epoch ?? "—"}</td>
          </tr>
        </tbody>
      </table>
      <div class="actions" style="margin-top:12px">
        <button class="action" onclick={toggleSync}>
          {sync?.enabled ? "关闭凭证同步" : "开启凭证同步"}
        </button>
      </div>
      <p class="dim" style="margin-top:12px">
        开启后，局域网内持有网关密钥的设备可以<strong>取走你的账号令牌</strong>。
        客户端把它加密存在自己机器上，用于外出时切到网关模式。
        默认关闭：这是比「转发请求」敏感一个量级的操作。
      </p>
    </div>

    <div class="actions" style="margin-top:20px">
      <button class="action danger" onclick={askReset}>重置网关密钥</button>
      <button class="action" onclick={askToggle}>关闭转发服务</button>
    </div>
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
