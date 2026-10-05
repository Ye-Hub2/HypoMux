# 02 — 前端依赖一致性 / 类型检查 / 测试 / 构建 验收报告

- 范围：`desktop/frontend/`（pnpm + Vite 8 + React 18 + TypeScript 5.9 + vitest 4）
- 基线 HEAD：`fc821b16bd55630ac6cba4311179e1a1b6644ded`
- 运行时：Windows / node v24.21.0 / pnpm 11.7.0（DSH 内置 runtime）
- 本轮只验收，未改源码、未 `git add`、未改 lockfile、未删除任何已有目录。

## 验收结论

**有条件通过**

四条质量闸门（依赖一致性、typecheck、test、生产构建）**全部通过，零阻塞**：
`pnpm install --frozen-lockfile` 退出码 0、`tsc --noEmit` 退出码 0、`vitest run` 409/409 全绿退出码 0、`vite build --mode production` 退出码 0。

判「有条件通过」而非「通过」，原因只有一条，且**不是本次改动引入的**：`tsc` 与 `vite build` 依赖 `desktop/frontend/bindings/`，该目录被 `.gitignore` 排除（`desktop/.gitignore:9`），本机已存在才使类型检查与构建成立。裸 `git clone` 后直接跑 `build` 会在这一步失败——CI 已用 `wails generate module` 前置覆盖该步骤，故属「流程前置条件」而非缺陷。详见「覆盖缺口」。

本次三条改动主线（A 新增 Hyper-V 虚拟网卡管理页 / B 删除自动更新 / C 删除 About 页）**未引入任何孤儿依赖、未破坏类型、未产生失败用例、未使产物体积异常膨胀**。

## 阻塞问题

**无。** 未发现任何阻塞项。

以下为非阻塞观察项，供团队决定是否立项，**均不阻塞本轮验收**：

### 观察 1 — `pixi.js` / `pixi-live2d-display` / `fflate` / `fake-indexeddb` 为既有孤儿依赖（存量债，非本次引入）

- 位置：`desktop/frontend/package.json:17-19`（`fflate`、`pixi-live2d-display`、`pixi.js`）、`desktop/frontend/package.json:32`（`fake-indexeddb`）
- 反查结论：全仓 `desktop/frontend`（142 个 ts/tsx/js/json/html 文件）**零 import 命中**。`fake-indexeddb` 亦无 `import "fake-indexeddb/auto"` 形式。`jsdom` 看似未 import，实为 24 个测试文件首行 `// @vitest-environment jsdom` 注释驱动，`vite.config.ts` 走默认 `node` 环境——**不算孤儿**。
- 存量证据：`git grep -E "pixi|fflate|fake-indexeddb" HEAD -- desktop/frontend` 只命中 `HEAD:package.json` 与 `HEAD:pnpm-lock.yaml`，HEAD 源码同样零引用 → **HEAD 之前就已存在**，与本次「删除自动更新 / 删除 About 页」无关。
- 连带成本：`desktop/frontend/pnpm-workspace.yaml:4` 的 override `"pixi-live2d-display>gh-pages": "^6.3.0"` 只服务于这个无人引用的包。
- 体量：`node_modules/.pnpm` 下 41 个孤儿包闭包合计 **≈35.9 MB / 340.1 MB 总量**（`pixi.js` 11.6 MB、`gh-pages`、34 个 `@pixi/*` 子包等）。
- 复现命令：
  ```
  cd desktop/frontend; git grep -n -E "pixi|fflate|fake-indexeddb" -- src vite.config.ts
  ```
- 对构建产物**零影响**：`dist/assets` 中不存在任何 `@pixi` / `fflate` chunk（见构建产物清单），因为未被 import 即不会被 tree-shake 进来。

### 观察 2 — 自动更新删除后无残留依赖、无残留引用

- 前端**从未持有 updater 专用依赖**：HEAD 与当前 `package.json` 的 `dependencies`/`devDependencies` 中均无 electron-updater / update-electron-app 一类库；自动更新是 Go(Wails) 侧能力，前端只经 bindings 调用，故删除功能不产生前端孤儿包。
- 前端源码反查 `updater|autoUpdate|auto_update|AutoUpdate|checkForUpdates|Updat` 共 28 处命中，逐条核对**全部为同形异义**：`settingsQueue.ts:24,29` 的 `updater` 是 React state 更新函数；`RuleSetsPanel.tsx:290`/`ConnectionsPage.tsx:874`/`RoutingPage.tsx:1080` 是规则集「立即更新/更新规则」；`SteamCDNPanel.tsx` 是下载节点刷新；`i18n/legacy.messages.json:629` 的 `rulesets_update_now` 同属规则集。**无自动更新残留**。
- 删除防护已就位：`src/pages/SettingsPage.test.tsx:100-101` 断言 `combobox[name="Update channel"]` 与文本 `Update channel` 均不存在，用例名 `no longer exposes any AI settings or companion control` 通过。

### 观察 3 — About 页删除干净，只留一条回归守卫

- `src/components/shell/CompactNavigation.test.tsx:20` 断言 `queryByRole("button", { name: "nav_about" })` 为 `null`，用例 `lists every network entry and no AI entry` 通过。
- 全仓 `About|about` 命中 15 处，逐条核对：除该守卫外全部是 `HyperVAdapterPanel` / `VirtualAdaptersPage` / `managementAdapters` 源码里的英文注释句中 "about" 一词，无 `AboutPage` import、无路由残留。

### 观察 4 — 测试 stderr 出现未捕获 `ReferenceError: NodeFilter is not defined`（不致失败）

- 出错栈原文：
  ```
  ReferenceError: NodeFilter is not defined
      at processNode (<repo>\desktop\frontend\node_modules\.pnpm\tabster@8.8.0\node_modules\tabster\dist\cjs\MutationEvent.cjs:89:9)
      at updateTabsterElements (...tabster@8.8.0\dist\cjs\MutationEvent.cjs:64:9)
      at MutationObserver.onMutation (...tabster@8.8.0\dist\cjs\MutationEvent.cjs:48:21)
      at MutationObserver.invokeTheCallbackFunction (...jsdom@30.0.1\lib\generated\idl\MutationCallback.js:19:26)
      at notifyMutationObservers (...jsdom@30.0.1\lib\living\helpers\mutation-observers.js:160:22)
  ```
- 出现位置：`src/pages/HomePage.test.tsx > HomePage virtual NIC cleanup > no longer mounts the retired Wintun virtual adapter card` 之后、VirtualAdaptersPage 队列前。
- 性质：Fluent UI 的 tabster 在 jsdom 30 缺少全局 `NodeFilter`，于 MutationObserver 回调内抛出。**未导致任何用例失败**（vitest 仍 409/409、exit 0），属噪声型异步错误。
- 风险：异步回调内异常不在 vitest 断言覆盖范围内，长期可能掩盖真实 MutationObserver 错误，也可能在 CI 更高并发下升级为偶发失败。**存量问题**，与本次三条主线无关（栈中无 VirtualAdapters / About / updater 符号）。
- 复现命令：
  ```
  cd desktop/frontend; node .\node_modules\vitest\vitest.mjs run src/pages/HomePage.test.tsx
  ```

### 观察 5 — pnpm 主版本与 CI 不一致（本次验证用 11.7.0，CI 用 10.34.5）

- `.github/workflows/build.yml:27` 固定 `PNPM_VERSION: 10.34.5`，并由 `:101-103` 断言版本不符即 fail。
- 本轮用 DSH 内置 pnpm **11.7.0** 验证（PATH 中无 pnpm）。lockfile 为 `lockfileVersion: '9.0'`，pnpm 10/11 均可读写，结论可迁移；但严格意义上 CI 所用 pnpm 版本**本轮未被实际验证**。属覆盖缺口，非缺陷。

## 风险与建议

1. **`bindings/` 缺失会让 clone 后的直跑构建失败（建议写进验收/上手文档）。**
   `desktop/frontend/tsconfig.json:24` 的 `include` 含 `"bindings"`，`vite.config.ts:12` 的 `wails("./bindings")` 同样指向它。裸 clone 后 `pnpm build` 会因找不到 bindings 而失败。CI 已通过 `.github/workflows/build.yml:139-142` 的 `wails generate module` 前置规避。建议在 README/贡献指南写明「首次构建需先 `wails generate module`」。

2. **建议清理 4 个既有孤儿依赖（约省 35.9 MB 安装体积、41 个包）。**
   移除 `pixi.js`、`pixi-live2d-display`、`fflate`、`fake-indexeddb` 四条 package.json 条目 + `pnpm-workspace.yaml:4` 的 `pixi-live2d-display>gh-pages` override，并重跑 `pnpm install` 更新 lockfile。对产物零影响（已验证不进 bundle），可显著缩短 clone→install 时间。**注意**：属存量债，建议独立 PR，不与本轮三条主线混提。

3. **建议给 vitest setup 注入 `NodeFilter` polyfill。** 一行 `globalThis.NodeFilter = ...`（或 setup 文件里 mock）即可消除观察 4 的异步噪声，让 MutationObserver 内的真实错误将来能被看见。属可选清理。

4. **本次改动未触碰 lint 维度——仓库根本没有 lint。** `package.json:6-12` 只有 `dev` / `build:dev` / `build` / `preview` / `test` 五条 script（与 HEAD 完全一致），无 `lint`、无独立 `typecheck`；仓库内无 `.eslintrc*` / `eslint.config.*` / `biome.json`。当前唯一静态质量闸门是 `tsc`。若团队希望提升前端质量基线，可评估引入 ESLint，但这超出本轮验收范围。

5. **通知文案断言约定与本次改动一致，未见脆弱断言。** 按项目约定核对（正文经 `notificationMessage.ts` 折叠至 72 字可见摘要，error 再经 `conciseDiagnosticMessage` 精简；warning/success Toast `timeout: 0`）：本次新增的 `VirtualAdaptersPage.test.tsx > action-failure notifications` 系列 3 个用例**断的是 `notify` 入参**，未断 DOM，全部通过，无脆弱断言。

## 执行记录

### package.json 全部 scripts（`desktop/frontend/package.json:6-12`）

| script | 命令 |
| --- | --- |
| `dev` | `vite` |
| `build:dev` | `tsc && vite build --minify false --mode development` |
| `build` | `tsc && vite build --mode production` |
| `preview` | `vite preview` |
| `test` | `vitest run` |

**无 `lint`、无独立 `typecheck` script。** `tsc` 通过 `build` / `build:dev` 隐式执行。5 条 script 与 HEAD 完全一致，本次未增删。

### 命令 / 退出码 / 耗时

工作目录均为 `<repo>\desktop\frontend`；pnpm 经 `node %USERPROFILE%\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\pnpm\bin\pnpm.mjs` 调用（pnpm 不在 PATH）。

| # | 目的 | 命令 | 退出码 | 耗时 |
| --- | --- | --- | --- | --- |
| 1 | 依赖一致性 | `pnpm install --frozen-lockfile` | **0** | 3.686 s（pnpm 自报 `Done in 666ms`） |
| 2 | 类型检查 | `node .\node_modules\typescript\bin\tsc --noEmit`（TypeScript 5.9.3） | **0** | 5.425 s |
| 3 | lint | — | 不适用 | 仓库无 lint script / 无 eslint·biome 配置 |
| 4 | 全量测试 | `node .\node_modules\vitest\vitest.mjs run --reporter=verbose`（vitest 4.1.11） | **0** | 33.238 s（vitest 自报 `Duration 32.43s`） |
| 5 | 生产构建 | `node .\node_modules\vite\bin\vite.js build --mode production`（vite 8.1.5） | **0** | 5.242 s（vite 自报 `built in 4.89s`） |

命令 1 原始输出：
```
✓ Lockfile passes supply-chain policies (verified 19m ago)
Lockfile is up to date, resolution step is skipped
Already up to date
Done in 666ms using pnpm v11.7.0
EXITCODE=0
ELAPSED_MS=3686
```
`resolution step is skipped` + `Already up to date` 即 pnpm 对「package.json 与 pnpm-lock.yaml 完全同步」的判定；**无需 `--lockfile-only` 定位差异，也未触碰 lockfile**。

命令 4 原始统计尾部：
```
 Test Files  44 passed (44)
      Tests  409 passed (409)
   Start at  21:59:58
   Duration  32.43s (transform 50.16s, setup 0ms, import 244.98s, tests 71.59s, environment 62.11s)
EXITCODE=0
ELAPSED_MS=33238
```

### 测试统计

- **失败用例：0。失败断言原文：无。**
- 通过：**44/44 测试文件，409/409 用例**（vitest 4.1.11，Node v24.21.0）。
- 三大主线新代码的用例均全绿：
  - `src/pages/VirtualAdaptersPage.test.tsx`：create flow 5、create quota 10、remove flow 3、batch removal 6、DHCP pacing 1、write serialization 2、browser preview 1、action-failure notifications 3 —— **31 个全过**。
  - `src/components/vnic/managementAdapters.test.ts`：deriveAdapterState 4、managed/pool flags 3、summarizeAdapters 1、row identity 2、validateCreateCount 9、createQuota 7、switch filtering 4、mergeAdapterBatch 6、splitBackendHint 8、needsRestartPrompt 4、switchAvailability 4、isAdapterRemovable 5、dropAdapters 2、partitionRemoveResults 4 —— **63 个全过**。
  - `src/components/vnic/HyperVAdapterPanel.test.tsx`：state mapping 6、removal policy 4、multi-select 11、toolbar and messages 8、restart prompt 4、busy gate 1、retry gate 3、create budget 4、switch availability 3、backend hints 5 —— **49 个全过**。
- stderr 中的其他输出均**非失败**，逐类甄别：
  1. `⚠️ Browser Environment Detected / Only UI previews are available in the browser…` —— Wails 在 jsdom 下探测到非桌面环境，出现在 `ConnectionsPage` / `RoutingPage` / `RuleSetsPanel` / `SettingsPage` / `HomePage` / `NATDetectionPage` / `ThroughputDisplay` 测试。**环境提示，非断言失败**；其中 `VirtualAdaptersPage.test.tsx > browser preview > renders fixtures, shows the preview bar and refuses every write` 是**故意的正向用例**。
  2. `settings save failed: Error: offline at src/pages/SettingsPage.test.tsx:237` —— 该用例刻意 mock 拒绝以验证「保存与恢复都失败时草稿仍可编辑重试」，**通过**。
  3. `Warning: A component is changing a controlled input to be uncontrolled…` —— 来自 `src/pages/SettingsPage.tsx:144` 的 `SettingSwitch`，用例 `preserves edits typed while an earlier manual save is in flight` **通过**；React 18 dev warning，非测试失败。
  4. `ReferenceError: NodeFilter is not defined` —— 见「阻塞问题 → 观察 4」，**不影响退出码**。

### 构建产物

- `vite build --mode production`：**成功**，`✓ 2248 modules transformed`，`✓ built in 4.89s`，**无 unused chunk / 未使用动态导入（`(!)`）警告**。唯一警告为 `[PLUGIN_TIMINGS]`，纯性能提示：
  ```
  [PLUGIN_TIMINGS] Your build spent significant time in plugins. Here is a breakdown:
    - wails-typed-events (93%)
    - vite:css-post (4%)
  ```
- 产物总量（构建前后一致，说明本机 dist 已是当前改动后的产物，可作体积基线）：
  - **`dist` 总计 1,316,655 B = 1.256 MB，47 个文件**
- 最大 5 个 JS chunk：

| chunk | 原始 | gzip |
| --- | --- | --- |
| `AppNotifications-DySofnoC.js` | 355.78 kB | 108.42 kB |
| `RoutingPage-1X83G03t.js` | 112.36 kB | 33.09 kB |
| `ToolsPage-cutvbKg7.js` | 66.94 kB | 24.16 kB |
| `HealthPage-BvIDgwvo.js` | 66.24 kB | 21.67 kB |
| `index-DuR16ToE.js` | 65.47 kB | 23.54 kB |

- 新页面 chunk：`VirtualAdaptersPage-D95UYVQe.js` 21.54 kB（gzip 5.99 kB）+ `VirtualAdaptersPage-BSLZI-4c.css` 6.09 kB（gzip 1.30 kB）—— 新增主线 A 的体积代价温和，**无异常膨胀**。
- 主 CSS 仍是 `index-BU5e4tW3.css` 145.10 kB（gzip 25.13 kB），与新增页无关。
- `AppNotifications` 单 chunk 355.78 kB 为最大项（Fluent UI 通知岛），属存量特征。
- 产物扫描：`dist/assets/*.js` 中 **不含** `react-markdown` / `remark-gfm` 字样；**不含**任何 `@pixi` / `fflate` 痕迹 —— 印证被删依赖与既有孤儿依赖均未进包。

### Wails bindings 与新服务的对接

`desktop/frontend/bindings/github.com/Hypostasis-Cat/HypoMux/desktop/internal/services/hypervadapterservice.ts` 存在，`src/platform/services.ts:4` 正确 `import * as HyperVAdapterService`，`:15-16` 从生成类型 `HyperVAdapterStatus` / `HyperVSwitch` 重导出为前端类型，`:274-286` 定义分级超时（读 10 s / 写 60 s / 创建 180 s / 批量 180 s）。`tsc --noEmit` 全绿 → 前端与生成的 Go bindings **类型完全对齐**，新页面的 Go 契约冻结到位。

## 覆盖缺口

1. **未在裸 `git clone` 全新工作区验证。** 本轮在既有工作区执行，`node_modules` 与 `bindings/` 均已存在。因此**未验证**：空 `node_modules` 下 `pnpm install --frozen-lockfile` 的冷启动（受 `.npmrc` 的 `minimum-release-age=10080` 影响，新包会被拒——本地 store 已有缓存故未触发）；也未验证 `bindings/` 缺失时 `build` 的确切报错形态。建议由他人或下一轮在干净 worktree 上补跑一次。

2. **未用 CI 的 pnpm 版本复现。** CI 固定 pnpm **10.34.5**（`.github/workflows/build.yml:27`），本轮用 **11.7.0**。lockfileVersion 9.0 两者通用，结论应可迁移，但严格意义上 CI 版本未经实测。

3. **无 HEAD vs 工作区的产物体积差分。** 受「本轮不改源码、不切分支」约束，无法 checkout HEAD 做基线构建，故**只给出绝对体积**，未给出「本次改动导致的体积增减」。可推断 B/C 两条主线（删自动更新、删 About）应为**负向**（净减 react-markdown+remark-gfm 及其传递依赖），但未实测。旁证：当前 dist 体积与构建前逐字节相同（1,316,655 B），说明产物稳定可复现。

4. **无 lint 可执行。** 仓库无 lint 配置，本腿验收维度缺失，非执行疏漏。

5. **未做运行时/集成验收。** vitest 全为 jsdom 单元与组件测试，`VirtualAdaptersPage` 的真实 Hyper-V 交互（建卡、DHCP 租约、批量删除、重启提示）未在真实 Windows Hyper-V 主机上验证，需 Go 腿 + 手工验收覆盖。

6. **单次运行，未评估测试稳定性。** 未重复跑测试以检测 flake（尤其观察 4 的异步 `NodeFilter` 错误在高并发下是否可能升级为失败）。