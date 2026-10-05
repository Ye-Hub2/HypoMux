export const productInfo = {
  name: "HypoMux",
  // 本仓库是上游开源项目的 fork，自行构建分发，界面统一标注为自定义版。
  // 改这里要同步 desktop/main.go 里主窗口的原生标题。
  edition: { zh: "自定义版", en: "Custom Edition" },
  version: "2.7.0",
} as const;
