# pi-workbuddy

把 pi 接到 [pi-love-workbuddy-gateway](https://github.com/Titor-Z/pi-love-workbuddy-gateway)。

## 它解决什么

不用再手写 `~/.pi/agent/models.json`。

网关的模型目录里带着我们自己的信息——费用倍率、免费截止日期、别名、
「这个模型支持哪些思考档位」。手写配置文件只能把这些拼成一个名字字符串，
控制台里改一次就要重新复制一遍。用扩展之后，**每次打开 `/model` 都会现拉一次**，
改完立刻生效。

## 装

三种都行，效果一样：

```bash
# 1. 直接用（不改任何配置，适合先试试）
pi --extension ./index.ts

# 2. 放进 pi 的扩展目录（长期使用，每个会话都有）
cp -r . ~/.pi/agent/extensions/pi-workbuddy

# 3. 装成包（发布到 npm 之后）
pi install npm:pi-workbuddy
```

## 用

- `/model` 里会出现 `workbuddy` 这一组。
- `/workbuddy` 看网关在不在线、有多少个模型、控制台在哪。

## 不需要配置、也不需要登录

**地址是固定的** `http://127.0.0.1:8788/v1`：网关（网关模式）和客户端
（客户端模式）永远在本机这个端口上给 pi 服务，两种模式都一样。
所以 pi 这边没有地址要填，也没有密钥要登。

需要临时指到别处时用环境变量 `PLWG_BASE_URL`，改完 `/reload`。

本机那个端口**不校验密钥**——它只听 `127.0.0.1`，而能访问它的程序本来
就能直接读网关的配置目录。所以 `apiKey` 填的是占位值。将来若真加了令牌，
把 `apiKey` 换成 `"$PLWG_TOKEN"` 再 `/login` 即可。

## 费用为什么是 0

WorkBuddy 的 `credit` 与 token 不成比例（实测 glm-5.3 约 0.77×、minimax 正好 1.0×，
找不出统一系数），换算成金额只会得到一个**假数**。所以这里如实填 0。
真实消耗去网关控制台的「日志」页看，那边的数字来自服务端返回的 `usage`。

## 开发

```bash
npx -p typescript@5 tsc -p tsconfig.json
```
