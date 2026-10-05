# 摘除「关于」页 · 代码接线层交付（about-shell）

日期：2026-10-05 ｜ 分支：main ｜ 范围契约：`reports/about-removal/SCOPE.md` 的 `about-shell` 一节
未执行 `git add` / `git commit`，全部改动只在工作区。

---

## 1. 逐文件改动清单

### 1.1 删除文件（2 个）

| 文件 | 原行数 | 说明 |
| --- | --- | --- |
| `desktop/frontend/src/pages/AboutPage.tsx` | 87 | 整个「关于」页 |
| `desktop/frontend/src/pages/AboutPage.test.tsx` | — | 4 个用例，随页面一并删除（预期内用例数下降） |

删除前已核对两个绝对路径确在 `<repo>\desktop\frontend\src\pages\` 下。

### 1.2 `src/App.tsx`（-21 / +17）

四处改动，全部按语义处理而非机械删行：

1. **lazy import**：删掉 `const AboutPage = lazy(() => import("./pages/AboutPage")…)`（原 `:28`）。其余 8 个 lazy 声明未动。
2. **DEV 深链白名单**：
   ```diff
         requested === "health" || requested === "connections" || requested === "virtual-adapters" ||
         requested === "settings" ||
   -      requested === "blocked-domains" || requested === "about"
   +      requested === "blocked-domains"
       )) {
   ```
   `?page=about` 现在回落成 `home`（这是删页后的正确行为，不再是死链）。
3. **`pageOrder`**：删掉 `"about",` 一项。其余 9 项相对顺序不变 ⇒ `navigate()` 里 `pageOrder.indexOf()` 算出的前进/后退方向对剩余页面完全等价（`appearance` 的索引从 9 变 8，但它与其它页的相对先后没变）。
4. **渲染三元链**：删掉 `: target === "about" ? <AboutPage /> :` 分支，并把整条链**重新对齐为每级两空格**。原文件这条链的缩进本就不齐（`target === "settings"` 的冒号与 `target === "tools"` 的同级），删掉分支后我把剩余 8 级统一成规范嵌套。逻辑等价性核对过：删除的是一个独立分支，其余分支的判定顺序与条件表达式均未变，末支仍是 `: null` 兜底。

   ```tsx
   {target === "appearance" && import.meta.env.DEV
     ? <AppearanceLab />
     : target === "tools"
       ? <ToolsPage />
       : target === "settings"
         ? <SettingsPage
           adapterRuntime={connectionAdapters}
           onOpenBlockedDomains={() => navigate("blocked-domains")}
         />
         : target === "blocked-domains"
           ? <BlockedDomainsPage onBack={() => navigate("settings")} />
           : target === "health"
             ? <HealthPage adapterRuntime={connectionAdapters} enginePhase={enginePhase} />
             : target === "connections"
               ? <ConnectionsPage
                 initialAdapter={connectionsNavigation.adapter}
                 adapterRevision={connectionsNavigation.revision}
                 adapterRuntime={connectionAdapters ?? []}
               />
               : target === "virtual-adapters"
                 ? <VirtualAdaptersPage />
                 : target === "routing"
                   ? <RoutingPage />
                   : null}
   ```

### 1.3 `src/components/shell/CompactNavigation.tsx`（-14 / +2）

1. 图标 import 删掉 `Info24Regular,`（全仓仅此处用它的关于页导航按钮）。
2. `AppPage` 联合类型删掉 `| "about"`：
   ```ts
   export type AppPage = "tools" | "home" | "routing" | "health" | "connections" | "virtual-adapters" | "settings" | "blocked-domains" | "appearance";
   ```
3. 删掉 `.nav-bottom` 里的整个关于页 Tooltip+Button 导航项（原 `:123-133`）。

**关于 `activeButtonRef` 的判断**：任务书提示的「最后一个分支变成 else」风险在这里**不存在**——导航项是 `mainItems.map()` 渲染的，ref 条件 `active ? activeButtonRef : undefined` 在 map 内部；`.nav-bottom` 里剩下的 DEV 专用 Appearance Lab 按钮自带 `ref={navigationPage === "appearance" ? activeButtonRef : undefined}`。删除后**两条 ref 赋值路径都还在**（mainItems 的 7 项 + DEV 按钮），选中指示器（`.nav-selection-window`）逻辑不受影响。

**`.nav-bottom` 容器保留**：它现在只包 DEV 专用按钮。我核对了 `app.css:526-539`——`.nav-bottom` 的样式是 `position/z-index/display:flex/gap` + `margin-top:auto`，**没有 padding、没有 border、没有 min-height**，空容器渲染高度为 0，在生产构建（`import.meta.env.DEV === false`）下不会留下空隙。**因此不需要跨作用域改 CSS。**

### 1.4 `src/components/shell/CompactNavigation.test.tsx`（+1）

原用例断言了 7 个导航按钮，**没有断言关于页**，所以删除导航项不会让它变红。我只**追加**了一条负向断言防回流，未删除任何既有断言：

```tsx
expect(screen.getByRole("button", { name: "nav_settings" })).toBeTruthy();
expect(screen.queryByRole("button", { name: "nav_about" })).toBeNull();
```

（测试里 `t` 被 mock 成返回 key，所以关于项的 `aria-label` 就是 `nav_about`。）

### 1.5 `src/platform/desktop.ts`（-1 / +1）

- 删除 `openURL: (url: string) => Browser.OpenURL(url).catch(ignoreOutsideWails),`
- import 收窄：`import { Browser, Call, Window } from "@wailsio/runtime"` → `import { Call, Window } from "@wailsio/runtime"`
  （`Browser` 在本文件里**只被 `openURL` 用**，不摘会让 `noUnusedLocals` 报错）
- **`ignoreOutsideWails` 保留**：删掉 `openURL` 后它仍被 `closeWithAnimation`(:24)、`callAppearance`(:42)、`resizeTray`(:65)、`minimise`(:68)、`toggleMaximise`(:69)、`hideToTray`(:72)、`show`(:73)、`showStartup`(:77)、`quit`(:78) 共 9 处使用，不是死代码。

### 1.6 `src/product.ts`（-5 / +1）

```ts
export const productInfo = {
  name: "HypoMux",
  // 本仓库是上游开源项目的 fork，自行构建分发，界面统一标注为自定义版。
  // 改这里要同步 desktop/main.go 里主窗口的原生标题。
  edition: { zh: "自定义版", en: "Custom Edition" },
  version: "2.7.0",
} as const;
```

保留 `name` / `edition` / `version`（TitleBar 在用，且 TitleBar 不在我的作用域、必须原样保留）。

---

## 2. 「删前零引用」的 grep 证据

搜索范围：`desktop/frontend/src`（全前端源码），pattern 见下表。

| 目标 | pattern | 命中 | 判定 |
| --- | --- | --- | --- |
| `openURL` | `openURL` | 仅 `AboutPage.tsx:34,37,54,56` 与 `AboutPage.test.tsx:11,12,48,51,56` | 页面删除后归零 ✅ |
| `Browser` | `Browser` | 仅 `desktop.ts:1`（import）与 `:80`（唯一使用点） | 摘 import 安全 ✅ |
| `productInfo.build` | `productInfo\.build` | 仅 `AboutPage.tsx:29`、`AboutPage.test.tsx:39` | 归零 ✅ |
| `productInfo.website` | `productInfo\.website` | 仅 `AboutPage.tsx:34`、`AboutPage.test.tsx:48` | 归零 ✅ |
| `productInfo.repository` | `productInfo\.repository` | 仅 `AboutPage.tsx:37`、`AboutPage.test.tsx:51` | 归零 ✅ |
| `productInfo.releases` | `productInfo\.releases` | **删除前就已零命中**（上一轮摘除更新链路时成为死字段） | 归零 ✅ |
| `Info24Regular` | `Info24Regular` | 仅 `CompactNavigation.tsx:8`（import）与 `:131`（图标 JSX） | 归零 ✅ |
| `AppPage` 的 `"about"` | `"about"` | `App.tsx:74,96,150`、`CompactNavigation.tsx:18,125-129` | 全清 ✅ |
| `ignoreOutsideWails` | `ignoreOutsideWails` | 9 处（见 1.5） | 保留 ✅ |

**删除后的收尾 grep**（同 pattern 全量再搜一遍）：

```
pattern: AboutPage|openURL|productInfo\.(build|website|repository|releases)|Info24Regular|"about"|Browser\.
结果：No matches found
```

`desktop/frontend/bindings/` 未触碰（gitignore + wails 重新生成）；`openURL` 走的是 `@wailsio/runtime` 的 `Browser`，不是绑定文件，所以无绑定需要清理，`tsc` 也未因此报错。

---

## 3. 验证结果

```
cd desktop/frontend
node node_modules\typescript\bin\tsc --noEmit
→ 无输出，tsc exit=0

node node_modules\vitest\vitest.mjs run
→ Test Files  44 passed (44)
→ Tests       409 passed (409)
→ vitest exit=0
```

**用例数变化符合预期**：任务开始前基线是 **45 files / 413 tests**；删除 `AboutPage.test.tsx` 一个文件（4 个用例）⇒ `45-1=44` files、`413-4=409` tests，实测完全吻合，**零丢失零新增失败**。

（`SettingsPage.test.tsx` 的 `settings save failed: Error: offline` 是该用例故意构造的 stderr 输出，测试本身 PASS，与本次改动无关。）

---

## 4. 需要 lead 处理 / 跨作用域的事项

1. **`nav_about` i18n 键仍在**（`src/i18n/legacy.messages.json:98` 中文「关于」、`:486` 英文「About」）。我这侧已无任何 `t("nav_about")` 调用，删除键属于 `about-assets-css` 的写作用域，**请确认他们已处理**；若漏了，中文键会变成无人引用的孤儿文案。
2. **`settings_about` / `settings_sponsorship_text` 等键**：grep 结果显示它们原本只被 AboutPage 引用（AboutPage.tsx:49、:71），随页面删除已归零，删除工作同样属于 `about-assets-css`。
3. **`.about-*` / `.signpath-*` / `.sponsor-section` / `.payment-*` CSS 与 `public/support/{wei.png,zhi.jpg,SignPath/}`**：不在我作用域。注意 `public/support/icon.ico` 必须保留（应用图标源文件），仓库根 `support/` 也必须保留（README 引用）。
4. **`app.css` 无需为本次改动调整**：已在 1.3 核对 `.nav-bottom` 空容器不留空隙。`app.css:4786` 共享选择器组里的 `.about-page,` 那一行属于 `about-assets-css` 的删除清单——**请提醒他们只摘这一项，保留同组的其它选择器**（该组是全应用共用规则）。

---

## 5. 判断依据与遇到的问题

1. **没有删测试里的既有断言**。`CompactNavigation.test.tsx` 原本就没断言关于页，我只加了负向断言；如果反过来为了"让测试变绿"去删断言，就会丢掉防回归能力。
2. **重排 App.tsx 三元缩进是主动行为，不是顺手格式化**。删掉中间一层分支后原缩进会明显参差（同级冒号对不齐），我统一成每级两空格。判定标准是**逻辑等价**：`pageOrder` 相对顺序未变、八个分支的判定表达式逐字未动、末支仍是 `: null`。这属于本文件本就在改的代码块，未触碰任何其他文件。
3. **`AppPage` 类型是全应用共享契约**。删掉 `"about"` 后任何仍 `navigate("about")` 的地方都会编译报错——`tsc --noEmit` exit 0 即证明不存在遗漏调用点，这比 grep 更强。
4. **未发现需要 `bindings/` 清理的编译错误**，因此按 SCOPE 第 7 条保持不动。
5. **未改 `package.json`**：本次删除没有让任何依赖变成死代码（`react-markdown`/`remark-gfm` 是上一轮更新链路摘除留下的死依赖，属于历史遗留，不在本任务范围，我也没碰）。