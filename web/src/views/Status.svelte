<script>
  import { api } from "../lib/api.js";
  import * as fmt from "../lib/format.js";

  let status = $state(null);
  let error = $state("");
  let loading = $state(true);
  let now = $state(Date.now());

  async function load() {
    loading = true;
    error = "";
    try {
      status = await api.status();
    } catch (cause) {
      error = cause.message;
    }
    loading = false;
  }

  $effect(() => {
    load();
    // 运行时长每秒自己走，不必反复请求后端。
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  const uptimeSeconds = $derived.by(() => {
    if (!status) return 0;
    // 用本地时钟差值算，比直接读 uptime_seconds 更平滑。
    return Math.max(status.uptime_seconds, Math.floor((now - new Date(status.started_at).getTime()) / 1000));
  });

  const tokenExpiry = $derived.by(() => {
    const value = status?.account?.token_expires_at;
    if (!value) return null;
    return new Date(value);
  });
</script>

{#if error}
  <div class="notice error">{error}</div>
{/if}

<section class="section">
  <header>
    <h2>运行状态</h2>
    <button class="action" onclick={load} disabled={loading}>刷新</button>
  </header>

  {#if !status}
    <div class="empty">读取中…</div>
  {:else}
    <div class="grid stats" style="margin-bottom:20px">
      <div class="stat">
        <div class="label"><span>运行时长</span></div>
        <div class="value" style="font-size:24px">{fmt.uptime(uptimeSeconds)}</div>
        <div class="sub">启动于 {fmt.time(status.started_at)}</div>
      </div>
      <div class="stat">
        <div class="label"><span>进程号</span></div>
        <div class="value" style="font-size:24px">{status.pid}</div>
        <div class="sub">点重启会换一个</div>
      </div>
      <div class="stat">
        <div class="label"><span>版本</span></div>
        <div class="value" style="font-size:24px">{status.version}</div>
        <div class="sub">开发版显示 dev</div>
      </div>
      <div class="stat" class:alert={status.account?.token_expired}>
        <div class="label"><span>登录态</span></div>
        <div class="value" style="font-size:24px">
          {status.account?.token_expired ? "已过期" : "有效"}
        </div>
        <div class="sub">
          {#if tokenExpiry}有效期至 {fmt.time(tokenExpiry)}{:else}无法读取{/if}
        </div>
      </div>
    </div>

    <div class="card tight">
      <table>
        <tbody>
          <tr>
            <th style="width:200px">监听地址</th>
            <td class="num">{status.listen_addr}</td>
          </tr>
          <tr>
            <th>pi 用的地址</th>
            <td class="num">http://{status.listen_addr}/v1</td>
          </tr>
          <tr>
            <th>登录态文件</th>
            <td class="num">{status.auth_file}</td>
          </tr>
          <tr>
            <th>文本日志</th>
            <td class="num">{status.log_path}</td>
          </tr>
          <tr>
            <th>事件文件</th>
            <td class="num">{status.journal_path}</td>
          </tr>
          <tr>
            <th>促销配置</th>
            <td class="num">{status.promotions_path}</td>
          </tr>
          <tr>
            <th>账号标识</th>
            <td class="num">{status.account?.uid || "未读到"}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="notice warn" style="margin-top:20px">
      这四个文件里<strong>都不包含令牌明文</strong>。登录态是加密保存的，
      日志只记录模型、用量、耗时这类信息。令牌续期时会写回登录态文件，
      这是为了让 pi 可以长期免登录使用。
    </div>
  {/if}
</section>
