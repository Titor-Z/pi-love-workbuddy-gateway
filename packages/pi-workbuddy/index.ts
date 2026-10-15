// pi-workbuddy：把 pi 接到 pi-love-workbuddy-gateway。
//
// 为什么要有它：网关的模型目录里带着我们自己的信息——费用倍率、免费截止日期、
// 别名、「这个模型支持哪些思考档位」。手写 models.json 只能把这些信息拼成
// 一个名字字符串，改一次模型表就要重新复制一遍。用 provider 扩展之后，
// pi 每次打开 /model 都会从网关现拉一次，改完立刻生效。
//
// 地址是固定的：网关（或客户端）永远在本机 8788 上给 pi 服务，
// 两种模式都一样，所以 pi 这边没有任何需要配置的东西。
// 需要临时指向别处时用环境变量 PLWG_BASE_URL。
//
// 密钥：本机那个端口**不校验密钥**（它只听 127.0.0.1，能访问它的程序
// 本来就能读到网关的配置目录）。所以这里填一个占位值就够了，
// 不需要 /login。将来若真加了令牌，把 apiKey 换成 "$PLWG_TOKEN" 即可。
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const DEFAULT_BASE_URL = "http://127.0.0.1:8788/v1";

/** 本机网关的 API 地址。固定值：两种模式都是它。 */
function apiBaseUrl(): string {
  const override = process.env.PLWG_BASE_URL?.trim();
  return (override || DEFAULT_BASE_URL).replace(/\/+$/, "");
}

/** 控制台地址（只绑本机）。用于提示用户去哪里改模型覆盖表。 */
function consoleUrl(): string {
  return "http://127.0.0.1:8789";
}

/** 网关的 /v1/models 响应里我们自己的那部分。 */
interface WorkBuddyMeta {
  display_name?: string;
  thinking_level_map?: Record<string, string | null>;
  source?: {
    name?: string;
    context_window?: number;
    max_tokens?: number;
    images?: boolean;
    thinking?: boolean;
  };
}

interface GatewayModel {
  id: string;
  workbuddy?: WorkBuddyMeta;
}

/** pi 的模型配置。字段与 ProviderChatModelConfig 对齐。 */
interface PiModel {
  id: string;
  name: string;
  reasoning: boolean;
  input: ("text" | "image")[];
  thinkingLevelMap?: Record<string, string | null>;
  /** 费用一律填 0。 */
  cost: { input: number; output: number; cacheRead: number; cacheWrite: number };
  contextWindow: number;
  maxTokens: number;
}

// 费用刻意全填 0：WorkBuddy 的 credit 与 token 不成比例（实测 glm-5.3 约
// 0.77×、minimax 1.0×，找不出统一系数），换算成金额只会得到一个假数。
// 真实消耗在网关控制台的「日志」页看，那边的数字来自服务端返回的 usage。
const ZERO_COST = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

/** 把网关的一条模型转成 pi 的模型配置。 */
function toPiModel(model: GatewayModel): PiModel {
  const meta = model.workbuddy ?? {};
  const source = meta.source ?? {};
  return {
    id: model.id,
    // 展示名由网关算好（别名 + 费用标记 + 活动 + 截止日期）。
    // 拼接规则只写在 Go 那边一处，不在这里重写一遍。
    name: meta.display_name || source.name || model.id,
    reasoning: source.thinking === true,
    input: source.images ? ["text", "image"] : ["text"],
    ...(meta.thinking_level_map ? { thinkingLevelMap: meta.thinking_level_map } : {}),
    cost: ZERO_COST,
    contextWindow: source.context_window || 0,
    maxTokens: source.max_tokens || 0,
  };
}

/** 拉一次模型目录。 */
async function fetchModels(signal: AbortSignal): Promise<PiModel[]> {
  const response = await fetch(`${apiBaseUrl()}/models`, { signal });
  if (!response.ok) {
    throw new Error(`网关返回 HTTP ${response.status}`);
  }
  const payload = (await response.json()) as { data?: GatewayModel[] };
  const models = payload.data ?? [];
  return models.filter((model) => typeof model?.id === "string" && model.id !== "").map(toPiModel);
}

/**
 * 从落盘快照里读回来时，我们只依赖这几个字段。
 * 类型放宽成 Partial：快照是上一次自己写的，但也可能是旧版本写的——
 * 缺字段就补默认值，不猜。
 */
interface StoredModelLike extends Partial<PiModel> {
  id: string;
}

/** 把落盘的条目还原成模型配置。 */
function fromStored(models: readonly StoredModelLike[]): PiModel[] {
  return models.map((model) => ({
    id: model.id,
    name: model.name ?? model.id,
    reasoning: model.reasoning === true,
    input: model.input ?? ["text"],
    ...(model.thinkingLevelMap ? { thinkingLevelMap: model.thinkingLevelMap } : {}),
    cost: model.cost ?? ZERO_COST,
    contextWindow: model.contextWindow ?? 0,
    maxTokens: model.maxTokens ?? 0,
  }));
}

/** 把模型配置补成可落盘的完整条目（pi 的 AnyModel 需要的字段）。 */
function toStored(models: PiModel[]) {
  return models.map((model) => ({
    ...model,
    provider: "workbuddy" as const,
    api: "openai-completions" as const,
    baseUrl: apiBaseUrl(),
  }));
}

/** 探一次网关 /health（注意它不在 /v1 下面）。 */
async function probe(): Promise<{ ok: boolean; version?: string; models: number }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 1500);
  try {
    const base = apiBaseUrl().replace(/\/v1$/, "");
    const response = await fetch(`${base}/health`, { signal: controller.signal });
    if (!response.ok) {
      return { ok: false, models: 0 };
    }
    const health = (await response.json()) as { version?: string };
    let models = 0;
    try {
      models = (await fetchModels(controller.signal)).length;
    } catch {
      // 探活成功但目录拉不动：状态照常报「在线」，模型数留 0。
    }
    return { ok: true, version: health.version, models };
  } catch {
    return { ok: false, models: 0 };
  } finally {
    clearTimeout(timer);
  }
}

export default function (pi: ExtensionAPI) {
  pi.registerProvider("workbuddy", {
    name: "WorkBuddy",
    baseUrl: apiBaseUrl(),
    api: "openai-completions",
    apiKey: "workbuddy",
    // 每次打开 /model 都会调一次，所以控制台里改完模型覆盖表，
    // 回 pi 再打开一次就是新的。
    refreshModels: async (context) => {
      if (!context.allowNetwork) {
        // 离线 / `pi -p` / RPC 这类不发网络请求的场景：用上次落盘的那份。
        // 这里**不能返回空数组**——那会把模型列表清掉，比「用旧的」糟得多。
        return fromStored(context.stored?.models ?? []);
      }
      const models = await fetchModels(context.signal);
      // 落一份快照，让上面那条路下次有东西可用。
      await context.publish({ persist: { models: toStored(models), checkedAt: Date.now() } });
      return models;
    },
  });

  pi.registerCommand("workbuddy", {
    description: "WorkBuddy 网关：连接状态与模型目录",
    handler: async (_args, ctx) => {
      const status = await probe();
      if (!status.ok) {
        ctx.ui.notify(
          `! 没连上本机网关（${apiBaseUrl()}）\n` +
            `先在终端启动它：pi-love-workbuddy-gateway -d\n` +
            `客户端模式就启动客户端，它一样监听本机 8788。`,
          "warning",
        );
        return;
      }
      const version = status.version ? ` v${status.version}` : "";
      ctx.ui.notify(
        `● 网关在线${version} · ${status.models} 个模型 · ${apiBaseUrl()}\n` +
          `模型覆盖表（别名 / 免费截止 / 思考档位）在控制台改：${consoleUrl()}`,
        "info",
      );
    },
  });
}
