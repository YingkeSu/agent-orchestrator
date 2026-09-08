# Fork 开发工作流(AO 魔改指南)

本仓库是 [Untrivial-ai/agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator)
的个人 fork,用于自定义开发("魔改")。本文记录 fork 的分支模型、本地打包覆盖正式应用的
流程、以及同步上游更新的操作。

## 仓库与分支模型

| 远程 | 地址 | 用途 |
|---|---|---|
| `origin` | `YingkeSu/agent-orchestrator` | 自己的 fork,日常推送 |
| `upstream` | `Untrivial-ai/agent-orchestrator` | 官方上游,只拉不推(push 已被设为 `no-push`) |

| 分支 | 规则 |
|---|---|
| `main` | **只镜像上游,不提交任何自己的修改**,保证永远可以 fast-forward 跟随 `upstream/main` |
| `mods/main` | 所有魔改都放在这里,长期分支,随上游 rebase 前进 |

其他本地配置:`git config commit.template .gitmessage`(提交时自动带上 Co-authored-by 尾注)。

## 环境依赖

- Go 1.25.7+(本机已装 Homebrew Go 1.27.1)
- Node.js 20.19.0+ 和 npm 10
- clang / make(编译打包进应用的 tmux、ACP runtime 等;Xcode Command Line Tools 即可)

## 日常开发(不影响正式安装)

```bash
cd frontend
npm install        # 首次
npm run dev        # Electron 开发模式,predev 自动构建 Go daemon 等
npm run dev:web    # 仅渲染层,快速 UI 迭代
```

后端单独调试:

```bash
cd backend
go run .                       # 启动 daemon(127.0.0.1)
go run ./cmd/ao status         # 另一个终端里操作
```

改动验证:`cd backend && go test ./...`;前端 `npm run typecheck`。API/DTO 改动后记得
`npm run api`(详见根目录 `AGENTS.md`)。

## 打包并覆盖正式安装的应用

```bash
scripts/fork-dev-install.sh                # 打包 + 替换 /Applications 里的应用
scripts/fork-dev-install.sh --no-install   # 只打包,产物留在 frontend/out/
```

脚本等价于:

```bash
cd frontend
AO_RELEASE_REPO=YingkeSu/agent-orchestrator npm run package
osascript -e 'quit app "Agent Orchestrator"'
rm -rf "/Applications/Agent Orchestrator.app"
cp -R "out/Agent Orchestrator-darwin-arm64/Agent Orchestrator.app" /Applications/
```

### 为什么必须设 `AO_RELEASE_REPO`

打包时 `frontend/forge.config.ts` 会把 electron-updater 的更新源(`app-update.yml`)
烘焙进应用,**默认指向官方仓库**。不设的话,应用一旦检查更新,官方版本会把魔改版覆盖
回去。指向 fork 后,更新器只会查 fork 的 Releases(没有任何 release),魔改版永远不会
被官方更新替换。应用内的功能构建(feature builds)列表读同一个文件,因此也保持一致。

第二道保险:应用内自动更新默认是关闭的(`update-settings.ts` 的 `enabled: false`),
**不要在设置里打开它**。

### 签名与数据

- 本地构建**未签名、未公证**。首次打开若被 macOS 拦截,到"系统设置 → 隐私与安全性"
  点"仍要打开"。
- 本地构建与官方版 bundle id 相同(`dev.agent-orchestrator.desktop`),AO 的所有状态都在
  `~/.ao`(daemon 数据、worktrees、Electron 的 `~/.ao/electron`),替换应用后项目、会话、
  配置无缝延续。
- 回到官方版:从 [官方 Releases](https://github.com/Untrivial-ai/agent-orchestrator/releases)
  下载重装即可,数据不受影响。

## 同步上游更新

```bash
scripts/fork-sync-upstream.sh
```

脚本做三件事:`main` 快进到 `upstream/main` 并推到 fork;`mods/main` rebase 到新
`main` 上;提示你验证后手动 `git push origin mods/main --force-with-lease`(rebase 改写
历史,推送必须带 `--force-with-lease`,且留给人工确认是刻意的)。

手动等价操作:

```bash
git fetch upstream
git checkout main
git merge --ff-only upstream/main
git push origin main

git checkout mods/main
git rebase main
# 解决冲突后:git rebase --continue
# 验证:cd backend && go test ./... && cd ../frontend && npm run typecheck
git push origin mods/main --force-with-lease
```

冲突只可能出现在 `mods/main` 自己改过的文件上。rebase 完务必先跑测试再推送。

## 禁止事项

- **不要跑 `npm run publish`**(frontend 下):它会把构建产物发布到 GitHub Releases,
  属于发布流程,本地开发用 `npm run package` 就够了。
- **不要在 `main` 上提交自己的修改**:一旦 main 有了自有提交,`--ff-only` 快进就会失败,
  整个同步模型被破坏(修复需要 reset,容易误伤)。
- 不要往 `upstream` 推送(push 已禁用,这里是留档说明)。
