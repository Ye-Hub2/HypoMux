# T-Lead：便携版改由 GitHub Actions 构建 — 终验报告

日期：2026-10-03
负责人：Team Lead
关联：`reports/vnic/42-portable-assets.md`、`reports/vnic/43-portable-ci.md`、`reports/vnic/40-portable-package.md`、`reports/vnic/41-portable-runtime.md`

---

## 0. 结论

用户要求（m01072 / m01438）「使用 github 工作流构建便携版」已落地：**便携包现在由 `build.yml` 的 build job 组装并上传**，不是本地手工打包。

- 提交：`46680e0 ci: build the portable package in GitHub Actions`（父提交 `37571e5`）
- CI run：[37118538126](https://github.com/Ye-Hub2/HypoMux/actions/runs/37118538126)，event=`push`，headSha=`46680e08e9929157e7349380fed521a74195f509`，**success**，11:05:34Z → 11:11:26Z（约 5 分 52 秒）
- 本次 push **自动触发了工作流**（上一次 `37571e5` 因仓库首次注册竞态需要手动 dispatch，这次没有再出现）
- 产物：artifact `HypoMux-Portable-Windows-46680e08e9929157e7349380fed521a74195f509-1`，42,592,065 B；内含 zip 42,680,804 B + `SHA256SUMS.txt` 670 B；过期时间 2027-01-01
- 已下载回本机并逐项校验，全部通过（见 §3）

---

## 1. 契约豁免（重要治理记录）

`reports/vnic/00-frozen-interface.md:11` 与 `:171` 写过「不得修改 `.github/workflows/**`」「禁止改任何 workflow 文件」。**该约束被显式豁免。**

- 理由：`00-frozen-interface.md` 是 Lead 为「AI 移除 + 虚拟网卡」这一轮自订的分工契约，不是用户下达的约束；它早于用户 m01072/m01438 的要求。
- 用户显式要求由 GitHub 工作流产出便携包，而原 `build.yml` 没有任何便携包步骤，因此**必须**修改 workflow 才能满足需求。
- 处理：契约未改写（保持历史真实性），豁免以本条记录 + 提交说明留痕。若后续需要，可由 Lead 正式更新该契约文本。

---

## 2. 提交内容（6 文件 / +652 行）

| 文件 | 变更 |
|---|---|
| `.github/workflows/build.yml` | +166 行：`Assemble portable package`（`shell: pwsh`）+ `Upload portable package`（`actions/upload-artifact@v7`），插在 `Upload build artifacts` 之前；既有 28 步一行未动 |
| `.gitattributes` | +5：`*.cmd text eol=crlf`、`desktop/portable/*.txt text eol=crlf` |
| `.gitignore` | +3：`/portable-package/` |
| `desktop/portable/launch-portable.cmd` | 新增 11,102 B → 包内 `启动便携版.cmd` |
| `desktop/portable/restore-environment.cmd` | 新增 5,050 B → 包内 `恢复环境.cmd` |
| `desktop/portable/PORTABLE-README.txt` | 新增 5,626 B → 包内 `便携版说明.txt` |

工作流要点：

- 版本号从 `desktop\VERSION` 读（2.7.0），不写死。
- 五个二进制：`desktop\bin\hypomux.exe` → 包根；`desktop\bin\hypomux-engine.exe` + `bin\{sing-box.exe,wintun.dll,libcronet.dll}` → `bin\`。
- **`bin\` 不可拍平**：`desktop/internal/engineclient/service_windows.go:73-91` 的 `allowAutomaticCoreFallbackPath` 用 `os.SameFile` 强制回退引擎 == `<dir(hypomux.exe)>\bin\hypomux-engine.exe`。
- `SHA256SUMS.txt` 用 `[System.IO.File]::WriteAllText(..., [System.Text.UTF8Encoding]::new($false))` 写无 BOM，格式 `<hash>  <相对路径>`。
- zip 逐条 `CreateEntry` 正斜杠写入（**有意偏离 Lead 最初给的 `ZipFile::CreateFromDirectory` 规格**，ci-runner 举证正确并经 Lead 接受）：该 API 在 Windows 上把条目名写成 `bin\hypomux-engine.exe`，违反 ZIP 规范 APPNOTE 4.4.17，bsdtar / Info-ZIP 会把 `bin\` 塌平、直接破坏 launcher 依赖的目录结构。仍不使用 `Compress-Archive`（非 ASCII 条目名有编码问题）。
- 自检两项：无条目名含 `\`；条目数 == 9。

---

## 3. 回环校验证据（本机实跑）

### 3.1 zip 结构
```
entries=9   backslash entries: 0
     8482304  bin/hypomux-engine.exe
     9528832  bin/libcronet.dll
    81947136  bin/sing-box.exe
      427552  bin/wintun.dll
    15344640  hypomux.exe
         670  SHA256SUMS.txt
        5626  便携版说明.txt
        5050  恢复环境.cmd
       11102  启动便携版.cmd
```
中文条目名经 GitHub artifact 上传/下载往返后未损坏。

### 3.2 校验和
`SHA256SUMS.txt` 8 条与 `Get-FileHash` **全部一致**。三个仓库资产哈希与 `reports/vnic/42-portable-assets.md` 记录的 `adc7b5a1…` / `6751e6d2…` / `302b7f7f…` **逐字节相同**，证明 GitHub 未重写内层文件字节。

`sing-box.exe` = `7bbef1dea9189ee12799ae834ea4b4658355da25c47a21ad8804904c0ccd9410`，与 `bin/README.md` 记录的官方 1.14.2 归档一致。

### 3.3 启动器干跑
```
before: hypomux pids=6240  service=Running
exit=0
  [冲突] 检测到 hypomux.exe 正在运行。
         便携版与已安装版同名同 UniqueID（io.hypomux.desktop）…
  [3/6] [通过] 管理员权限检查通过。
  [4/6] 停止已安装的 HypoMuxCore 服务（必需前置条件）...
         [干跑] 已跳过实际停止操作。
  [5/6] … [干跑] 已跳过实际写入。
  [6/6] … HYPOMUX_DATA_DIR / HYPOMUX_ENGINE_PATH
  [干跑结束] 以上是真实运行时会执行的全部动作，本次未做任何修改。
after:  hypomux pids=6240 (delta=0)   service=Running   data dir exists: False
```
`恢复环境.cmd --check` 同样 exit 0。

结论：CI 产出的两个 `.cmd` 编码、路径、参数、提示全部正确，且干跑**零副作用**（未建 `data\`、服务未被停、未拉起任何 `hypomux.exe` 进程）。

---

## 4. 与本地手工包（`dist/portable/`）的差异

- 本地包 zip 41,593,864 B，CI 包 42,680,804 B。差值来自 `-ldflags -X main.commit=46680e08e…` 打进二进制的提交串长度差异，以及 runner 上重新构建的 `hypomux.exe`（15,344,640 B vs 本地 14,999,552 B）。
- `dist/portable/` 那份（`reports/vnic/40-portable-package.md`）已被 CI 版本取代，本机新的权威副本在：
  - `%USERPROFILE%\Desktop\HypoMux-Portable-2.7.0-vnic-preview\`（已解压，9 文件）
  - `%USERPROFILE%\Desktop\HypoMux-Portable-2.7.0-vnic-preview.zip`（SHA256 `18b89839eb2a5fd710bcd3d295585cf35a9d31f8e57d7e3c1b2cbe636f5062f4`）

---

## 5. 未验证 / 遗留

1. **便携包从未真机启动过。** 只做过 `--check` 干跑。真机启动需要先退出已安装版 2.7.0、停 `HypoMuxCore` 服务，并会与官方 TUN 抢同名适配器 `HypoMux-Tun`（`engine/internal/tun/supervisor.go:36-41`、`desktop/internal/services/tun_config.go:231`）。用户未授权前不做。
2. `signing_mode=production|publish` 路径未跑。本次 push 的 run 是 `signing_mode=none`，便携包装的是**未签名**二进制。
3. 虚拟网卡的 stale 清理缺陷仍在（`reports/vnic/22-engine-edge.md` F1 / `reports/vnic/30-real-machine-verdict.md` F-D）：外部 sing-box 占用同名适配器时，引擎重试只调 `s.vnic.Remove`（`engine/internal/server/server.go:866-878`），而 `engine/internal/vnic/manager.go:196-208` 的 `Remove` 只停自己跟踪的 keeper，不做 NIC 级删除。
4. 便携包会修改 `HKCU\...\Run\HypoMux`（用户在便携版 UI 动「开机自启」时覆盖安装版的同名键），且 `desktop/frontend/src/platform/services.ts:75` 附近已不再有 AI 字段。
5. 便携包暂未挂进 GitHub Release（`release` job 未改），只能从 Actions artifact 下载，过期时间 2027-01-01。

---

## 6. 使用方法（用户视角）

1. 从上面两个位置之一取包（zip 或已解压目录）。
2. 先从托盘**完全退出已安装版 HypoMux**（否则单实例交接会让便携版静默退出）。
3. 右键 `启动便携版.cmd` →「以管理员身份运行」。它会停 `HypoMuxCore` 服务、准备 `data\settings.json`（首次为 `mode=proxy`、不接管系统代理）、再启动 `hypomux.exe`。
4. 用完后运行 `恢复环境.cmd`（管理员）把 `HypoMuxCore` 服务拉回来。
5. 首页「创建虚拟网卡」需要管理员 + 已停服务，缺任一条会得到「当前聚合核心不支持虚拟网卡，请更新 HypoMux 核心后重试」。