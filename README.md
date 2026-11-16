# CryptoPay

CryptoPay 是一个**自托管、非托管（non-custodial）**的加密货币收款网关：商户通过 REST API 创建账单（invoice），把顾客重定向到收银台页面；顾客用钱包付款；网关只**读取**区块链来发现入账，确认后通过带签名的 webhook 通知商户。服务器上**从不保存私钥**——收款地址由扩展公钥（xpub）派生，或使用一个固定地址。

- 后端：Go（`backend/`，模块 `cryptopay`）—— HTTP API、链扫描器、webhook 投递
- 前端：Next.js（`web/`）—— 收银台 `/pay/{id}` 与管理后台 `/admin`
- 数据库：PostgreSQL

## 支持的币种与网络

稳定币约占加密支付交易量的 **60%**（以 USDT 为首，其中 Tron 上的 USDT 最多，其次是 USDC）；原生币中 BTC、LTC、ETH、TRX 在各支付处理商（CoinGate / CoinsBee / NOWPayments 2025–2026 年度统计）中使用最多。内置目录据此选择：

| 链族 | 网络 | 资产 | 默认确认数 |
|---|---|---|---|
| EVM | Ethereum | ETH, USDT, USDC | 12 |
| EVM | BNB Smart Chain | BNB, USDT, USDC | 15 |
| EVM | Polygon | USDT, USDC | 64 |
| EVM | Arbitrum One | ETH, USDT, USDC | 40 |
| EVM | Base | ETH, USDC | 20 |
| Tron | Tron | TRX, USDT | 19 |
| UTXO | Bitcoin | BTC | 2 |
| UTXO | Litecoin | LTC | 6 |
| Solana | Solana | SOL, USDT, USDC | finalized |
| 测试网 | Sepolia · Tron Nile · Bitcoin Testnet · Solana Devnet | ETH/USDC · TRX/USDT · BTC · SOL/USDC | — |

主网默认启用、测试网默认关闭；一条链只有**有收款钱包**（链的默认钱包，或某个商户自己的钱包）后才可支付。管理后台可以添加自定义代币（ERC-20 / TRC-20 / SPL）。

## 架构

```
商户后端 ──(REST, API key)──▶ ┌────────────┐ ◀──(cookie 会话)── 管理后台 (Next.js /admin)
                              │ Go gateway │
顾客浏览器 ──(公开 REST)─────▶ │ (backend/) │ ──(HMAC 签名 webhook)──▶ 商户后端
  收银台 (Next.js /pay/{id})   └─────┬──────┘
                                     │ 只读 RPC 扫描
             EVM · Tron · Bitcoin/Litecoin · Solana              PostgreSQL
```

- [`docs/api.md`](docs/api.md)：HTTP 接口契约（前后端唯一事实来源）
- [`docs/architecture.md`](docs/architecture.md)：架构、账单生命周期、扫描器、webhook
- [`web/README.md`](web/README.md)：前端说明

浏览器只访问 Next.js（同源），由它把 `/api/*` 代理到 Go 后端。

## 快速开始（Docker）

需要 Docker（含 Compose v2）。

```bash
cp .env.example .env
# 编辑 .env：至少修改 APP_SECRET（openssl rand -base64 48）和 ADMIN_PASSWORD
docker compose up --build -d        # 或 make up
docker compose ps                   # 等待 backend 显示 healthy
```

- 管理后台：http://localhost:3000/admin（使用 `.env` 里的 `ADMIN_EMAIL` / `ADMIN_PASSWORD` 登录；首次启动且数据库中没有管理员时自动创建 owner）
- 后端健康检查：http://127.0.0.1:8080/healthz（后端端口只绑定在 127.0.0.1）
- 端口被占用时可在 `.env` 中修改 `WEB_PORT` / `BACKEND_PORT`
- 开发演示数据：`make seed-dev`——创建演示商户和 API key（只显示一次），并把由公开的 Hardhat 测试助记词派生的 **仅供测试** 的 xpub 钱包设为 Sepolia / Tron Nile / Bitcoin Testnet 的默认钱包并启用这些测试网（`APP_ENV=production` 时拒绝运行；**切勿向这些地址发送真实资产**）
- 停止：`make down`；删除数据卷：`make clean`

不使用 Docker 时：`cd backend && go run ./cmd/gateway`（需要 `DATABASE_URL`、`APP_SECRET`），前端见 `web/README.md`。

## 首次运行：在管理后台完成配置

1. **创建收款钱包**（钱包 → 新建）
   - EVM / Tron / Bitcoin / Litecoin：粘贴**账户级扩展公钥**（EVM `m/44'/60'/0'`，Tron `m/44'/195'/0'`，BTC `m/84'/0'/0'` 的 `xpub`/`zpub`，测试网 `tpub`/`vpub`）。保存前可以“预览”前 3 个派生地址，请与自己的钱包软件核对。每张账单使用一个新地址 `xpub/0/index`。
   - Solana（以及不想用 xpub 的情况）：使用**固定地址（static）**。同一地址上的账单用**唯一金额**区分（金额末位会被微调），顾客必须精确支付显示的金额。
   - 粘贴私钥（`xprv`…）会被明确拒绝。
   - 同一个 xpub（或同一族的同一个固定地址）只能登记一次：两条记录用同一把钥匙会给不同商户派生出相同的地址，后台会返回 409。要给多个商户各自独立的 xpub 钱包，请使用不同的账户（如 EVM `m/44'/60'/1'`、`m/44'/60'/2'`）或不同的助记词。
2. **决定资金进哪个钱包**——两种方式可以混用：
   - **链的默认钱包**（链 → 选择链 → 默认收款钱包）：所有没有自己钱包的商户都收款到这里。只有一个商户、或所有商户本来就是同一家公司（资金进同一个钱包）时，只设默认钱包即可。确认链已启用；“可支付”列变为是。
   - **商户专属钱包**（商户 → 选择商户 → 收款钱包 → 为某条链选择钱包）：该商户在这条链上的账单收款到它自己的钱包，覆盖默认钱包。平台上的多个商户是**不同主体、资金必须分开**时使用。没有默认钱包的链，只要有商户设置了专属钱包，也会被扫描，但只对这些商户可支付。
   - 规则：某商户在某条链上的收款钱包 = 它的专属钱包，否则 = 链的默认钱包，否则该链对这个商户不可支付（`GET /api/v1/assets` 与收银台里都不会出现）。修改或移除钱包只影响之后新生成的报价：已经显示给顾客的未付账单仍使用原来的地址。
3. **配置商用 RPC**（链 → RPC）。内置默认值是免费公共节点，只适合开发测试；生产环境请为每条链填写 Alchemy / Infura / QuickNode（EVM）、TronGrid（`?apikey=`）、Helius（Solana）、GetBlock 或自建节点（BTC/LTC）等地址，可用“测试 RPC”检查。RPC 地址在数据库中加密存储，界面只显示 `协议://主机/…`。
4. **创建商户与 API key**（商户 → 新建）。webhook 签名密钥和 API key 明文**只显示一次**，请立即保存到商户后端。需要独立收款时，在商户详情页的“收款钱包”里为各条链选择该商户自己的钱包（见第 2 步）。
5. 按需调整**设置**：账单有效期、宽限期、少付容差、汇率加价、`checkout_base_url`（收银台对外地址，例如 `https://pay.example.com`）、WalletConnect project id 等。

## 商户接入

### 1. 创建账单

```bash
curl -X POST https://pay.example.com/api/v1/invoices \
  -H "Authorization: Bearer sk_live_xxx" \
  -H "Content-Type: application/json" \
  -d '{
        "amount": "25.00",
        "currency": "USD",
        "order_id": "ORDER-1001",
        "title": "Pro plan",
        "success_url": "https://shop.example/thanks",
        "cancel_url": "https://shop.example/cart",
        "metadata": {"customer_id": "42"}
      }'
```

- 返回 `201` 和 `Invoice`；同一 `order_id` 以相同金额和币种重复提交返回 `200` 与原账单（幂等），金额或币种不同返回 `409`。
- `currency` 可以是法币代码（USD、EUR、CNY…）或已启用资产的符号（如 `USDT`）。
- 可选 `asset_id`（如 `usdt-tron`）直接锁定付款资产；`GET /api/v1/assets` 列出当前可支付资产。
- 金额一律是**字符串**（`"25.00"`），不要用浮点数。

### 2. 把顾客重定向到 `checkout_url`

`checkout_url` 形如 `https://pay.example.com/pay/{invoice_id}`。收银台让顾客选择币种和网络，显示地址、金额、二维码和钱包按钮，并实时显示确认进度；付款完成后跳回 `success_url`。

### 3. 接收并验证 webhook

请求体为 `{ "id", "type", "created_at", "data": Invoice }`，事件类型：`invoice.payment_detected`、`invoice.confirming`、`invoice.paid`、`invoice.underpaid`、`invoice.expired`、`invoice.cancelled`。签名为 `hex(HMAC-SHA256(secret, timestamp + "." + 原始请求体))`：

```go
func cryptopayWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	ts, _ := strconv.ParseInt(r.Header.Get("X-CryptoPay-Timestamp"), 10, 64)

	mac := hmac.New(sha256.New, []byte(webhookSecret)) // whsec_...
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body) // 原始字节，不要重新序列化
	want := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-CryptoPay-Signature"))) ||
		time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var ev struct {
		ID   string          `json:"id"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &ev)
	// 按 ev.ID（= X-CryptoPay-Delivery）去重后再处理，然后返回任意 2xx。
	w.WriteHeader(http.StatusNoContent)
}
```

- 投递语义是**至少一次（at-least-once）**：非 2xx、超时会按 `min(2^n × 15s, 6h)` 退避重试，直到设置的最大次数；同一事件可能收到多次，**必须按事件 `id` 去重**。
- 多个事件可能并行投递，**到达顺序不保证**：以 `data.status`（以及 `data.updated_at`）为准，只在 `invoice.paid` 时发货。
- 后台可以查看每次投递的状态码与响应，并手动重试。

### 4. 其他接口

`GET /api/v1/invoices/{id}`、`GET /api/v1/invoices?status=&order_id=&page=&page_size=`、`POST /api/v1/invoices/{id}/cancel`（仅 `pending` 可取消）。完整契约见 [`docs/api.md`](docs/api.md)。

## 配置参考（环境变量）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `DATABASE_URL` | — （必填） | PostgreSQL URL；可用 `pool_max_conns` 等参数覆盖连接池大小 |
| `APP_SECRET` | — （必填，≥32 字符） | 静态加密（RPC 地址、webhook 密钥）的主密钥，**必须保持不变并妥善备份** |
| `APP_ENV` | `development` | `production` 启用 Secure cookie、HSTS，禁止 webhook 访问内网，拒绝 `seed-dev` |
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`（JSON 日志，不记录请求头、请求体、查询串） |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | — | 首次启动且没有任何管理员时创建 owner |
| `SCANNER_ENABLED` | `true` | 本进程是否运行链扫描与账单过期任务 |
| `WEBHOOK_ALLOW_PRIVATE` | 开发环境 `true`，生产 `false` | 是否允许 webhook 指向 127.0.0.1 / 内网地址 |
| `TRUST_PROXY_HEADERS` / `TRUSTED_PROXIES` | 关 / — | 仅当直接对端是受信代理时才采信 `X-Forwarded-For`（限流、审计 IP） |
| `CORS_ORIGINS` | — | 允许直接在浏览器调用 `/api/v1` 的来源（通常留空） |
| `COINGECKO_API_KEY` / `COINGECKO_PRO` / `COINGECKO_BASE_URL` | — | 汇率来源 CoinGecko |
| `RPC_<CHAIN_ID>` | 内置公共节点 | 每条链的 RPC，逗号分隔做故障切换，例如 `RPC_TRON_NILE`；后台配置优先 |
| `WEB_PORT` / `BACKEND_PORT` / `POSTGRES_PASSWORD` | `3000` / `8080` / 开发密码 | 仅 docker compose 使用 |

运行时可调参数（有效期、宽限期、容差、加价、webhook 次数与超时、`checkout_base_url`）在后台“设置”中修改。各项说明见 [`.env.example`](.env.example)。

`gateway` 命令：`serve`（默认，启动时自动迁移，`--no-migrate` 关闭）、`migrate`、`create-admin --email --password --role`、`seed-dev`、`healthcheck`、`version`。

## 在 Docker 中运行测试

所有测试都可以在容器中运行，本机不需要 Go 或 Node：

```bash
make test           # 后端 + 前端
make test-backend   # go vet + go test -race ./...（使用一次性的 PostgreSQL 容器）
make test-web       # pnpm lint && pnpm typecheck && pnpm test
make test-live      # 可选：用免费公共 RPC 测试链适配器（依赖网络、受限流影响，可能偶发失败）
make lint           # gofmt / go vet / eslint / tsc
```

等价的 compose 命令：`docker compose -f docker-compose.test.yml run --rm --build backend-test`（退出码即测试结果），之后 `docker compose -f docker-compose.test.yml down -v`。

本机直接运行后端测试：`export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:54329/cryptopay_test?sslmode=disable' && cd backend && go test -race ./...`（未设置时数据库相关测试会被跳过）。

## 安全说明

- **非托管 / 只读**：网关只保存扩展**公钥**或固定地址，只调用只读 RPC，不签名、不广播交易。资金归集（sweep）由商户自己持有助记词的钱包软件完成，不在本系统范围内。
- **永远不要粘贴私钥或助记词**。`xprv` 等私钥会被拒绝；任何要求你在后台输入助记词的指引都是错误的。
- `APP_SECRET` 加密数据库中的 RPC 地址（常含 API key）和 webhook 签名密钥；API key 与会话令牌只保存 SHA-256 哈希。丢失 `APP_SECRET` 会导致这些密文无法解密，泄露则需要轮换所有 RPC key 与 webhook 密钥。
- 管理后台：HttpOnly + SameSite=Lax 会话 cookie（生产环境 Secure）、所有写操作要求 `X-Requested-With: cryptopay`（CSRF 防护）、登录失败按 IP+邮箱限流、owner / admin / viewer 三级权限，所有修改写入审计日志（不含密钥、密码和完整 RPC 地址）。
- 账单 UUID 即收银台的访问凭证：不要在公开场合泄露 `checkout_url`。

### 生产环境检查清单

- [ ] `APP_ENV=production`，全程 HTTPS（反向代理终止 TLS），设置正确的 `checkout_base_url`
- [ ] 强随机 `APP_SECRET` 并离线备份；强 `ADMIN_PASSWORD`，首次登录后修改；`POSTGRES_PASSWORD` 已修改
- [ ] 每条启用的链都配置了商用 RPC（不要依赖免费公共节点），并用“测试 RPC”验证
- [ ] 钱包预览地址已与自己的钱包软件核对；测试用的 Hardhat 钱包**没有**分配给主网（既不是默认钱包，也不是商户专属钱包）
- [ ] 反向代理会覆盖/追加 `X-Forwarded-For`，并把代理地址配置到 `TRUSTED_PROXIES`（否则所有顾客共用一个限流桶）
- [ ] 后端端口不对公网开放，只暴露 Web；数据库定期备份
- [ ] 商户端 webhook 校验签名、检查时间戳并按事件 id 去重
- [ ] 多实例部署时，只让一个或少数实例 `SCANNER_ENABLED=true`（扫描有分布式锁，可多实例，但无需全部开启）

## 已知限制

- **EVM / Tron 原生币的内部转账不会被发现**：只识别交易顶层的 ETH/BNB/TRX 转账和 ERC-20/TRC-20 `Transfer` 事件；通过合约内部调用（部分智能合约钱包、交易所批量提现）转入的原生币检测不到，需要在后台把账单手动标记为已付。
- **Litecoin MWEB** 隐私交易无法观察；请使用普通地址付款。
- 没有可免费使用且无需 key 的 Litecoin JSON-RPC：内置默认节点每分钟仅允许少量匿名请求，只够开发用，生产必须配置自己的 RPC。
- **资金归集不在范围内**：每张账单的派生地址上的资金需用自己的钱包软件归集（同一钱包助记词）。
- **固定地址钱包**靠唯一金额区分账单：金额不精确（交易所扣手续费、顾客手动改金额）的付款会进入“未匹配转账”，需要管理员手动分配；同一金额在账单关闭（宽限期结束）后会被复用。
- 扫描器不回溯历史：链第一次被扫描时从当前区块高度开始。
- 一条链只在启用且有钱包（默认钱包或至少一个商户专属钱包）时才被扫描：把链上最后一个钱包移除后，之前生成、仍在等待付款的报价也不会再被发现。请等这些订单关闭后再移除。
- 汇率来自 CoinGecko（无 key 时有较低的速率限制）；拿不到汇率时无法用该资产报价（稳定币在开启锚定时按 1 USD 计）。
- 限流计数保存在进程内存中，多实例部署时各实例分别计数。
- webhook 至少投递一次且不保证顺序（见上文）。
