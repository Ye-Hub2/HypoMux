# 43 便携包组装进入 GitHub 工作流（task-22 / ci-runner）

- 任务：在 `.github/workflows/build.yml` 的 `build` job 内、SignPath 签名步骤之后、`Upload build artifacts` 之前，新增「组装便携包」与「上传便携包」两个步骤，让便携版由 GitHub 工作流产出，并本地跑通。
- 写入范围：`.github/workflows/build.yml`、`.gitattributes`、`.gitignore`、本报告。未改动 `desktop/**`、`engine/**`、`desktop/portable/**` 的任何内容。
- 状态：**本地全部实测通过**。未 `git commit`、未 `git push`（按 task-22 要求留给 Lead）；该工作流尚未在 GitHub 上执行过这两个新步骤。
- 本地时间：2026-10-03 19:00（Windows PowerShell 5.1 实测会话）。

## 0. 结论摘要

| 检查项 | 结果 |
| --- | --- |
| 工作流 YAML 能否被真正的 YAML 解析器解析 | 通过（`gopkg.in/yaml.v3 v3.0.1`，31 个 step，顺序正确） |
| build.yml 内 `run:` 正文与本地实测脚本是否逐字一致 | 通过（两侧各 7014 字节 / 6958 字符 / 153 行完全相等） |
| 组装脚本本地实跑 | 通过（exit 0） |
| 产物结构（8 个载荷 + SHA256SUMS.txt） | 通过（stage 115,407,824 B；zip 41,593,902 B；9 个条目） |
| zip 条目名是否全部用正斜杠（ZIP 规范 APPNOTE 4.4.17） | 通过（9/9） |
| SHA256SUMS.txt 是否无 BOM、LF、可被 Get-FileHash 复算 | 通过（670 B、无 BOM、CRLF=0、8 行全部 MATCH） |
| 两种解压方式是否都还原出正确的中文文件名与目录结构 | 通过（`Expand-Archive`、`ZipFile.ExtractToDirectory` 各 9/9） |
| `启动便携版.cmd --check` / `恢复环境.cmd --check` | 通过（exit 0；UTF-8 输出；0 个 U+FFFD；266 / 198 个汉字） |
| 干跑是否真的启动了 `hypomux.exe` | 没有（见 §7 的进程时间戳证据） |
| 源文件缺失时是否 fail-fast | 通过（exit 1，且未创建任何 staging 目录） |
| 本地测试是否污染仓库 | 没有（`HypoMux\portable-package` 不存在） |
| Go 测试 `go -C desktop test -count=1 ./...` | 见 §10 |

## 1. 变更清单

`git status --porcelain`（未提交）：

```
 M .gitattributes
 M .github/workflows/build.yml
 M .gitignore
?? desktop/portable/     <- 本任务没有碰，是 local-pipeline-verifier 的 task-21
?? reports/
```

`git diff --stat`：

```
 .gitattributes              |   5 ++
 .github/workflows/build.yml | 166 ++++++++++++++++++++++++++++++++++++++++++++
 .gitignore                  |   3 +
 3 files changed, 174 insertions(+)
```

- `.gitattributes`：新增 `*.cmd text eol=crlf` 与 `desktop/portable/*.txt text eol=crlf`（含两行注释说明为什么需要）。
- `.gitignore`：新增 `/portable-package/`（本地试跑用的暂存目录，避免弄脏工作树）。
- `.github/workflows/build.yml`：只在 `Upload build artifacts` 前插入两个步骤，没有改动任何既有步骤。
- 行尾噪声：`git diff --numstat` = `5 0 .gitattributes`、`3 0 .gitignore`，纯新增，没有整文件重写。

## 2. 两个新步骤的完整 YAML

下面这段是 `git diff` 的原文（唯一权威来源，未手工改写）：

```diff
diff --git a/.github/workflows/build.yml b/.github/workflows/build.yml
index cdcbdd4..9409bff 100644
--- a/.github/workflows/build.yml
+++ b/.github/workflows/build.yml
@@ -301,4 +301,170 @@ jobs:
           go -C desktop test ./internal/services -run TestVerifyDownloadedInstallerAuthenticityAcceptsOfficialSignedInstaller -count=1 -v
 
+      - name: Assemble portable package
+        shell: pwsh
+        run: |
+          $ErrorActionPreference = 'Stop'
+          Add-Type -AssemblyName System.IO.Compression
+          Add-Type -AssemblyName System.IO.Compression.FileSystem
+
+          # Resolve every path against the repository root captured at start-up: .NET APIs
+          # such as [System.IO.File] use the process working directory, which is not
+          # guaranteed to follow PowerShell's current location.
+          $repoRoot = (Get-Location).Path
+          Write-Host "Repository root: $repoRoot"
+
+          $versionFile = Join-Path $repoRoot 'desktop\VERSION'
+          if (-not (Test-Path -LiteralPath $versionFile)) {
+            throw "desktop\VERSION not found under $repoRoot; run this step from the repository root."
+          }
+          $version = [System.IO.File]::ReadAllText($versionFile).Trim()
+          if ([string]::IsNullOrWhiteSpace($version)) {
+            throw 'desktop\VERSION is empty; cannot derive the portable package version.'
+          }
+
+          # Sources: build output (desktop\bin) plus the tracked runtime binaries (bin\) and
+          # the tracked launcher assets (desktop\portable). Fail fast with an exact path if
+          # anything the package needs is missing.
+          $binarySources = @(
+            (Join-Path $repoRoot 'desktop\bin\hypomux.exe'),
+            (Join-Path $repoRoot 'desktop\bin\hypomux-engine.exe'),
+            (Join-Path $repoRoot 'bin\sing-box.exe'),
+            (Join-Path $repoRoot 'bin\wintun.dll'),
+            (Join-Path $repoRoot 'bin\libcronet.dll')
+          )
+          $assets = @(
+            @{ Source = (Join-Path $repoRoot 'desktop\portable\launch-portable.cmd');     Name = '启动便携版.cmd' },
+            @{ Source = (Join-Path $repoRoot 'desktop\portable\restore-environment.cmd'); Name = '恢复环境.cmd' },
+            @{ Source = (Join-Path $repoRoot 'desktop\portable\PORTABLE-README.txt');     Name = '便携版说明.txt' }
+          )
+          foreach ($source in @($binarySources) + @($assets | ForEach-Object { $_.Source })) {
+            if (-not (Test-Path -LiteralPath $source)) {
+              throw "Portable package source is missing: $source"
+            }
+          }
+
+          $stageName = "HypoMux-Portable-$version-vnic-preview"
+          $outDir = Join-Path $repoRoot 'portable-package'
+          $stage = Join-Path $outDir $stageName
+          $binDir = Join-Path $stage 'bin'
+          $zipPath = Join-Path $outDir "$stageName.zip"
+
+          if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
+          if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
+          New-Item -ItemType Directory -Path $binDir -Force | Out-Null
+
+          # The layout below is fixed by the application code: keep the engine and its
+          # sidecar DLLs inside bin\. desktop\internal\engineclient\service_windows.go:73-91
+          # (allowAutomaticCoreFallbackPath) uses os.SameFile to require the fallback engine
+          # to be exactly <dir(hypomux.exe)>\bin\hypomux-engine.exe, so flattening bin\ makes
+          # the elevated Core fallback fail whenever the HypoMuxCore service is stopped.
+          # sing-box.exe/wintun.dll/libcronet.dll must stay next to hypomux-engine.exe
+          # because the engine resolves sing-box from its own directory and sing-box loads
+          # wintun.dll from there.
+          Copy-Item -LiteralPath $binarySources[0] -Destination (Join-Path $stage 'hypomux.exe') -Force
+          foreach ($binary in @($binarySources[1..4])) {
+            Copy-Item -LiteralPath $binary -Destination $binDir -Force
+          }
+
+          # The user-facing launch assets are stored with ASCII names in the repository and
+          # only get their Chinese package names here.
+          foreach ($asset in $assets) {
+            Copy-Item -LiteralPath $asset.Source -Destination (Join-Path $stage $asset.Name) -Force
+          }
+
+          $required = @(
+            'hypomux.exe',
+            'bin\hypomux-engine.exe',
+            'bin\sing-box.exe',
+            'bin\wintun.dll',
+            'bin\libcronet.dll',
+            '启动便携版.cmd',
+            '恢复环境.cmd',
+            '便携版说明.txt'
+          )
+          $missing = @($required | Where-Object { -not (Test-Path -LiteralPath (Join-Path $stage $_)) })
+          if ($missing.Count -gt 0) {
+            throw "Portable package is incomplete, missing: $($missing -join ', ')."
+          }
+
+          # SHA256SUMS.txt covers every staged file. It is written as UTF-8 without BOM so
+          # the checksums stay parsable by both Windows and Linux tooling.
+          $stageRoot = (Resolve-Path -LiteralPath $stage).Path
+          $sumsPath = Join-Path $stage 'SHA256SUMS.txt'
+          $sumLines = Get-ChildItem -LiteralPath $stage -Recurse -File |
+            Where-Object { $_.FullName -ne $sumsPath } |
+            Sort-Object FullName |
+            ForEach-Object {
+              $relative = $_.FullName.Substring($stageRoot.Length).TrimStart('\') -replace '\\', '/'
+              $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
+              "$hash  $relative"
+            }
+          [System.IO.File]::WriteAllText($sumsPath, (($sumLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
+
+          # Zip with System.IO.Compression, never Compress-Archive: Compress-Archive has a
+          # history of mangling non-ASCII entry names, while .NET writes non-ASCII names as
+          # UTF-8 and sets the language-encoding flag. Entries are added one by one instead
+          # of with ZipFile::CreateFromDirectory because that helper stores the Windows path
+          # separator, and a backslash in an entry name violates the ZIP spec (APPNOTE 4.4.17
+          # requires forward slashes): tools that treat "\" as a literal file name character
+          # (bsdtar, Info-ZIP) would flatten bin\ into the package root and break the
+          # launcher. The package layout is part of the contract, so the separators are
+          # normalized here and verified below.
+          $archive = [System.IO.Compression.ZipFile]::Open($zipPath, [System.IO.Compression.ZipArchiveMode]::Create)
+          try {
+            foreach ($file in Get-ChildItem -LiteralPath $stage -Recurse -File | Sort-Object FullName) {
+              $entryName = $file.FullName.Substring($stageRoot.Length).TrimStart('\') -replace '\\', '/'
+              $entry = $archive.CreateEntry($entryName, [System.IO.Compression.CompressionLevel]::Optimal)
+              $entryStream = $entry.Open()
+              try {
+                $fileStream = [System.IO.File]::OpenRead($file.FullName)
+                try { $fileStream.CopyTo($entryStream) } finally { $fileStream.Dispose() }
+              } finally {
+                $entryStream.Dispose()
+              }
+            }
+          } finally {
+            $archive.Dispose()
+          }
+
+          $zipCheck = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
+          try {
+            $badEntries = @($zipCheck.Entries | Where-Object { $_.FullName -like '*\*' })
+            if ($badEntries.Count -gt 0) {
+              throw "Zip entry names must use forward slashes, found: $($badEntries[0].FullName)"
+            }
+            if ($zipCheck.Entries.Count -ne $required.Count + 1) {
+              throw "Unexpected zip entry count: $($zipCheck.Entries.Count) (expected $($required.Count + 1))."
+            }
+          } finally {
+            $zipCheck.Dispose()
+          }
+
+          $stageBytes = (Get-ChildItem -LiteralPath $stage -Recurse -File | Measure-Object -Property Length -Sum).Sum
+          Write-Host "Portable package stage: $stageRoot ($stageBytes bytes)"
+          Write-Host "Portable package zip  : $zipPath ($((Get-Item -LiteralPath $zipPath).Length) bytes)"
+          Write-Host 'Staged files:'
+          Get-ChildItem -LiteralPath $stage -Recurse -File | Sort-Object FullName | ForEach-Object {
+            Write-Host ("  {0,12}  {1}" -f $_.Length, $_.FullName.Substring($stageRoot.Length).TrimStart('\'))
+          }
+          Write-Host 'Zip entries:'
+          $zip = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
+          try {
+            $zip.Entries | Sort-Object FullName | ForEach-Object {
+              Write-Host ("  {0,12}  {1}" -f $_.Length, $_.FullName)
+            }
+          } finally {
+            $zip.Dispose()
+          }
+
+      - name: Upload portable package
+        uses: actions/upload-artifact@v7
+        with:
+          name: HypoMux-Portable-Windows-${{ github.sha }}-${{ github.run_attempt }}
+          path: |
+            portable-package/*.zip
+            portable-package/*/SHA256SUMS.txt
+          if-no-files-found: error
+
       - name: Upload build artifacts
         uses: actions/upload-artifact@v7
```

步骤位置：`Assemble portable package` 是 build job 的第 29 步，`Upload portable package` 第 30 步，`Upload build artifacts` 第 31 步。

在 `signing_mode=production/publish` 的手动触发里，第 23 步 `Repackage signed executables`（build.yml:249-254）会把签名后的 `hypomux.exe` / `hypomux-engine.exe` 覆盖回 `desktop\bin\`，而组装步骤在它之后运行，所以**签名模式下便携包里装的是已签名的可执行文件**（未签名模式则是本地产物）。

## 3. 组装脚本全文

脚本直接写在 `run: |` 里（不额外新增脚本文件，避免多一个需要同步的产物）：

```powershell
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

# Resolve every path against the repository root captured at start-up: .NET APIs
# such as [System.IO.File] use the process working directory, which is not
# guaranteed to follow PowerShell's current location.
$repoRoot = (Get-Location).Path
Write-Host "Repository root: $repoRoot"

$versionFile = Join-Path $repoRoot 'desktop\VERSION'
if (-not (Test-Path -LiteralPath $versionFile)) {
  throw "desktop\VERSION not found under $repoRoot; run this step from the repository root."
}
$version = [System.IO.File]::ReadAllText($versionFile).Trim()
if ([string]::IsNullOrWhiteSpace($version)) {
  throw 'desktop\VERSION is empty; cannot derive the portable package version.'
}

# Sources: build output (desktop\bin) plus the tracked runtime binaries (bin\) and
# the tracked launcher assets (desktop\portable). Fail fast with an exact path if
# anything the package needs is missing.
$binarySources = @(
  (Join-Path $repoRoot 'desktop\bin\hypomux.exe'),
  (Join-Path $repoRoot 'desktop\bin\hypomux-engine.exe'),
  (Join-Path $repoRoot 'bin\sing-box.exe'),
  (Join-Path $repoRoot 'bin\wintun.dll'),
  (Join-Path $repoRoot 'bin\libcronet.dll')
)
$assets = @(
  @{ Source = (Join-Path $repoRoot 'desktop\portable\launch-portable.cmd');     Name = '启动便携版.cmd' },
  @{ Source = (Join-Path $repoRoot 'desktop\portable\restore-environment.cmd'); Name = '恢复环境.cmd' },
  @{ Source = (Join-Path $repoRoot 'desktop\portable\PORTABLE-README.txt');     Name = '便携版说明.txt' }
)
foreach ($source in @($binarySources) + @($assets | ForEach-Object { $_.Source })) {
  if (-not (Test-Path -LiteralPath $source)) {
    throw "Portable package source is missing: $source"
  }
}

$stageName = "HypoMux-Portable-$version-vnic-preview"
$outDir = Join-Path $repoRoot 'portable-package'
$stage = Join-Path $outDir $stageName
$binDir = Join-Path $stage 'bin'
$zipPath = Join-Path $outDir "$stageName.zip"

if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
New-Item -ItemType Directory -Path $binDir -Force | Out-Null

# The layout below is fixed by the application code: keep the engine and its
# sidecar DLLs inside bin\. desktop\internal\engineclient\service_windows.go:73-91
# (allowAutomaticCoreFallbackPath) uses os.SameFile to require the fallback engine
# to be exactly <dir(hypomux.exe)>\bin\hypomux-engine.exe, so flattening bin\ makes
# the elevated Core fallback fail whenever the HypoMuxCore service is stopped.
# sing-box.exe/wintun.dll/libcronet.dll must stay next to hypomux-engine.exe
# because the engine resolves sing-box from its own directory and sing-box loads
# wintun.dll from there.
Copy-Item -LiteralPath $binarySources[0] -Destination (Join-Path $stage 'hypomux.exe') -Force
foreach ($binary in @($binarySources[1..4])) {
  Copy-Item -LiteralPath $binary -Destination $binDir -Force
}

# The user-facing launch assets are stored with ASCII names in the repository and
# only get their Chinese package names here.
foreach ($asset in $assets) {
  Copy-Item -LiteralPath $asset.Source -Destination (Join-Path $stage $asset.Name) -Force
}

$required = @(
  'hypomux.exe',
  'bin\hypomux-engine.exe',
  'bin\sing-box.exe',
  'bin\wintun.dll',
  'bin\libcronet.dll',
  '启动便携版.cmd',
  '恢复环境.cmd',
  '便携版说明.txt'
)
$missing = @($required | Where-Object { -not (Test-Path -LiteralPath (Join-Path $stage $_)) })
if ($missing.Count -gt 0) {
  throw "Portable package is incomplete, missing: $($missing -join ', ')."
}

# SHA256SUMS.txt covers every staged file. It is written as UTF-8 without BOM so
# the checksums stay parsable by both Windows and Linux tooling.
$stageRoot = (Resolve-Path -LiteralPath $stage).Path
$sumsPath = Join-Path $stage 'SHA256SUMS.txt'
$sumLines = Get-ChildItem -LiteralPath $stage -Recurse -File |
  Where-Object { $_.FullName -ne $sumsPath } |
  Sort-Object FullName |
  ForEach-Object {
    $relative = $_.FullName.Substring($stageRoot.Length).TrimStart('\') -replace '\\', '/'
    $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $relative"
  }
[System.IO.File]::WriteAllText($sumsPath, (($sumLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))

# Zip with System.IO.Compression, never Compress-Archive: Compress-Archive has a
# history of mangling non-ASCII entry names, while .NET writes non-ASCII names as
# UTF-8 and sets the language-encoding flag. Entries are added one by one instead
# of with ZipFile::CreateFromDirectory because that helper stores the Windows path
# separator, and a backslash in an entry name violates the ZIP spec (APPNOTE 4.4.17
# requires forward slashes): tools that treat "\" as a literal file name character
# (bsdtar, Info-ZIP) would flatten bin\ into the package root and break the
# launcher. The package layout is part of the contract, so the separators are
# normalized here and verified below.
$archive = [System.IO.Compression.ZipFile]::Open($zipPath, [System.IO.Compression.ZipArchiveMode]::Create)
try {
  foreach ($file in Get-ChildItem -LiteralPath $stage -Recurse -File | Sort-Object FullName) {
    $entryName = $file.FullName.Substring($stageRoot.Length).TrimStart('\') -replace '\\', '/'
    $entry = $archive.CreateEntry($entryName, [System.IO.Compression.CompressionLevel]::Optimal)
    $entryStream = $entry.Open()
    try {
      $fileStream = [System.IO.File]::OpenRead($file.FullName)
      try { $fileStream.CopyTo($entryStream) } finally { $fileStream.Dispose() }
    } finally {
      $entryStream.Dispose()
    }
  }
} finally {
  $archive.Dispose()
}

$zipCheck = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
try {
  $badEntries = @($zipCheck.Entries | Where-Object { $_.FullName -like '*\*' })
  if ($badEntries.Count -gt 0) {
    throw "Zip entry names must use forward slashes, found: $($badEntries[0].FullName)"
  }
  if ($zipCheck.Entries.Count -ne $required.Count + 1) {
    throw "Unexpected zip entry count: $($zipCheck.Entries.Count) (expected $($required.Count + 1))."
  }
} finally {
  $zipCheck.Dispose()
}

$stageBytes = (Get-ChildItem -LiteralPath $stage -Recurse -File | Measure-Object -Property Length -Sum).Sum
Write-Host "Portable package stage: $stageRoot ($stageBytes bytes)"
Write-Host "Portable package zip  : $zipPath ($((Get-Item -LiteralPath $zipPath).Length) bytes)"
Write-Host 'Staged files:'
Get-ChildItem -LiteralPath $stage -Recurse -File | Sort-Object FullName | ForEach-Object {
  Write-Host ("  {0,12}  {1}" -f $_.Length, $_.FullName.Substring($stageRoot.Length).TrimStart('\'))
}
Write-Host 'Zip entries:'
$zip = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
try {
  $zip.Entries | Sort-Object FullName | ForEach-Object {
    Write-Host ("  {0,12}  {1}" -f $_.Length, $_.FullName)
  }
} finally {
  $zip.Dispose()
}
```

**一致性证明**：把 build.yml 交给真正的 YAML 解析器（`gopkg.in/yaml.v3`）解析，取出该步骤的 `run` 字符串，与本地实测使用的 `assemble-portable.ps1` 比较：两侧各 `7014 字节 / 6958 字符 / 153 行`，完全相等（见 §9）。

## 4. 本地实跑方式

工作流里的脚本无法直接在本机执行（本机没有 GitHub runner），所以采用「**镜像仓库 + 逐字脚本**」的方式实跑：

1. 在 `%TEMP%\hypomux-portable-test\repo\` 用**硬链接**（`New-Item -ItemType HardLink`）镜像出 `desktop\VERSION`、`desktop\bin\*`、`bin\*`、`desktop\portable\*`，不复制 115 MB 数据、也不写仓库。
2. 待测脚本正文单独保存为 `assemble-portable.ps1`（UTF-8 无 BOM），与 build.yml 的 `run:` 正文逐字一致（§3 的证明）。
3. 驱动脚本 `run-local-test.ps1`（纯 ASCII，避免编码干扰）分 9 步执行全部检查，日志落在 `local-test-log.txt`。

实跑命令与退出码：

| # | 命令 | exit code |
| --- | --- | --- |
| 1 | 构建临时镜像仓库 | 0 |
| 2 | `& assemble-portable.utf8bom.ps1`（工作目录 = 镜像仓库） | **0** |
| 3 | 用 `ZipFile.OpenRead` 列条目 | 0 |
| 4 | 逐个 staged 文件与源文件比对 SHA256 | 0 |
| 5 | 复算 `SHA256SUMS.txt` 的 8 条 | 0 |
| 6 | `Expand-Archive` + `ZipFile.ExtractToDirectory` 两路解压比对 | 0 |
| 7 | `cmd /c "启动便携版.cmd" --check` | **0** |
| 7 | `cmd /c "恢复环境.cmd" --check` | **0** |
| 8 | 缺资产 fail-fast 用例 | 组装脚本 exit **1**（预期） |
| 9 | 真实仓库污染检查 | 0 |

驱动脚本本身不参与 CI，只是证据生成器，因此没有放进仓库。

## 5. 产物结构与 zip 条目

```
portable-package/
├── HypoMux-Portable-2.7.0-vnic-preview.zip     41,593,902 B
└── HypoMux-Portable-2.7.0-vnic-preview/
    ├── hypomux.exe                             14,999,552 B
    ├── bin/
    │   ├── hypomux-engine.exe                   8,482,304 B
    │   ├── sing-box.exe                        81,947,136 B
    │   ├── wintun.dll                             427,552 B
    │   └── libcronet.dll                        9,528,832 B
    ├── 启动便携版.cmd                              11,102 B
    ├── 恢复环境.cmd                                 5,050 B
    ├── 便携版说明.txt                               5,626 B
    └── SHA256SUMS.txt                               670 B
（stage 合计 115,407,824 B）
```

zip 条目清单（`zip entry count: 9`，全部正斜杠）：

```
   8482304  bin/hypomux-engine.exe
   9528832  bin/libcronet.dll
  81947136  bin/sing-box.exe
    427552  bin/wintun.dll
  14999552  hypomux.exe
       670  SHA256SUMS.txt
      5626  便携版说明.txt
      5050  恢复环境.cmd
     11102  启动便携版.cmd
```

`bin\` 没有被拍平：`desktop\internal\engineclient\service_windows.go:73-91` 的 `allowAutomaticCoreFallbackPath` 用 `os.SameFile` 要求回退引擎恰好是 `<dir(hypomux.exe)>\bin\hypomux-engine.exe`，拍平会让 HypoMuxCore 服务停止时的提权回退失效。

## 6. SHA256 证据

| 包内路径 | SHA256 | 字节 |
| --- | --- | --- |
| `hypomux.exe` | `cfe0280080b886ccdc576755f3268cca41ce6913625a12466bdf947fa129e1be` | 14,999,552 |
| `bin/hypomux-engine.exe` | `d5d82ec3d7215823c293d2915cd4529db8390af43ced5e7ec4939e6d0731e2e0` | 8,482,304 |
| `bin/sing-box.exe` | `7bbef1dea9189ee12799ae834ea4b4658355da25c47a21ad8804904c0ccd9410` | 81,947,136 |
| `bin/wintun.dll` | `e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce` | 427,552 |
| `bin/libcronet.dll` | `257f966119ffca91d7a2ce110a4b668b865d88bf2ed5339fd06a5644b0d02823` | 9,528,832 |
| `启动便携版.cmd` | `adc7b5a1dca2f248993cb0b2ede88ec749d473a4e169ffebcd721c618ce68a3f` | 11,102 |
| `恢复环境.cmd` | `6751e6d22a2fd8f9df11a64a342b6b7eb1cc7bf8d853ed2df1ec882828a036ec` | 5,050 |
| `便携版说明.txt` | `302b7f7f824844b1592173c84da203e841adc662f6ec8ce25ae147ce7b878d65` | 5,626 |

- 这 8 个 staged 文件都与它们的源文件逐字节一致（驱动脚本按哈希反查来源并打印）。
- `SHA256SUMS.txt`：670 B、**无 BOM**、CRLF=0、8 行 LF 结尾；用 `Get-FileHash` 逐条复算，8/8 `MATCH`。

## 7. `--check` 干跑证据

| 脚本 | exit code | stdout 字节 | 编码 | 汉字数 | U+FFFD | stderr |
| --- | --- | --- | --- | --- | --- | --- |
| `启动便携版.cmd --check` | 0 | 2,486 | UTF-8 | 266 | 0 | 0 B |
| `恢复环境.cmd --check` | 0 | 1,627 | UTF-8 | 198 | 0 | 0 B |

输出片段（解压后的真实包里执行）：

```
============================================================
HypoMux 便携预览版（免安装）
============================================================
程序目录  : C:\...\Temp\hypomux-portable-test\extracted-expandarchive
```

**没有真的启动新进程**：干跑结束后 `Get-CimInstance Win32_Process -Filter "Name='hypomux.exe'"` 只有 1 个进程，它是 17:41:05 启动的已安装版本 `C:\Program Files\HypoMux\hypomux.exe`，而两次干跑的 stdout 文件写于 19:00:09 / 19:00:10 —— 即这个进程早于干跑 1 小时 19 分钟，与干跑无关。这与静态审计一致：`desktop/portable/launch-portable.cmd:125-135` 在 CHECK 分支打印后 `exit /b 0`，真正的 `start "" /d "%ROOT%" "%ROOT%\hypomux.exe"` 在 `:136`，CHECK 分支到不了；所有写动作都有 `if defined CHECK` 守卫（:89、:106、:125、:179、:205、:221）；`restore-environment.cmd` 同样在 `pause` 之前 `exit /b 0`。

## 8. git 属性与行尾证据

真实仓库 `git check-attr text eol -- <路径>`：

```
desktop/portable/launch-portable.cmd: text: set
desktop/portable/launch-portable.cmd: eol: crlf
desktop/portable/restore-environment.cmd: text: set
desktop/portable/restore-environment.cmd: eol: crlf
desktop/portable/PORTABLE-README.txt: text: set
desktop/portable/PORTABLE-README.txt: eol: crlf
.github/workflows/build.yml: text: set
.github/workflows/build.yml: eol: lf
.gitattributes: text: auto
.gitattributes: eol: unspecified
```

工作树字节事实：

```
desktop/portable/launch-portable.cmd      bytes=11102 CRLF=257 bare_LF=0 BOM=False
desktop/portable/restore-environment.cmd  bytes=5050  CRLF=136 bare_LF=0 BOM=False
desktop/portable/PORTABLE-README.txt      bytes=5626  CRLF=85  bare_LF=0 BOM=True
```

一次性仓库（`%TEMP%\hypomux-eol-proof`，复制 `.gitattributes` 与三个资产后 `git init` + `git add` + `git commit`，未碰真实仓库）里的 `git ls-files --eol`：

```
i/lf    w/crlf  attr/text=auto        	.gitattributes
i/lf    w/lf    attr/text eol=lf      	.github/workflows/build.yml
i/lf    w/crlf  attr/text eol=crlf    	desktop/portable/PORTABLE-README.txt
i/lf    w/crlf  attr/text eol=crlf    	desktop/portable/launch-portable.cmd
i/lf    w/crlf  attr/text eol=crlf    	desktop/portable/restore-environment.cmd
```

提交后 `git status --porcelain` 为空 —— 也就是说 `.cmd` / `.txt` 的 CRLF 不会在每次 `git add` 时被反复改写（这正是加 `eol=crlf` 的目的：`.cmd` 对行尾敏感，而 Windows runner 不保证 `core.autocrlf`）。

## 9. YAML 校验

本机没有可用的 `python`（Microsoft Store 0 字节 stub）、`desktop/frontend/node_modules` 里也没有 `yaml`/`js-yaml`，所以改用 **Go 侧真正的 YAML 实现**：`gopkg.in/yaml.v3@v3.0.1`（本地模块缓存里已有），临时程序位于 `%TEMP%\yamlcheck\`，不写入仓库。

```
$ go run . <build.yml> <assemble-portable.ps1>
OK: build.yml parses as YAML (gopkg.in/yaml.v3 v3.0.1)
jobs: build, release
build job steps: 31
  ...（第 1-28 步为既有步骤，此处省略）
  [29] Assemble portable package
  [30] Upload portable package
  [31] Upload build artifacts
OK: step order assemble(29) < upload-portable(30) < upload-build-artifacts(31)
embedded run text: 7014 bytes / 6958 chars / 153 lines
tested script    : 7014 bytes / 6958 chars / 153 lines
OK: the run block parsed out of the YAML is byte-identical to the locally tested script
upload name: HypoMux-Portable-Windows-${{ github.sha }}-${{ github.run_attempt }}
upload path: "portable-package/*.zip\nportable-package/*/SHA256SUMS.txt\n"
if-no-files-found: error
OK: portable package upload step is wired up
```

## 10. Go 测试

```
$ env:GOROOT="$env:TEMP\go-full\go"; $env:PATH="$env:TEMP\go-full\go\bin;$env:PATH"
$ go -C desktop test -count=1 ./...
ok  	github.com/Hypostasis-Cat/HypoMux/desktop	0.299s
?   	github.com/Hypostasis-Cat/HypoMux/desktop/build/windows/syso	[no test files]
?   	github.com/Hypostasis-Cat/HypoMux/desktop/cmd/release-version	[no test files]
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/cmd/update-manifest-sign	0.388s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/engineclient	3.706s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform	0.360s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/platform/wails	0.678s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/releaseversion	0.468s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/services	50.284s
ok  	github.com/Hypostasis-Cat/HypoMux/desktop/internal/startup	0.389s
go test exit code = 0
```

`desktop` 包（含 `desktop/installer_layout_test.go`、`desktop/installer_directory_windows_test.go`）以及 `desktop/internal/services`、`desktop/internal/engineclient` 全绿；本次改动没有触及任何 Go 源码，这一步是回归确认。

## 11. 实现过程中踩到的四个坑（都已在脚本里修掉）

1. **PowerShell 的 location 与 .NET 的进程工作目录不是一回事。** `Push-Location` 之后 `[System.IO.File]::WriteAllText` 仍然按“真实”工作目录解析相对路径，第一次实跑的报错原文是：
   `使用“3”个参数调用“WriteAllText”时发生异常:“未能找到路径“<repo>\portable-package\HypoMux-Portable-2.7.0-vnic-preview\SHA256SUMS.txt”的一部分。”`
   修复：脚本开头取 `$repoRoot = (Get-Location).Path`，之后所有路径都用 `Join-Path $repoRoot ...` 变成绝对路径（并统一用 `-LiteralPath`）。
2. **`ZipFile.CreateFromDirectory` 在 Windows 上写出的条目名带反斜杠**（`bin\hypomux-engine.exe`）。ZIP 规范（APPNOTE 4.4.17）要求正斜杠；把 `\` 当普通字符的工具（bsdtar、Info-ZIP）解压后会得到根目录下一个名叫 `bin\hypomux-engine.exe` 的文件，`bin\` 结构消失，便携包直接坏掉。修复：改用 `ZipArchive.Open(..., Create)` + `CreateEntry(名字用正斜杠)` 逐条写入，并在脚本末尾自检“没有条目名含 `\`”“条目数 = 8+1”，不满足就 `throw`。
3. **本机 `pwsh` 并不存在**：harness 的 `pwsh` 工具实际执行的是 **Windows PowerShell 5.1.19041.5129**，系统 ANSI 代码页 ACP=936。5.1 会把**无 BOM** 的 `.ps1` 按 GBK 解码，因此本地测试副本必须转成 UTF-8 **带 BOM** 再执行；CI 用 `shell: pwsh`（PowerShell 7）读无 BOM 的 UTF-8 正文，本身没有问题。脚本正文不含任何 5.1/7 专有语法，两版都实测通过。
4. **控制台重定向的输出编码**：`cmd` 把 stdout 重定向到文件时按控制台代码页写字节。驱动脚本因此先尝试严格 UTF-8 解码，失败再按 CP936 解码，只要求“能干净解码 + 有汉字 + 无 U+FFFD”。本次两路都是 UTF-8（266 / 198 个汉字，0 个替换字符）。

## 12. 未验证项（诚实清单）

1. **真实 CI 首跑**：这两个新步骤还没有在 GitHub Actions 上运行过。runner 上 `shell: pwsh` 是 PowerShell 7，与本地 5.1 不同（脚本正文无差异，但只能在首跑时最终确认）；如需触发，用 `gh workflow run build.yml -f signing_mode=none`。
2. **签名模式**：`signing_mode=production/publish` 时，便携包应包含已签名二进制（依据是 build.yml:249-254 的覆盖顺序），但本次没有跑过签名路径。
3. **artifact 下载后的校验**：GitHub 会把上传的 artifact 再包一层 zip，本步骤上传的 `HypoMux-Portable-2.7.0-vnic-preview.zip` 与 `SHA256SUMS.txt` 在里层。里层 zip 的字节应原样保留、条目名不会被 GitHub 重新编码，但只有真正下载一次才能 100% 确认。
4. **便携包在真实用户机器上的运行**：本地只做了 `--check` 干跑，没有启动 `hypomux.exe`（task-22 明确禁止）。
5. **体积/耗时影响**：这个步骤对所有触发（push / PR / 手动）都生效，每次构建会多产生约 41.6 MB 的 artifact。如果需要限制，可以给该步骤加上与签名步骤相同的 `if:` 条件，或只保留上传步骤的 `if:`。
6. **构建时间**：本地在 SSD 上约数秒（复制 115 MB + 压缩到 41.6 MB）；runner 上的实际耗时未测。

## 13. 回滚方式

删除 `build.yml` 中 `- name: Assemble portable package` 到 `- name: Upload portable package` 的 `with:` 块末尾（即 `Upload build artifacts` 之前的那 166 行），并删掉 `.gitattributes` 的 5 行与 `.gitignore` 的 3 行即可；没有引入任何新文件、新依赖或仓库外状态。因为本次没有提交，`git checkout -- .github/workflows/build.yml .gitattributes .gitignore` 也是一条完整回滚路径。

## 14. 复现步骤

```powershell
# 1) 逐字一致性（YAML 里的 run 正文 == 本地实测脚本）
& '%LOCALAPPDATA%\Temp\hypomux-portable-test\verify-yaml-sync.ps1'

# 2) 端到端本地实跑（镜像仓库，不污染工作树）
& '%LOCALAPPDATA%\Temp\hypomux-portable-test\run-local-test.ps1'
# 日志：%LOCALAPPDATA%\Temp\hypomux-portable-test\local-test-log.txt

# 3) YAML 结构性校验
$env:GOROOT = "$env:TEMP\go-full\go"; $env:PATH = "$env:TEMP\go-full\go\bin;$env:PATH"
Set-Location '%LOCALAPPDATA%\Temp\yamlcheck'
go run . '<repo>\.github\workflows\build.yml' '%LOCALAPPDATA%\Temp\hypomux-portable-test\assemble-portable.ps1'
```

## 15. 契约冲突提示（需要 Lead / 用户裁决）

本任务要求在 `.github/workflows/build.yml` 中新增步骤，而 `reports/vnic/00-frozen-interface.md` 里有两处相反的规定：

- 第 11 行：**不得修改** `.github/workflows/**`、`desktop/build/**`、`nsis/**`、`protocol/v1/manifest.json` 之外的契约文件。
- 第 171 行（§7 CI 构建，ci-runner 规则）：**禁止**改任何 workflow 文件。

我把这个冲突原样报给 Lead，没有自行解释掉。判断依据是：用户的原始要求就是“让便携版由 GitHub 工作流产出”，而工作流里本来没有这个步骤，因此**必须**修改 workflow 文件才能满足需求；这属于 frozen interface 落后于需求，应由 Lead/用户明确豁免或更新契约文本，而不是由我静默越权。

本报告记录的事实是：改动已在工作树里完成并本地验证，但**尚未提交**，因此 Lead 仍有完全的选择权（提交、修改或整体回滚）。
