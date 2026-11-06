import { bgRed, green, red, yellow } from "kolorist";

export type Lang = "zh-cn" | "en-us";

export const locales = {
  "zh-cn": {
    gitCommitMessages: {
      types: "請選擇提交類型",
      scopes: "請選擇提交範圍",
      description: `請輸入描述信息（${yellow("!")}開頭表示破壞性改動`,
    },
    gitCommitTypes: [
      ["feat", "新功能"],
      ["feat-wip", "開發中的功能，比如某功能的部分代碼"],
      ["fix", "修復Bug"],
      ["docs", "只涉及文檔更新"],
      ["typo", "代碼或文檔勘誤，比如錯誤拼寫"],
      ["style", "修改代碼風格，不影響代碼含義的變更"],
      ["refactor", "代碼重構，既不修復 bug 也不添加功能的代碼變更"],
      ["perf", "可提高性能的代碼更改"],
      ["optimize", "優化代碼質量的代碼更改"],
      ["test", "添加缺失的測試或更正現有測試"],
      ["build", "影響構建系統或外部依賴項的更改"],
      ["ci", "對 CI 配置文件和腳本的更改"],
      ["chore", "沒有修改src或測試文件的其他變更"],
      ["revert", "還原先前的提交"],
    ] as [string, string][],
    gitCommitScopes: [
      ["projects", "項目"],
      ["packages", "包"],
      ["components", "組件"],
      ["hooks", "鈎子函數"],
      ["utils", "工具函數"],
      ["types", "TS類型聲明"],
      ["styles", "代碼風格"],
      ["deps", "項目依賴"],
      ["release", "發佈項目新版本"],
      ["other", "其他的變更"],
    ] as [string, string][],
    gitCommitNoStaged: `${bgRed(" 錯誤 ")} ${red("暫存區沒有文件，請先使用 git add 添加文件後再提交!")}`,
    gitCommitVerify: `${bgRed(" 錯誤 ")} ${red("git 提交信息必須符合 Conventional Commits 標準!")}\n\n${green(
      "推薦使用命令 `pnpm commit` 生成符合 Conventional Commits 標準的提交信息。\n獲取有關 Conventional Commits 的更多信息，請訪問此鏈接: https://conventionalcommits.org",
    )}`,
  },
  "en-us": {
    gitCommitMessages: {
      types: "Please select a type",
      scopes: "Please select a scope",
      description: `Please enter a description (add prefix ${yellow("!")} to indicate breaking change)`,
    },
    gitCommitTypes: [
      ["feat", "A new feature"],
      ["feat-wip", "Features in development, such as partial code for a certain feature"],
      ["fix", "A bug fix"],
      ["docs", "Documentation only changes"],
      ["typo", "Code or document corrections, such as spelling errors"],
      ["style", "Changes that do not affect the meaning of the code"],
      ["refactor", "A code change that neither fixes a bug nor adds a feature"],
      ["perf", "A code change that improves performance"],
      ["optimize", "A code change that optimizes code quality"],
      ["test", "Adding missing tests or correcting existing tests"],
      ["build", "Changes that affect the build system or external dependencies"],
      ["ci", "Changes to our CI configuration files and scripts"],
      ["chore", "Other changes that don't modify src or test files"],
      ["revert", "Reverts a previous commit"],
    ] as [string, string][],
    gitCommitScopes: [
      ["projects", "project"],
      ["packages", "packages"],
      ["components", "components"],
      ["hooks", "hook functions"],
      ["utils", "utils functions"],
      ["types", "TS declaration"],
      ["styles", "style"],
      ["deps", "project dependencies"],
      ["release", "release project"],
      ["other", "other changes"],
    ] as [string, string][],
    gitCommitNoStaged: `${bgRed(" ERROR ")} ${red("No files in staging area, please use git add to stage files first!")}`,
    gitCommitVerify: `${bgRed(" ERROR ")} ${red("git commit message must match the Conventional Commits standard!")}\n\n${green(
      "Recommended to use the command `pnpm commit` to generate Conventional Commits compliant commit information.\nGet more info about Conventional Commits, follow this link: https://conventionalcommits.org",
    )}`,
  },
} satisfies Record<Lang, Record<string, unknown>>;
