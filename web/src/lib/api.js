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
  promotions: () => request("GET", "/api/promotions"),
  savePromotions: (promotions) => request("PUT", "/api/promotions", { promotions }),
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
