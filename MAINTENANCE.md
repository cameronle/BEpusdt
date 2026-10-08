# 自维护分支

- 上游：<https://github.com/v03413/BEpusdt>，保留 GPL-3.0 许可及原作者信息。
- `main`：仅保留上游代码，供后续显式同步；没有添加本地补丁。
- `maintenance`：默认、自维护分支，基于上游 `v1.24.2` / `4d88040fd4096e77e8fb9ad2650e775753a977b6`。
- 本地补丁版本线：`v1.24.2-shop-rpc3`。这是维护版本，不是上游官方发布。
- 分支源码可以重建网关；真实 RPC Key、收款地址、商户签名密钥、账户、订单、数据库和部署凭据不属于源码。

## 已纳入的调整

1. 传统 EVM 日志查询按网络注册合约过滤，扫描串行限速/失败退避；允许网络隔离的回执 RPC 配置。
2. 可选 USDT-only BSC / Polygon 扫描器：按合约与收款地址 Transfer topic 查询，仅匹配区块读取时间戳。
3. 实时、历史回扫与回执任务分离；实时/历史 RPC 池及其方法冷却独立，历史故障不堵实时链头。
4. RPC 链 ID、响应、区间和回执验证；请求故障切换、限流冷却、日志查询范围自适应分页。
5. 进度、失败区间与订单覆盖原子持久化，先落原生订单匹配状态再推进游标；恢复/历史回扫不丢覆盖。
6. 保留金额/网络/代币/地址/时间窗/区块哈希/成功回执与确认数校验，以及原生防重与签名回调。
7. 隔离回归测试覆盖扫描恢复、故障冷却隔离、无效/缺失回执、重复匹配和 `atom_usdt=0.0001` 的双链报价与严格金额匹配。

## 配置边界

扫描器默认关闭，显式设置 `BEPUSDT_EVM_SCANNER_V2=1` 才启用；未开启时仍使用兼容后的传统扫描器。
开启前必须在原生后台启用区块确认，BSC 15 / Polygon 40 确认。该扫描器只处理这两条链的 USDT，不扫描其他资产或原生币。

参考 `.env.scanner.example`，其中只有公开 RPC。用位于仓库**外**、权限 `0600` 的私有 EnvironmentFile 保存带 Key 的 RPC 列表，并由已有服务加载。不同链的 URL 必须保持独立；不得提交该私有文件。
- `BEPUSDT_RPC_<BSC|POLYGON>_URLS`：实时与历史池的初始 URL 列表，运行时冷却状态各自独立。
- `BEPUSDT_RECEIPT_RPC_<BSC|POLYGON>_URLS`：回执池 URL 列表。
- `BEPUSDT_EVM_STATE_DIR`：必须可写的持久化扫描进度目录；数据库和扫描状态一起备份。
- 节点按请求类型故障切换，最后成功的备用可能持续优先；这不是自动配额控制，也不保证永远只用公共主节点。
- BNB 官方 dataseed 可用于回执，但不能假定其支持 `eth_getLogs`。公共 RPC 和免费备用没有可用性/额度保证。

`atom_usdt=0.0001` 是原生后台/数据库运行配置，不是新增环境变量，也没有改写上游默认金额步长。改变步长不改写已有支付单；顾客必须按收银台的**完整实际金额**转账。

商城返回路由属于商城配置，不属于这个独立网关仓库。此仓库不包含商城、生产域名、收款配置或运维私密文件。

## 重建和验证

要求：`go.mod` 指定的 Go 工具链、Node 22.22.2、pnpm 10.34.6（脚本使用固定版 npx）。先构建后台前端并嵌入，再编译 Go；只有 Go 二进制而缺少 `secure.html` 不能作为可运行交付。

```sh
git clone --branch maintenance https://github.com/cameronle/BEpusdt.git
cd BEpusdt
sh scripts/build-maintenance.sh
sh scripts/verify-maintenance.sh
sh scripts/scan-secrets.sh
sha256sum dist/bepusdt
```

产物 `dist/bepusdt`、前端 `web/dist` / `static/secure` 均不提交。可显式传入 `VERSION` 标记候选版本。不要把二进制复制覆盖运行中的程序，部署前须备份、保留回退目录，并验证数据与实际进程哈希。

可选真实公开链只读验证（自动发现近期公共转账，不硬编码商户钱包或真实支付单）：
```sh
BEPUSDT_RUN_LIVE_RPC_TESTS=1 GOMAXPROCS=2 go test -p 1 ./app/task -run TestFastLiveChainsAndReadOnlyFailover -count=1 -v
```
该测试依赖公共节点的现时可用性，并不创建订单、转账或调用商户回调。隔离测试、只读 RPC、真实支付验收是三类不同证据；不能把测试绿灯当成真实付款或自动发货验证。

## 分支保护与自动化

维护分支使用 `Go checks` 和 `Secret scan` 两个必需检查，禁止强推和删除。不强制 PR-only，仓库管理员保留直推/必要时绕过检查的权限；正常改动建议先在功能分支跑 CI，再合入维护分支。

Actions 只运行构建、单测、race、vet 及脱敏扫描，权限为 `contents: read`，第三方 Actions 固定 commit，gitleaks 二进制固定版本和 SHA-256。
维护分支移除了继承的定时清理、DockerHub 发布和 GitHub Release 工作流；没有生产 Secret，也没有自动部署生产服务。`main` 中的上游工作流仅作为原样源码保留，不应主动启用其发布/定时任务。

脱敏扫描保留默认规则。`.gitleaks.toml` 仅将上游 API 文档中 `token` 字段的**公开 TRON USDT 合约地址**作为精确误报例外，同时匹配文档路径与完整公共值；不忽略整个文档或测试目录。实际钱包、交易与商户身份还应在发布前与本机私密配置进行不回显的字面值比对。

## 后续跟进上游

先获取并审查新上游 Release 的差异，在独立候选分支迁移补丁，运行完整检查，再更新维护分支。同步 `main` 不代表升级维护分支或线上程序；不要用上游原版静默覆盖带扫描器修复的部署。不要创建未经过维护检查的发行标签来触发上游发布流程。
