# pi-workbuddy

把 pi 接到 [pi-love-workbuddy-gateway](https://github.com/Titor-Z/pi-love-workbuddy-gateway)。

## 它解决什么

不用再手写 `~/.pi/agent/models.json`。

网关的模型目录里带着我们自己的信息——费用倍率、免费截止日期、别名、
「这个模型支持哪些思考档位」。手写配置文件只能把这些拼成一个名字字符串，
控制台里改一次就要重新复制一遍。用扩展之后，**每次打开 `/model` 都会现拉一次**，
改完立刻生效。

## 装

**这个扩展内嵌在网关的二进制里，不由这里单独安装。** 网关一条命令把它写进
pi 的扩展目录：

```bash
pi-love-workbuddy-gateway package --export
```

升级网关后重跑一次就是更新。不去 npm、也不公开。

装到别处（给另一台机器导出）用带参数的形式：

```bash
pi-love-workbuddy-gateway package --export /tmp/pi-workbuddy
```

装完重启 pi（或 `/reload`）。目录里只有两个文件：`index.ts` 与这份说明——
扩展是 pi 直接加载的单文件，不需要 `package.json`。

临时试一次也可以直接用 pi 自己的机制：

```bash
pi --extension ./index.ts
```

## 与网关同步更新

扩展要认网关 `/v1/models` 里的字段。两者分开发布的话，版本迟早对不上，
而症状会是「模型列表少了东西」这种不容易一眼看出的事。
所以它跟着二进制走，**版本对不上这件事从设计上就不存在**。

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

改完 `index.ts` 用 pi 自己的类型做一次静态检查。pi 通常是全局装的，
所以先把它的类型链到本地 `node_modules`，再跑 `tsc`：

```bash
cd internal/piplugin
mkdir -p node_modules/@earendil-works
ln -s "$(npm root -g)/@earendil-works/pi-coding-agent" node_modules/@earendil-works/
npx --yes -p typescript@5 tsc -p tsconfig.json
```

`tsconfig.json` 里 `paths` 写的是 `./node_modules/...` 而不是绝对路径——
pi 装在哪台机器上只有那台机器知道，写死会让别人的检查直接失败。

**这个目录里的 `node_modules` 不入库**（只是那个软链接），
所以它也不在 `package --export` 写出的文件里：pi 加载扩展只需要 `index.ts`。
