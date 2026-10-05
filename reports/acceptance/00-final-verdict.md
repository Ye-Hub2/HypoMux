# HypoMux 工作区改动 · 团队验收总判定

- 验收对象：相对 `HEAD = fc821b1`（"feat: move the virtual NIC management to a standalone Hyper-V page"）的**全部工作区改动**
- 范围：**59 个已跟踪文件（+5805 / −4352）** + **58 个未跟踪报告文件**（`reports/`）
- 方式：7 条腿并行验收（Go 构建测试 / 前端检查 / 死引用审计 / Hyper-V 后端深审 / 网卡前端深审 / CI 发布链 / 提交卫生），其中 Hyper-V 后端腿另拆 2 名子成员（测试可信度、冻结契约核对）
- 纪律：全程只读，未 `git add/commit/reset/clean`；验收前后已跟踪改动集合未变

---

## 一、总体结论

**有条件通过 —— 质量全绿，但不建议直接提交/放行。**

所有质量闸门实测通过，功能需求逐条落地，**没有发现会崩溃、会断链、会跑不起来的缺陷**。但有 **8 个必改项**（3 个功能正确性 + 2 个发布链 + 3 个文档），以及一项**隐私风险**（未跟踪报告含本机路径/主机名/真实内网 IP，公开仓库不可撤回）。

| 判定 | 数量 | 清单 |
|---|---|---|
| 🔴 功能缺陷（必改） | 3 | P0-1 单删超时错配、P0-2 身份用 name、P1-1 单张 Remove 漏读脚本失败 |
| 🟠 发布链（必改） | 2 | P1-2 便携包进了仓库却没进 Release、P1-3 Authenticode 校验静默空跑 |
| 🟡 文档漂移（必改） | 3 | README_EN.md 整份漏改、v2.7.0 双语发布说明、docs/README 索引 |
| ⚪ 风险/建议 | 10 | 见「风险与建议」 |
| 🛑 提交卫生 | 5 | 见「提交卫生」，其中隐私最优先 |

---

## 二、改动全貌：**是 4 条主线，不是 3 条**

预判的 3 条之外，验收发现了第 4 条此前无任何报告覆盖的改动：

| # | 主线 | 规模 | 关键文件 |
|---|---|---|---|
| A | Hyper-V 虚拟网卡独立管理页（vNIC） | `hyperv_adapter.go` +1044、`hyperv_adapter_test.go` +1504、`VirtualAdaptersPage.tsx` +500 / `.test.tsx` +764、`managementAdapters.ts` +314、Go 侧共 27 文件 | Go 服务 + Wails 绑定 + 页面 + 导航 + i18n |
| B | 自动更新功能整体删除 | 删 11 个文件（含 `updater*.go` 8 个、`update-manifest-ed25519` 公钥、`cmd/update-manifest-sign`） | updater 链、发布签名链、设置项 |
| C | 关于页 + 应用内赞助入口删除 | 删 `AboutPage.tsx/.test.tsx` + 3 张二维码图片 | shell / 导航 / CSS |
| D | **fork 重品牌化（此前无人报告）** | `main.go` 窗口标题 `HypoMux 自定义版`、`product.ts` 新增 `edition` 字段、`TitleBar.tsx` 引用 | fork 专属化 |

---

## 三、质量闸门（两条腿独立复现，互为交叉验证）

| 闸门 | 命令 | 结果 | 出处 |
|---|---|---|---|
| Go 编译 | `go -C desktop build ./...` | exit 0（1.2s） | 01、03 |
| Go 静态 | `go -C desktop vet ./...` | exit 0（0.6s） | 01、04 |
| Go 测试 | `go -C desktop test -count=1 ./...` | exit 0（51.6s）**PASS 570 / FAIL 0 / SKIP 9** | 01 |
| Go 竞态 | `go test -race -count=1 ./internal/services/...` | exit 0（92.2s），**无 DATA RACE**（本机 mingw-w64 gcc 可用） | 01 |
| Go 格式 | `gofmt -l .` | 零输出 | 01、07 |
| Go 依赖 | 临时副本 `go mod tidy` | exit 0，**go.mod/go.sum 逐字节一致**（删 updater 无需 tidy） | 01 |
| 前端依赖 | `pnpm install --frozen-lockfile` | exit 0（3.7s），lock 与 package.json 同步 | 02 |
| 类型检查 | `tsc --noEmit`（TS 5.9.3, strict + noUnusedLocals） | exit 0，零错误 | 02、03、05、07 |
| 前端测试 | `vitest run` | exit 0（33.2s）**44/44 文件、409/409 用例、0 失败** | 02、03、07 |
| 生产构建 | `vite build --mode production` | exit 0（4.9s），2248 模块，产物 1.256 MB | 02 |
| 跨平台编译 | `GOOS=linux/darwin go build ./internal/services/` | OK（build tag 完整） | 04 |
| CI YAML | 4 个 workflow 解析 | 全部可解析，**对已删符号零引用** | 06、03 |
| Lint | — | **仓库无 lint script，也无 eslint/biome 配置**，静态闸门只有 tsc | 02 |

> 9 个 SKIP 全是显式环境变量门控（逐条列在 01 报告），**本轮新增的 Hyper-V 测试无一被跳过**。

---

## 四、功能需求逐条核对（用户拍板的 6 条）

| # | 需求 | 结论 | 证据 |
|---|---|---|---|
| 1 | 每张能检测到的虚拟网卡都有移除按钮（不分是否本软件创建） | ✅ 落地 | `removeAdapters` 放开命名+台账两道闸门（`hyperv_adapter.go:2123`）；UI 单删明确走批量 API（`VirtualAdaptersPage.tsx:480-489` 注释+调用） |
| 2 | 刷新旁批量多选删除 | ✅ 落地 | `HyperVAdapterPanel.tsx` 多选 + `services.ts:377` 整数组单次调用 |
| 3 | 硬排除只有 `Container NIC*` 与 `vEthernet (Default Switch)` | ✅ 后端 4 形态覆盖 / ⚠️ 前端镜像只覆盖 2 形态 | 后端 `hyperv_adapter.go:2012-2028`；前端 `managementAdapters.ts:117-150`（P2-2） |
| 4 | 合成**一次**提权脚本删 N 张（而非线程并发） | ✅ 落地 | `hyperv_adapter.go:2202-2203` 单一 envelope 单次 `runScript`；测试用 `toHaveBeenCalledTimes(1)` 钉死 |
| 5 | 台账 `data/hyperv/adapters.json` 删除后重写 | ✅ 落地 | 重读实况 + 先删成功再清台账（`hyperv_adapter.go:2220-2239` 注释说明了顺序理由） |
| 6 | 三重归属判定（命名 → 台账 → MAC 逐字节） | ✅ 落地 | `hypervMACMismatchReason()`（`hyperv_adapter.go:2048`） |

**安全面**：批量脚本走 `-EncodedCommand` + Base64 payload，读脚本路径零字符串插值，**无命令注入面**；脚本按 `DeviceId` 定位而非网卡名。

---

## 五、🔴 必改项

### P0-1 单卡删除超时错配 → 会误报失败并诱导二次 UAC 🔴
- 现象：单卡删除前端预算 **60s**，Go 侧这一批的预算 **90s**。`withServiceTimeout` 只 reject awaiter、**底层调用继续跑**（`services.ts:268` 注释自陈）→ 60s 时前端报失败，网卡其实还在删，用户重试触发**第二次 UAC**。
- 位置：`desktop/frontend/src/pages/VirtualAdaptersPage.tsx:485-489` + `desktop/frontend/src/platform/services.ts:275` vs `desktop/internal/services/hyperv_adapter.go:42`（`hypervRemoveScriptTimeout = 90 * time.Second`）+ `:2037-2046`（`hypervRemoveBatchTimeout(1) = 90s`）+ `:2203`
- **主控已独立复核**：Go 侧 `hypervRemoveBatchTimeout(1)` 确实返回 90s > 前端 60s。
- 修法：前端单删改用批量预算 `HYPERV_ADAPTER_BATCH_TIMEOUT_MS`（180s），或把 Go 单张预算降到 60s 以下。**注意别简单调大前端预算**——`services.ts:277-285` 已说明「预算先到期是最坏结果」。

### P0-2 选择态/剪枝用 `name` 当身份 → 同名网卡会串行误删 🟠
- 现象：勾选状态与 `dropAdapters` 剪枝都以网卡名匹配，而仓库**已有**稳定行键 `adapterRowKey()`，却只用作 React `key`。存在两张同名网卡时，勾一张=选两张、删一张抹掉两行。
- 位置：`HyperVAdapterPanel.tsx:176/183/521`、`managementAdapters.ts:166-174`（主控已复核 `dropAdapters` 按 `row.name` 过滤）vs `managementAdapters.ts:227-228`（已有 rowKey）、`:495`
- 根治需后端在 `HyperVRemoveResult` 补 `adapterId`/`MAC`（现只有 name/removed/reason/interface，`hyperv_adapter.go:209-214`）——**是否动冻结字段 §3.2 需你拍板**。

### P1-1 单张 `Remove()` 漏读脚本逐条失败 → 归属丢失后永远删不掉 🔴
- 现象：单张路径只判 `runErr`，**从不读 `result.Failures`**，脚本回报单张失败仍标 `removed=true` 并清台账、清出口池 → 网卡还在但归属已丢，此后永远删不掉。注释 `:2205-2206` 声称「脚本一旦没报错，这张卡就一定删掉了」，**与实现相反**。批量路径走 `hypervReconcileRemoveRows`（`:2216`）是对的。
- 位置：`desktop/internal/services/hyperv_adapter.go:2204-2214`（主控已逐行复核）
- 定性：**潜伏缺陷**——UI 单删走批量 API，`Remove` 目前只有 Wails 绑定导出、前端零调用方（`services.ts:374` 暴露但无 page 调用）。
- 修法：复用 `hypervReconcileRemoveRows`，或让单张路径把 `Failures` 转成 error。

### P1-2 便携 zip 进了仓库却进不了 Release 🟠
- 现象：`build.yml:463` 上传 `HypoMux-Portable-Windows-*`，但全仓唯一 `download-artifact`（`build.yml:506`）只取 `HypoMux-Windows-*` → **Release 里只有安装包，没有便携包**。
- **主控已独立复核**（上传名/下载名逐条比对确认）。
- 修法：`download-artifact` 加 `pattern: HypoMux-*` + `merge-multiple`，或发布 job 显式消费便携产物。

### P1-3 Authenticode 信任校验静默空跑 🟠
- 现象：`build.yml:296-301`「Verify production installer trust policy」跑 `-run TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller`，而该测试随 `updater_windows_test.go` 被删 → 实测 `no tests to run` 且 **EXITCODE=0**，production/publish 模式**不再校验签名**。`installer_layout_test.go:370` 仍固化 `HYPOMUX_SIGNED_INSTALLER_TEST` 变量。
- **主控已独立复核**（步骤与被删测试名对照）。
- 修法：把该测试迁到不被删除的位置（如 `installer_layout_test.go`），或删掉步骤并同步 `:370`；**绝不能让它继续"绿着但不测"**。

### P2-1 文档漂移三处 🟡
1. `README_EN.md:24,37,50,59,212` **整份漏改**（不在 diff 内），仍宣称 stable/preview update channels、update checks、Ed25519 signed update manifest；中文 `README.md` 同五行已改写 → 中英文对不上。
2. `.github/release-notes/v2.7.0.md:97,106` 与 `v2.7.0.en.md:97,106` 仍描述已删的 Ed25519 自动更新链，而 `product.ts` 的 version 就是 **2.7.0**、`README.md:26` 直链该文件。
3. `docs/README.md:3` 索引仍写「更新渠道选择」，目标章节已改名。
- 建议顺带加一条文档护栏测试（现有 `installer_layout_test.go:322-355` 只防 CI token，不防 README 宣称）。

---

## 六、⚪ 风险与建议（非阻塞）

**功能与一致性**
- 批量删除缺回归防护：`fixture.timeouts` 只写不读（`hyperv_adapter_test.go:2049/2062/2078`）→ 把 `hyperv_adapter.go:2203` 改成写死 90s，整套测试仍全绿；「而非并发」这条方案约束只断言了「脚本调 1 次」，脚本内串行证据 `hyperv_adapter.go:2664` 无人读，禁用名单 `test:1221-1232` 缺 `Start-Job`/`-Parallel`/`Invoke-Expression`。
- 失效断言 `test:2354-2357`：拿代入后的运行时文案与未代入的格式串模板比较，`!=` 恒真；而专为"逐字一致"抽出的 `hypervMACMismatchReason()`（`hyperv_adapter.go:2048`）全测试文件零引用。
- 后端注释与需求**反向**：`hyperv_adapter.go:1329-1335`、`:1348-1351` 仍称台账外卡是「只读展示、Remove 直接拒绝、最后一道闸门」，而需求 1 已按你拍板放开了这道闸门 —— 会误导后续维护者。
- 测试覆盖缺口：`List()` 仅 6.1%（合并循环零测试，而 `inventoryHook` 已可用于补测），`hyperv_adapter_windows.go` 0%；"部分失败后失败卡留在列表可重试"这个最关键 UX 契约零覆盖——`VirtualAdaptersPage.test.tsx:917-952` 造的 `xuni-02` 根本不在 rows 里，实际断言的是另外两张。
- 恒真断言：`VirtualAdaptersPage.test.tsx:1108/1120` 断的 `mocks.remove` 是已废弃路径，前端全仓零调用方。
- 保护名单前后端两份无编译期约束的镜像（后端 4 形态 / 前端 2 形态），注释自承「必须在两处同时改」。
- UX：批量确认弹窗文案未写「将删除 N 张 / 可能断网」（创建侧有断网 banner，删除侧无对等物）；失败原因会被 `notificationMessage.ts:47-77` 折叠/关键词替换（如含「容器网络」的保护原因被换成「网络暂时不可用」），批量 N>2 时默认可见文本只剩第 1 条。
- 可见行为变更：`HideVirtualAdapters` 默认 true→false，建议在发布说明显式提及。

**工程环境**
- `.gitattributes` 只写 `* text=auto`，没给 `*.mod`/`*.sum` 钉 `eol=lf`，叠加本机 `core.autocrlf=true` → `go mod tidy -diff` 退出码 1 是**假阳性**（59 删 59 增、内容相同）；真实危害是任何 Windows 用户跑一次 tidy 或 `wails3 task` 就凭空产生约 30 行 git 改动。建议加 `go.mod text eol=lf` / `go.sum text eol=lf` 两行。
- `isVirtualAdapter` marker 表无 `hypomux`（`desktop/internal/services/adapters.go:142-156`）：改名后的 `HypoMux-vnic-*` 会漏判为物理网卡，污染聚合选源；`adapter_visibility_test.go:10-36` 无对应用例。
- 既有孤儿依赖 4 个（`pixi.js`/`pixi-live2d-display`/`fflate`/`fake-indexeddb`，HEAD 即零引用，闭包 41 包≈35.9 MB，对 dist 零影响）。
- 既有存量债：186 个 i18n 孤儿键、`.hotspot-qr`(app.css:5411-5412)/`.virtual-adapter-section`(vnic.css:4-6) 孤儿 CSS —— **均非本次引入**。
- 裸 clone 直接跑前端 build 会失败（`tsconfig.json:24`、`vite.config.ts:12` 依赖被 gitignore 的 `bindings/`）；CI 已用 `build.yml:139-142` 的 `wails generate module` 覆盖，建议写进上手文档。

---

## 七、🛑 提交卫生（放行前必须处理）

1. **🔴 隐私泄露**：~~`reports/` 下 59 份报告含**本机路径 ×93**、**主机名 `<HOST>`**、**真实内网 IP `192.168.16.x`**（本机 `.<masked>` 与用户自建 vNIC 同段物理网卡）。这是公开 fork，泄露不可撤回 → **提交前必须脱敏或整体 gitignore**。~~
   **✅ 已于本轮闭环**：脱敏覆盖 48 个文件，本机路径 → `<repo>`/`%USERPROFILE%`/`%LOCALAPPDATA%`、主机名 → `<HOST>`、用户名 → `<ADMIN>`、内网 IP → `192.168.16.<masked>`、MAC → `<MAC>`、机器 GUID → `<GUID>`；**计划外追加发现并掩码**：个人邮箱（`95-ci-run.md`）、DHCP 客户端标识 `0x…`、7 处 IPv6 链路本地地址、Windows 8.3 用户目录短路径。复核：残留敏感命中 **0**、NUL 字节 **0**、67/67 文件 UTF-8、行数零回归、无 mojibake。保留项仅为产品设计网段（`192.168.16.0/24`、`10.66.*`）、测试夹具、公网 DNS、公开 SignPath 组织 id 与 Windows 协议标识符（`requireAdministrator` 等 22 处 —— 报告本身在教人 grep 这些名字，改掉会让报告说谎）。
2. **`desktop/cover_hyperv`**（459,303 B / 4,920 行 Go coverage profile）：跑测试的遗留产物、`.gitignore` 未覆盖、`git add -A` 会扫入。**已由我在验收收尾时删除**，建议补进 `.gitignore`。
3. ~~**3 个 UTF-16LE 编码 txt**~~ **✅ 已于本轮闭环**：3 个文件（行数 10/998/87 完全不变）已转 UTF-8 无 BOM，NUL 归零，另归一了 `_phase3_datadir.txt` 的 BOM，全目录编码一致。
4. **5 个文件跨主线混改**，需 `git add -p` 逐 hunk 拆：`App.tsx`、`i18n/legacy.messages.json`、`app.css`、`settings.go`、`SettingsPage.tsx`。
5. **建议的提交拆分**（4 条主线各自成 commit，报告单独处理）：
   - `feat: Hyper-V 虚拟网卡独立管理页（含批量删除）`
   - `refactor: 移除自动更新功能与签名清单链`
   - `refactor: 移除关于页与应用内赞助入口`
   - `feat: fork 重品牌化（edition / 窗口标题）`
   
> ⚠️ **陷阱记录**：`git check-ignore -v reports/` 在本机 Git 返回 rc=0 的**伪匹配**（对不存在的目录 `zzz_nonexistent_dir_xyz/` 返回完全相同结果，且匹配到的 `.gitignore` 第 72 行实为空行）。判断是否被忽略只能用 `git ls-files --others --exclude-standard`。

---

## 八、成员分歧与主控裁定

| 分歧 | 裁定 |
|---|---|
| 单删路径：后端腿说「UI 不走 `Remove`，是潜伏缺陷」；前端腿说「UI 单删走 `removeAdapters([name])`」 | **两者都成立**。缺陷在 `Remove`（requireLedger=true）这条无 UI 调用方的路径；UI 单删走批量 API，但**另有超时错配**（P0-1）。已核验 `VirtualAdaptersPage.tsx:480-489` 的注释与调用 |
| 未跟踪文件数：预判 58 vs 实测 60 | 一致 —— 59 份报告 + `cover_hyperv`（已删），现已全部落在 `reports/` 下 |
| `about-removal` 报告声称的「任务前 45 files / 413 tests」基线 | **不成立**：HEAD 顶层测试声明仅 268 个，该基线只能测自已含 vNIC 测试的中间态树。报告结论本身可精确复现，属**口径问题非编造** |
| `95-ci-run.md` §6「禁止加入 reports」 | 追溯 SHA 祖先关系成立但已过期（HEAD 已在其后两次提交），仅靠一次性 pathspec 维持 |

---

## 九、覆盖缺口（未臆断通过）

- **CI 未实跑**：SignPath 签名轮询、CNB release API、`softprops/action-gh-release` 实际上传、跨 job artifact 传递；`actions/upload-artifact@v7` / `download-artifact@v8` / `setup-go@v7` / `setup-node@v6` 这些 tag 是否真实存在（本次 diff 未改动，无法离线确认）。
- **真机 Hyper-V 行为未验**：`test:2180` 断言 `vEthernet (WSL)` 不会进 `List()` 输入域，但该前提取决于 WSL 交换机的 `AllowManagementOS`，**代码判定不了**——若真出现，HypoMux 会给 WSL 基础设施发移除按钮（与 Default Switch 同类）。建议排进真机验收清单。
- 非 Windows 平台只验证到**编译通过**，未运行。
- 仓库无 lint 维度，静态质量只有 tsc。
- 前端测试为 jsdom 单测，未做真实 Electron/Wails 端到端。

---

## 十、放行建议（按此顺序最省事）

1. **修 3 个功能缺陷**：P0-1（超时）、P0-2（身份键）、P1-1（漏读 Failures）+ 同步 3 处反向注释 —— 都集中在 `hyperv_adapter.go`、`VirtualAdaptersPage.tsx`、`services.ts` 三个文件。
2. **修 2 处 CI**：便携产物接线（P1-2）+ 空跑校验（P1-3）—— 否则下次发布 Release 里没有便携包，且签名校验形同虚设。
3. **补 3 处文档**：README_EN.md、v2.7.0 双语发布说明、docs/README 索引。
4. **脱敏 reports/**（或加 `reports/.gitignore`），补 `.gitignore` 的 cover profile 规则。
5. **按 4 条主线拆 4 个 commit**（5 个混改文件用 `git add -p`）。

> 可选加固（不阻塞发布）：把 `List()` 与 `hyperv_adapter_windows.go` 补上测试、读上 `fixture.timeouts`、修失效断言、给 README 加防宣称护栏测试、给 `.gitattributes` 钉 `eol=lf`。

---

## 十一、成员报告索引

| 报告 | 主题 |
|---|---|
| [01-go-build-tests.md](01-go-build-tests.md) | Go 编译/vet/测试/race/格式/依赖残留 |
| [02-frontend-checks.md](02-frontend-checks.md) | 前端依赖一致性/类型/测试/构建 |
| [03-dangling-refs.md](03-dangling-refs.md) | 死引用与孤儿残留审计（清理干净 26 项 / 残留 11 项） |
| [04-hyperv-go-review.md](04-hyperv-go-review.md) | Hyper-V 后端深审 + 测试可信度 + 冻结契约核对 |
| [05-vnic-frontend-review.md](05-vnic-frontend-review.md) | 虚拟网卡前端深审 + 前后端契约对照 |
| [06-ci-release-docs.md](06-ci-release-docs.md) | CI 步骤核对/发布链/版本元数据/文档 |
| [07-repo-hygiene-reports.md](07-repo-hygiene-reports.md) | 未跟踪文件盘点/报告一致性抽查/59 文件变更台账与拆分建议 |