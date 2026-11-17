# 测试链手动走查流程

用真实测试网（Sepolia、Tron Nile、Bitcoin testnet3）把一笔订单从下单走到 Webhook 送达。
整个过程不涉及真实资金。

## 0. 前提

- 服务已启动：`make up`，`http://localhost:3000/healthz` 返回 200。
- 已执行过 `make seed-dev`。它会启用三条测试链，并给它们分配**仅供测试**的默认钱包：

  | 链 | 默认钱包 | 需要的确认数 | 大约耗时 |
  |---|---|---|---|
  | `sepolia` | TEST ONLY - Hardhat (EVM) | 3 | 约 1 分钟 |
  | `tron-nile` | TEST ONLY - Hardhat (Tron) | 19 | 约 1 分钟 |
  | `bitcoin-testnet` | TEST ONLY - Hardhat (Bitcoin testnet) | 1 | 10 分钟到数小时（testnet3 出块不稳定） |

  这些钱包由**公开的** Hardhat 助记词派生，任何人都能把里面的币转走（链上常有自动清扫机器人）。
  这不影响到账检测，但不要往里转超过测试所需的金额。
- 网关只从链被启用的那一刻开始扫描，不回扫历史区块：链启用前发出的转账不会被发现。

## 1. 准备付款钱包和测试币

付款方要用**普通钱包账户**付款。EVM 和 Tron 上由合约转出的原生币（例如多签钱包、部分水龙头合约直接打到收款地址）检测不到。

| 网络 | 钱包 | 测试币来源 |
|---|---|---|
| Ethereum Sepolia | MetaMask（设置 → 高级 → 显示测试网络） | ETH：任一 Sepolia ETH 水龙头（Google Cloud Web3 faucet、QuickNode faucet 等）；USDC：`https://faucet.circle.com`，网络选 **Ethereum Sepolia**；PYUSD：Google Cloud Web3 faucet 选 PYUSD · Sepolia |
| Tron Nile | TronLink（切换到 Nile 测试网） | `https://nileex.io/join/getJoinPage`，领 TRX 和 USDT |
| Bitcoin testnet3 | UniSat 或 OKX 钱包，网络选 **Testnet3** | 任一 testnet3 水龙头 |

注意：

- **USDC 合约必须是** `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238`（Circle 官方 Sepolia USDC），
  Sepolia **PYUSD 合约必须是** `0xCaC524BcA292aaade2DF8A05cC58F0a65B1B3bB9`（Paxos 官方测试币），
  Nile **USDT 合约必须是** `TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf`。别的同名测试代币不会被识别。
  如果你领到的是其他合约，可以在后台「币种 → 添加代币」里加上。
- Sepolia 转 USDC 需要少量 ETH 付 gas；Nile 转 USDT 需要 TRX 付能量费（收银台设置的 feeLimit 是 100 TRX）。
- Bitcoin 的默认 RPC 是 **testnet3**，testnet4 的币检测不到。

## 2. 检查后台状态

1. 打开 `http://localhost:3000/admin`，用 `.env` 里的 `ADMIN_EMAIL` / `ADMIN_PASSWORD` 登录。
2. 「链与 RPC」：确认 `sepolia`、`tron-nile`、`bitcoin-testnet` 是启用状态，默认钱包是 `TEST ONLY - …`，
   落后区块数接近 0，没有最近错误。免费公共 RPC 偶尔限流，出现短暂错误后能自己恢复就没问题。

## 3. 启动本地 Webhook 接收器

1. 后台「商户」→ `Demo Shop (dev)` →Webhook 区域点「轮换密钥」，复制新的签名密钥（只显示一次）。
2. Webhook 地址填 `http://host.docker.internal:9009/hook` 并保存。
3. 在项目根目录运行：

   ```bash
   WEBHOOK_SECRET=whsec_粘贴刚才的密钥 python3 scripts/webhook-receiver.py
   ```

   每收到一条通知会打印事件类型、订单状态、已确认金额，以及签名是否校验通过。

## 4. 创建订单

任选一种方式：

- **后台**：「订单 → 创建订单」，商户选 `Demo Shop (dev)`，金额 `1`，币种 `USD`。
- **商户 API**：先在商户详情页「API 密钥 → 创建密钥」拿到 `sk_live_…`，然后：

  ```bash
  curl -s localhost:3000/api/v1/invoices \
    -H "Authorization: Bearer sk_live_你的密钥" \
    -H 'Content-Type: application/json' \
    -d '{"amount":"1","currency":"USD","order_id":"walkthrough-1","title":"Testnet walkthrough"}'
  ```

  用同一个 `order_id` 再发一次会拿到同一张订单（HTTP 200），改了金额会返回 409。

打开返回的 `checkout_url`（形如 `http://localhost:3000/pay/<id>`）。

## 5. 付款场景

每个场景用一张新订单。付款后**不要刷新收银台**，页面每 3 秒自动更新。

### A. Sepolia USDC，浏览器钱包唤起

1. 收银台选 **USDC · Ethereum Sepolia**，应付 `1`（稳定币按 1:1 计价）。
2. 「钱包付款」→ 选 MetaMask → 连接。页面会请求切换到 Sepolia，确认交易。
3. 预期：
   - 十几秒内页面进入「确认中」，显示 `n / 3` 确认；
   - 约 1 分钟后变成「付款成功」，设置了 success_url 时几秒后自动跳转；
   - 接收器依次打印 `invoice.confirming`、`invoice.paid`，签名 `OK`。

### A2. Sepolia PYUSD

与 A 相同，收银台改选 **PYUSD · Ethereum Sepolia**。PYUSD 是标准 ERC-20，流程和预期结果与 USDC 一致。

### B. Sepolia ETH，手机扫码

1. 选 **ETH · Ethereum Sepolia**，金额按 CoinGecko 实时汇率换算。
2. 用手机 MetaMask（同样切到 Sepolia）扫「付款请求」二维码，金额和网络会自动带入。
   如果钱包不认二维码，切到「仅地址」，手动输入页面上的金额。
3. 预期同 A。

### C. Tron Nile USDT，TronLink

1. 选 **USDT · Tron Nile**。
2. 「钱包付款」→ TronLink。确认 TronLink 当前在 Nile 网络，否则页面会提示网络不匹配。
3. 预期：19 个确认，约 1 分钟后付款成功。

### D. 部分付款后补齐

1. 新订单选 Sepolia USDC，第一笔只转 `0.5`。
2. 预期：状态「部分支付」，页面显示已收和剩余金额，接收器打印 `invoice.payment_detected`。
3. 再转剩余的 `0.5`：进入确认中，随后付款成功（`invoice.confirming` → `invoice.paid`）。

### E. 多付

转 `1.5` USDC：订单付款成功，后台标记「多付」，收银台提示多付，订单详情里能看到多付标记。

### F. 过期与宽限期

1. 用商户 API 建一张短期订单：在请求体里加 `"expires_in": 120`。
2. 收银台选好币种后不付款，等 2 分钟：收银台显示「付款已过期」，后台状态为「已过期」，接收器打印 `invoice.expired`。
3. 过期后 24 小时宽限期内补付全额：订单仍会变为已支付，后台标记「逾期到账」（`paid_late`）。

### G. 商户专属钱包

`Shop B` 在 Sepolia 上绑定了自己的钱包（`Shop B EVM`，同样是公开测试密钥）。
给 `Shop B` 建一张 1 USD 订单并选 Sepolia USDC：收款地址与 Demo Shop 的订单不同，各自入账互不影响。
它的 Webhook 地址需要单独设置。

### H. Bitcoin testnet3（可选）

选 **BTC · Bitcoin Testnet**，用 UniSat 或 OKX 的「钱包付款」，或扫码。
1 个确认即付款成功，但 testnet3 出块间隔很不稳定，可能要等很久。

## 6. 在后台核对

- **订单详情**：报价（地址、应付/已收/已确认金额、汇率）、链上转账（确认数、区块浏览器链接）、
  Webhook 投递记录（payload、状态码，可手动重试）。
- **链上转账**：所有检测到的转账；「未匹配」的转账可以手动关联到订单。
- **Webhook 投递**：失败的投递会按指数退避重试，也可以手动重试。
- **总览**：收款金额、订单数、转化率、按币种统计。
- **审计日志**：刚才每一次后台操作都有记录。

## 7. 排查

| 现象 | 检查 |
|---|---|
| 付款后几分钟没反应 | 「链与 RPC」里该链的最近错误和落后区块数；确认付款钱包在正确的网络；确认代币合约是第 1 节列出的那个；`docker compose logs -f backend` |
| 一直停在确认中 | 正常，等确认数够；免费 RPC 限流时会变慢 |
| 收到的金额对不上 | 订单详情里看转账金额；少于应付按部分支付处理，低于应付 0.5% 以内视为付清（系统设置可调） |
| 接收器没收到通知 | 「Webhook 投递」页看最后错误；接收器要在本机 9009 端口运行；签名 `INVALID` 说明密钥填错了 |
| 收银台没有某个币种 | 该链未启用、没有钱包，或币种被禁用 |

测完后可以在「链与 RPC」里关闭不再需要的测试链。
