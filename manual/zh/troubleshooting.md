# 排障

先看这里，再翻日志。每一条都写着"怎么确认"和"怎么解决"。

## 界面打不开

```bash
ennote status
```

- `running: false` → 服务没起来，`ennote start`；如果启动失败，看 `~/.ennote/logs/ennote.log`
- `running: true` 但浏览器打不开 → 检查地址与端口是否和你请求的一致（默认 `http://127.0.0.1:45131`）

## 端口被占用

```
Port 45131 is already in use on 127.0.0.1
```

换端口：`ennote start --port 4200`。或者先确认占用者是不是上一个没停掉的 Ennote。

## 忘了本机密码

```bash
ennote passwd          # 重设
ennote passwd --reset  # 移除密码，下次启动重新设置
```

## 界面上出现"Worker 不可达，只读"

顶部横幅出现 `Worker unreachable — Ennote is read-only until it returns` 时，界面拒绝所有写操作。
这是按照"离线即只读"的策略做的：不会把你的操作排队重放。

确认 Worker 是否真的挂了：

```bash
curl -s http://127.0.0.1:45131/api/worker/v1/health/ready
```

- 有 HTTP 响应（哪怕 401/428）→ Worker 活着，界面会在 5 秒内重试并自动恢复
- 连接被拒绝 → Worker 确实没跑，`ennote stop && ennote start`

!!! note "没网不等于不可用"
    只读与否取决于 **Worker 是否应答**，不是浏览器有没有网络。飞机模式下，本机的 Worker 仍然
    可以正常对话。

## 用主机名访问局域网返回 403

这是 0.6.0 起的 Host 校验。要么改用 IP 访问，要么显式放行主机名：

```bash
ENNOTE_GATE_ALLOWED_HOSTS=myhost.local ennote start
```

详见 [局域网与安全](lan-security.md)。

## 首次安装下载失败

无网络或无法访问 GitHub 时，`ennote` 会回退到本机已有的完整版本：

```bash
ennote status --json   # 看 binaryVersion 与 binaryFallback
```

日志里会有 `warning: failed to install Ennote x.y.z binaries (...); using cached a.b.c`。
如果本机一个完整版本都没有，错误信息里会带上尝试过的 URL 与缓存目录，按提示手工放置二进制，
或把 `ENNOTE_RELEASES_BASE` 指向一个镜像。

## 命令被拒绝执行

降级模式（`ENNOTE_SANDBOX=none`）下，Worker 会静态分析 shell 命令，以下写法一律拒绝：

- 未加引号的通配符（`*`、`?`）——它们可能展开成任意路径
- 花括号展开（`{a,b}`）、变量展开（`$VAR`）、命令替换（`$(...)`）、ANSI-C 引用（`$'...'`）
- 任何拼出 `~/.ennote` 的路径（包括用引号、转义、空引号对拆开写的）

换成字面量路径，或者启用 `bwrap` 沙箱（Linux 默认就启用）。

## 想彻底重来

```bash
ennote stop
mv ~/.ennote ~/.ennote.bak   # 先备份，确认没问题再删
ennote start
```

## 收集诊断信息

```bash
ennote status --json
tail -n 200 ~/.ennote/logs/ennote.log
curl -s http://127.0.0.1:45131/api/health
```

三条一起贴出来，基本能定位绝大多数问题。报 issue 时请附上 `ennote status --json` 里的
`binaryVersion` —— 版本对不上排查会走很多弯路。
