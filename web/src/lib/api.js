// 与网关的 JSON 接口通信。
//
// 凭据来自页面里的 meta：网关每次启动生成一次性随机串注入首页，
// 所有 /api/* 都必须带上它。别的网站读不到我们的 HTML，所以拿不到这个值。

const NONCE_HEADER = "X-Plwg-Nonce";

const NONCE = readNonce();

function readNonce() {
  const meta = document.querySelector('meta[name="plwg-nonce"]');
  return meta ? meta.getAttribute("content") : "";
}

// 网关没起来或者凭据没注入时，要让用户看到明确的原因，
// 而不是一个空表格。
export class ApiError extends Error {
  constructor(message, status, code) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

async function request(method, path, body) {
  const headers = { [NONCE_HEADER]: NONCE };
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let response;
  try {
    response = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (cause) {
    throw new ApiError("连不上网关。确认它还在运行。", 0, "network_error");
  }

  const text = await response.text();
  let payload = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = null;
    }
  }

  if (!response.ok) {
    const detail = payload?.error;
    throw new ApiError(
      detail?.message || `请求失败（HTTP ${response.status}）`,
      response.status,
      detail?.code || "unknown",
    );
  }
  return payload;
}

export const api = {
  ping: () => fetch("/api/ping").then((r) => r.ok),
  status: () => request("GET", "/api/status"),
  billing: () => request("GET", "/api/billing"),
  usage: (days = 14) => request("GET", `/api/usage?days=${days}`),
  models: () => request("GET", "/api/models"),
  events: (limit = 200) => request("GET", `/api/events?limit=${limit}`),
  overrides: () => request("GET", "/api/overrides"),
  saveOverrides: (models) => request("PUT", "/api/overrides", { models }),
  // 只有客户端模式有：网关自己就是权威。
  overridesSync: () => request("GET", "/api/overrides/sync"),
  syncOverrides: (prefer) => request("POST", "/api/overrides/sync", prefer ? { prefer } : {}),
  settings: () => request("GET", "/api/settings"),
  saveSettings: (settings) => request("PUT", "/api/settings", settings),
  relay: () => request("GET", "/api/relay"),
  revokeDevice: (id) => request("POST", `/api/devices/revoke?id=${encodeURIComponent(id)}`),
  unrevokeDevice: (id) => request("POST", `/api/devices/unrevoke?id=${encodeURIComponent(id)}`),
  resetRelayPSK: () => request("POST", "/api/relay/psk", { confirm: true }),
  // 完整密钥按需取：不随列表接口返回，避免被轮询反复拉走。
  revealPSK: () => request("POST", "/api/relay/psk/reveal"),
  // 生成一次性短配对码（网关侧点一下，客户端填进去）。
  issuePairCode: () => request("POST", "/api/relay/paircode"),
  setRelayEnabled: (enabled) => request("POST", "/api/relay/enable", { enable: enabled }),
  // 探测只取指纹、不建立信任：密钥与配对码都不参与，
  // 所以用户在比对指纹之前不需要先把任何凭据交出去。
  pairingProbe: (address) => request("POST", "/api/pairing/probe", { address }),
  pairingConfirm: (address, code, psk, fingerprint) =>
    request("POST", "/api/pairing/confirm", { address, code, psk, fingerprint }),
  sync: () => request("GET", "/api/sync"),
  setSync: (enabled) => request("POST", "/api/sync", { enable: enabled, confirm: true }),
  syncNow: () => request("POST", "/api/sync/now"),
  switchToGateway: () => request("POST", "/api/sync/switch"),
  switchCheck: () => request("GET", "/api/sync/switch-check"),
  stop: () => request("POST", "/api/control/stop"),
  restart: () => request("POST", "/api/control/restart"),
};

// piConfigUrl 是可直接下载 pi 配置的地址。
// 浏览器直接访问下载链接带不上自定义请求头，所以带着凭据取回来再造一个本地链接。
export async function downloadPiConfig() {
  const response = await fetch("/api/models/pi", { headers: { [NONCE_HEADER]: NONCE } });
  if (!response.ok) {
    throw new ApiError(`生成配置失败（HTTP ${response.status}）`, response.status, "pi_config");
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = "models.json";
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export async function fetchPiConfigText() {
  const response = await fetch("/api/models/pi", { headers: { [NONCE_HEADER]: NONCE } });
  if (!response.ok) {
    throw new ApiError(`生成配置失败（HTTP ${response.status}）`, response.status, "pi_config");
  }
  return response.text();
}
