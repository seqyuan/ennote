# 局域网与安全

默认情况下 Ennote 只监听 `127.0.0.1`，只有本机能访问。这一页讲清楚怎么安全地放开，以及放开之后
哪些东西会变成你的责任。

## 默认形态

```
浏览器 ──HTTP──> ennogate (127.0.0.1:45131) ──HTTP──> ennoworker (loopback)
                 · 校验密码、下发 cookie              · 唯一的业务权威
                 · 托管静态界面                      · 从不监听公网
```

Worker **永远只监听 loopback**，这是硬性约束：它不参与任何账号、Token 或云端逻辑，
所以云端出问题不会影响它。

## 让局域网能访问

```bash
ENNOTE_HOSTNAME=0.0.0.0 ENNOTE_ALLOW_LAN=1 \
ENNOTE_TLS_CERT=/path/cert.pem ENNOTE_TLS_KEY=/path/key.pem \
ennote start
```

三道闸门，缺一不可：

1. `ENNOTE_ALLOW_LAN=1` —— 绑定非 loopback 地址必须显式声明，否则**拒绝启动**
2. TLS —— 没有证书时必须同时设 `ENNOTE_ALLOW_INSECURE_LAN=1` 才会启动（明文意味着密码和
   7 天 cookie 会明文过网，只建议在可信网络里临时这样用）
3. Host 白名单 —— 见下一节

## Host 校验（0.6.0 起）

每个请求都会检查 `Host` 头，防止 DNS rebinding：攻击者可以让自己的域名解析到 `127.0.0.1`，
再用一个网页对你的浏览器发请求，如果没有这项检查，全新安装可能被第三方抢先设密码。

规则：

- **IP 字面量永远放行**：`http://192.168.1.5:45131` 可以
- **loopback 名放行**：`localhost`、`127.0.0.1`、`[::1]`
- **用主机名访问需要显式列入白名单**，否则返回 403：

```bash
ENNOTE_HOSTNAME=0.0.0.0 ENNOTE_ALLOW_LAN=1 \
ENNOTE_GATE_ALLOWED_HOSTS=myhost.local,ennote.lan \
ennote start
```

!!! warning "升级到 0.6.0 之后"
    如果你以前用 `http://myhost.local:45131` 这样的主机名访问局域网，升级后会看到 403。
    两个选择：改用 IP 访问，或者把那个主机名放进 `ENNOTE_GATE_ALLOWED_HOSTS`。
    这不是故障，是有意收紧。

## 凭据与密钥

- Provider 的 API Key 存在 `~/.ennote/config/`，文件权限 `0600`，不会出现在日志里
- 本机界面密码用 bcrypt 存储
- 会话 cookie 在 TLS 下带 `Secure`
- 登录会话存在 `~/.ennote/config/sessions-<端口>.json`（`0600`），重启 gate 不用重新登录；
  文件里只有以密码哈希为密钥的 HMAC 摘要，没有可直接重放的 token
- 换密码会让所有浏览器掉登录：`ennote passwd`、`ennote passwd --reset` 与手工删掉 `auth.json`
  都一样。即使重设成同一个密码也会掉（bcrypt 每次用新 salt）。
  已经建立的长连接（SSE 事件流）不会被中断，直到它自己断开
- 所有抓取外部 URL 的工具都会拒绝私有网段、链路本地地址与云元数据地址

## 文件与产物

- 写入的工具调用只能落在 Project 工作区内（工作区根经过规范化，符号链接不能把你带出去）
- `~/.ennote` 被视为受保护路径：降级模式下 shell 命令引用它会被拒绝
- Artifact 与 spill 文件权限 `0600`

## 云端是可选件

Ennote 可以在完全离线的环境里长期使用。远程访问、账号、Relay 都是可选的：它们不可用时，
本机的启动、计算、持久化与局域网访问都不受影响。这是设计约束，不是尽力而为。

## 清单：把 Ennote 暴露到局域网前

- [ ] 用 TLS，别用 `ENNOTE_ALLOW_INSECURE_LAN`
- [ ] 只把需要的主机名写进 `ENNOTE_GATE_ALLOWED_HOSTS`，不要图省事写成通配
- [ ] 确认路由器没有把 45131 端口映射到公网——Ennote 不是为公网暴露设计的
- [ ] 定期看一眼 `~/.ennote/logs/ennote.log`
