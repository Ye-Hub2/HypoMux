# 冻结契约 · Hyper-V 虚拟网卡批量创建（v2）

> 取代 `reports/vnic/00-frozen-interface.md`（Wintun 方案，已废弃）。
> 冻结人：lead　冻结时间：2026-10-03　状态：**实施中，所有实现方必须逐字遵守**

---

## 0. 需求来源（不可再讨论）

- **m01573**「这种虚拟网卡没用啊」——Wintun L3 空壳方案作废。
- **m01577**「首先当前局域网存在限速，是基于 mac 地址限速，所以需要虚拟网卡获取局域网的 IP 来聚合提升网速。」
- 「已经打开 hyper-v 了」
- **m01590** 用户选定：按 Hyper-V vNIC 重做 / 创建时手动填数量。
- **m01604**「顺便把主页的虚拟网卡改成一个独立管理虚拟网卡页面，不再主页显示了，太占地方了。」（硬验收）
- **m01700**「不是协商速率的问题，是网线接了百兆交换机。目前是限速 1.2M/S，所以才要做聚合。」

实测标定：单流 `http://mirrors.tuna.tsinghua.edu.cn/ubuntu/ls-lR.gz` = **1,203,238 B/s = 9.63 Mbps**，与每 MAC 限速吻合。百兆交换机口可承载约 10 个 MAC 配额。

---

## 1. 技术路线（冻结）

**不使用 Wintun。** 使用 Hyper-V 宿主机虚拟网卡（ManagementOS vNIC）挂在**外部交换机**上。

```
Add-VMNetworkAdapter -ManagementOS -SwitchName <外部交换机> -Name <名字> `
    -StaticMacAddress <mac> -PassThru -ErrorAction Stop
```

依据：
- `reports/vnic/60-hyperv-cmd-surface.md` §2（真机复核：`New-VMNetworkAdapter` 不存在；参数集 `ManagementOS`/`VMName`/`VMObject`；无 `-Persistent`、无 `-SwitchType`）。
- 参考实现 `%USERPROFILE%\Desktop\xuni-network\artifacts\payload\resources\scripts\New-XuniNic.ps1:226-227`。
- 删除必须走对象管道（`Remove-VMNetworkAdapter` 的 `ResourceObject` 参数集，`ValueFromPipeline`，**不可同时传 `-ManagementOS`**）；`-ManagementOS -Name` 会同时命中幽灵记录。依据 `reports/vnic/60-hyperv-cmd-surface.md` §3 与 `reports/task-43-remove-nic-ghost-fix.md`。
- 宿主机侧网卡名恒为 `vEthernet (<Hyper-V 对象名>)`；唯一稳定主键是 **DeviceId**（= `Get-NetAdapter.InterfaceGuid`）。依据 `reports/vnic/60-hyperv-cmd-surface.md` §4。
- 主 TUN 清理逻辑 `engine/internal/tun/cleanup_windows.go:105-140` 按名字精确匹配 `HypoMux-Tun` + InstanceID 含 `WINTUN`，**不会误杀 `vEthernet (HypoMux-vnic-*)`**（`reports/vnic/61-vnic-contract-rework.md` §5 已验证）。

---

## 2. 引擎侧：整体删除（冻结）

**`vnic.create` / `vnic.status` / `vnic.remove` 三个 RPC 全部删除，不保留任何替代。**

理由：Hyper-V vNIC 是宿主机上长期存在的接口，引擎不是创建者；`selected_adapter_ids` + 现有网卡枚举已能消费 `vEthernet (HypoMux-vnic-*)`。

执行方案 = `reports/vnic/61-vnic-contract-rework.md` 的**变体 B**，合计 **-1237 行**。三处陷阱必须正确处理：

1. `engine/internal/server/server.go:968-973` 的 `handleTunLog` 夹在删除块中间，**必须保留**，并折叠产生的双空行。
2. `engine/internal/server/server_test.go:190-250` 的 `fakeTunController` 是共享假件，**不能删**。
3. `engine/internal/api/v1/contract_test.go` 的 4 处 case **必须删**，否则编译失败。

`engine/internal/protocol/protocol.go` **零改动**。

`protocol/v1/manifest.json` 删除 3 个 method 条目及其 guards（`reports/vnic/61-vnic-contract-rework.md` 已给出精确行号：`:76`、`:82`、`:88`）。`protocol/v1/fixtures/messages.json` 同步删除。

**stale 孤儿适配器缺陷（F1）随整包删除自然消失，无需补丁。**

环境（已验证可用）：
- `GOTOOLCHAIN=auto`（**不是 local**——go.mod 要 1.26.0，本地默认 1.22.5）
- `GOPROXY=https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct`
- 前置：`$env:Path = "$env:USERPROFILE\.cargo\bin;" + $env:Path`（沿用现有 shell 准备）
- 校验门：`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`gofmt -l`（**必须零输出**，CI 在 `.github/workflows/build.yml:168-186` 卡这一关）

---

## 3. 桌面层接口（冻结 · 逐字实现）

### 3.1 Go 服务

新文件：`desktop/internal/services/hyperv_adapter.go`（可另加 `desktop/internal/services/hyperv_*.go`）

```go
func NewHyperVAdapterService(settings *SettingsService, adapters *AdapterService) *HyperVAdapterService

func (s *HyperVAdapterService) List() ([]HyperVAdapterStatus, error)
func (s *HyperVAdapterService) Switches() ([]HyperVSwitch, error)
func (s *HyperVAdapterService) Create(switchName string, count int) ([]HyperVAdapterStatus, error)
func (s *HyperVAdapterService) Remove(name string) error
func (s *HyperVAdapterService) Shutdown()
```

**绑定文件名 = Go 类型名小写** ⇒ `desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/hypervadapterservice.ts`。

### 3.2 模型（camelCase JSON tag，冻结）

```go
type HyperVAdapterStatus struct {
    Name          string `json:"name"`          // Hyper-V 对象名，如 HypoMux-vnic-01
    InterfaceName string `json:"interfaceName"` // 宿主机网卡名 vEthernet (HypoMux-vnic-01) ← 出口池的键
    AdapterID     string `json:"adapterId"`     // DeviceId == Get-NetAdapter.InterfaceGuid
    MacAddress    string `json:"macAddress"`
    SwitchName    string `json:"switchName"`
    State         string `json:"state"`         // absent|creating|ready|failed
    Address       string `json:"address"`
    PrefixLength  int    `json:"prefixLength"`
    Gateway       string `json:"gateway"`
    Managed       bool   `json:"managed"`        // 在台账里 ⇒ 本工具创建
    InPool        bool   `json:"inPool"`         // 已在出口池 selected_adapter_ids
    BatchID       string `json:"batchId"`
    CreatedAt     string `json:"createdAt"`
    LastError     string `json:"lastError"`
}

type HyperVSwitch struct {
    Name              string `json:"name"`
    Type              string `json:"type"`                // External | Internal
    AllowManagementOS bool   `json:"allowManagementOs"`
    Uplink            string `json:"uplink"`            // 外部交换机的物理网卡名
    NetAdapterName    string `json:"netAdapterName"`
}
```

**`InterfaceName` 是关键字段**：HypoMux 的出口池 `selected_adapter_ids` 用的是宿主机网卡名（`vEthernet (xuni-01)`），不是 Hyper-V 对象名。漏了它就无法自动进池。

### 3.3 提权方案（冻结）

- **不复用 `EnsureElevated`**——它会重启聚合核心、断开用户当前连接（`desktop/internal/services/engine.go:763`）。
- 一次性 runas 子进程跑**常量 PowerShell 脚本**；所有可变数据（交换机名、张数、序号前缀、名字、结果文件路径）经 **base64 注入**，脚本正文是 Go 常量字符串。
- 三条硬约束：
  1. 必须 `powershell.exe`（Windows PowerShell 5.1），**不能 `pwsh`**——Hyper-V 是 Windows PowerShell 模块。
  2. `-EncodedCommand` 必须是 **UTF-16LE base64**。
  3. `ShellExecuteExW + verb runas` **拿不到子进程 stdout** ⇒ 结果必须走**结果文件**（无 BOM UTF-8，写 `.tmp` 后 `Move-Item` 原子提交）。
- 注入防护靠结构不靠转义：base64 字母表不可能逃出 PowerShell 单引号。
- 本进程已提权时（先例 `desktop/internal/services/tun_preflight_windows.go:25`）可直接 exec；否则 runas。
- 脚本必须自带界 + 幂等 + 超时只报错不改状态，因为父进程杀不掉已提权子进程（`desktop/internal/engineclient/privileged_windows.go:390-404`）。
- **不新增 exe、不新增 helper 子命令。**

### 3.4 归属判定（冻结 · 三重）

1. 台账 `<HYPOMUX_DATA_DIR>/hyperv/adapters.json`（**权威**）——复用 `desktop/internal/services/atomic_file.go:9` 的原子写 + 命名互斥。
2. 命名 `HypoMux-vnic-NN`。
3. 删除时 MAC 逐字节一致。

**只删台账里登记过的。未登记的一律跳过（`state=skipped`），绝不误删用户自己的 `xuni-*`。**

### 3.5 DHCP 等待（冻结）

轮询 500 ms：网卡出现 **15 s** → IP 就绪 **45 s** → 整体 **60 s**。
成功判据：`PrefixOrigin=Dhcp` **且** `AddressState=Preferred`。
失败特征：APIPA `169.254.x.x`、`AddressState=Duplicate`、`Dhcp-Client-Admin` 事件 1002。
**DHCP 超时属软失败**：标记 `failed` + `lastError`，不阻断整批、不自动删卡。

### 3.6 出口池写入（冻结 · 两条硬约束）

1. **必须走 `SettingsService.UpdateHome`**（`desktop/internal/services/settings.go:407-414` → `updateHomeStrategy` `:416-443`）。
2. **禁用 `UpdateFields`**——白名单 `desktop/internal/services/settings.go:265-310` 不含出口池字段。
3. 必须传当前 `Mode`。
4. **顺序**：提权创建 → 台账落盘 → `UpdateHome`。**不得持锁跨越提权调用。**
5. 创建后**必须重启聚合**——`desktop/internal/services/engine.go:828`/`:854` 是启动期快照。

### 3.7 批次回滚（冻结）

- 硬失败（命令级失败）→ 停止本批、**保留已成功的**、标 `failed`、返回可辨识错误码。
- 提供「撤销本次创建」（按 `batchId`），逆序删除本轮已创建的全部。
- 上限：**16 张/批、32 张/总**。MAC 动态生成（本地管理位 `02:`）；**名字序号回收**——取 01–99 里最小的空闲编号，删掉的编号会重新发出去。

### 3.8 `List()` 不得依赖提权（冻结）

`desktop/internal/services/adapters.go:80-82` 会把「还没有 IPv4」的网卡整个丢掉——creating/failed 状态将永远不可见。**禁止复用 `AdapterService.List()`**，必须自己扫 `net.Interfaces()`。

`List()` / `Switches()` 必须在**非提权**下可用（降级路径）：宿主网卡名 `vEthernet (<对象名>)` 可反解出 Hyper-V 对象名，`InterfaceGuid` 即 DeviceId。若某项确实需要提权，允许在 elevated helper 里取，但 `List()` 本身不得触发 UAC。

### 3.9 `main.go` 改造（冻结）

- 删除 `desktop/main.go:150`（`virtualAdapterService` 构造）、`:156`（其 Shutdown）、`:188`（其 RegisterService）。
- 改为构造 `hypervAdapterService`，注册到同一位置。
- **`hypervAdapterService.Shutdown()` 必须仍在 `engineService.Shutdown()`（`desktop/main.go:157`）之前。**
- 不得重新引入任何 AI 接线。

---

## 4. 前端（冻结）

- 页面标识：**`virtual-adapters`**（`AppPage` 字符串联合）。
- 导航位置：`connections` 之后（`desktop/frontend/src/components/shell/CompactNavigation.tsx:30-37`），文案走 `t("nav_virtual_adapters")`。
- **`desktop/frontend/src/platform/services.ts` 分组改为：**
  ```ts
  virtualAdapters: {
    list(): Promise<GeneratedVirtualAdapterStatus[]>,
    create(switchName: string, count: number): Promise<GeneratedVirtualAdapterStatus[]>,
    remove(name: string): Promise<void>,
  }
  ```
  加 `switches()` 返回 `GeneratedHyperVSwitch[]`。
- 新增 `desktop/frontend/src/pages/VirtualAdaptersPage.tsx`、`components/vnic/HyperVAdapterPanel.tsx`、`components/vnic/managementAdapters.ts`（纯函数）。
- **删除** `components/vnic/VirtualAdapterPanel.tsx` + `.test.tsx`；`components/vnic/vnic.css` 保留复用。
- **主页清理**：`desktop/frontend/src/pages/HomePage.tsx` 删 `:26` 导入、`:67-74` `notifySuccess`、`:249-254` JSX。`notifyError`（`:91`/`:100`）保留。`HomePage.test.tsx` 既有 17 用例不改，另加 1 条「主页无虚拟网卡」回归断言。
- **不改 `desktop/frontend/src/app.css`**（5944 行，冲突面最大）。页面骨架走页面自有 CSS，先例 `pages/mtu.css:1`。
- **i18n 必须走 `desktop/frontend/src/i18n/` 既有 `t()` 通道**，zh + en 双写，禁止硬编码 CJK。
- 轮询必须用 `usePageActive()` 门控（`desktop/frontend/src/components/shell/AppShell.tsx:50-53` 让访问过的页面永久挂载）。
- ⚠️ `desktop/frontend/tsconfig.json` 有 **`noUnusedLocals: true`** —— 未使用的 import/局部变量会直接让 CI 失败。
- ⚠️ **图标导出必须先实证**：本机无 `node_modules`、全仓 grep 零命中。实施前先确认 `VirtualNetwork24Regular` / `Server24Regular` 等确实从 `@fluentui/react-icons` 导出；**不要凭记忆写**。

---

## 5. CI / 便携包（冻结）

- `.github/workflows/build.yml:346` 的便携包后缀 `-vnic-preview` **必须改掉**（Wintun 概念已废弃）。
- `desktop/portable/launch-portable.cmd`、`restore-environment.cmd`、`PORTABLE-README.txt` 里所有 `vnic` 字样与「虚拟网卡 = Wintun」叙述必须同步更新。
- 便捷脚本编码约束不变：`.cmd` = UTF-8 **无 BOM** + CRLF；`PORTABLE-README.txt` = UTF-8 **带 BOM**。`.gitattributes` 已有 `*.cmd text eol=crlf` / `desktop/portable/*.txt text eol=crlf`。
- **zip 必须逐条 `ZipArchive.CreateEntry` 正斜杠写入**（`ZipFile::CreateFromDirectory` 会写 `bin\`，塌平后破坏 `desktop/internal/engineclient/service_windows.go:73-91`）。

---

## 6. 发布纪律（冻结）

1. **desktop + engine + frontend + workflow 的改动必须同批落地一个提交。** 任一层单独提交都会让 CI 编译失败（bindings 未生成 / 服务名不匹配 / 悬空 import）。
2. `reports/**` **一律不进提交**。暂存必须用显式路径，**禁止 `git add -A`**。
3. 提交前必过本地门禁：`gofmt -l` 零输出、`go -C engine test ./...`、`go -C desktop test ./...`、`go vet`、`pnpm test`、`tsc && vite build`。
4. 推送后确认 Actions 触发；若首次未触发则 `gh workflow run build.yml -f signing_mode=none`。**git push 不得加 `credential.interactive=false` / `GCM_INTERACTIVE=Never`。**
5. **未经用户明确许可，不得创建、删除或改动任何真实网卡、外部交换机、路由或系统网络设置。**

---

## 7. 各任务写范围（互斥）

| 任务 | 成员 | 写范围 |
|---|---|---|
| T-A 引擎删除 | `engine-vnic-dev` | `engine/**`、`protocol/v1/**` |
| T-B 桌面服务 | `desktop-vnic-dev` | `desktop/internal/services/hyperv_*.go`、`desktop/internal/services/virtual_adapter.go`(删)、`desktop/internal/services/vnic_config.go`(删)、`desktop/internal/engineclient/vnic.go`(删)、`desktop/main.go` |
| T-C 前端 | `frontend-vnic-dev` | `desktop/frontend/src/**` |
| T-D CI/便携包 | `infra-analyst` | `.github/workflows/build.yml`、`desktop/portable/**`、`.gitignore`、`docs/**` |

各自报告：`reports/vnic/71-engine-removal.md`、`72-desktop-hyperv.md`、`73-frontend-hyperv.md`、`74-portable-ci.md`。
