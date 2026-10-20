# v0.1.0 发布清单

更新：2026-10-20。每一步凡是对外可见或不可逆的操作（合并到 main、动 tag、改包可见性），都要用户明确批准后再执行。

## 0. 发布前的两个决定

1. **版本号已被占用。** 远端已有旧代码的 tag：`v0.1.0` → `19527d6`、`v0.2.0` → `857e8d8`，都是 2026-09-29 重构前的版本。仓库没有 GitHub Release。可选方案：
   - 推荐：先把旧 tag 改名保留，例如 `archive/v0.1.0-legacy` 和 `archive/v0.2.0-legacy`，指向原提交不变，再删除远端的 `v0.1.0`、`v0.2.0`，新版本沿用 v0.1.0。旧提交仍然可以通过改名后的 tag 找到。
   - 不动旧 tag：新版本改用 `v0.3.0`。文档里所有 `v0.1.0` 统一替换，CHANGELOG 注明"重构后的首个公开版本"。
2. **main 落后 dev 168 个提交。** main 是默认分支，公开仓库首页、`install.py` 的默认 ref 都指向 main，而 main 上还没有 release.yml。发布前先开 dev → main 的 PR 并合并（`--merge`，保留历史），tag 打在 main 的合并提交上。

## 1. 发布前检查

- [ ] docs PR（README / deploy README / CHANGELOG 去掉"v0.1.0 起提供"等过时说明，补"已知问题"）已合并到 dev。
- [ ] dev 的 CI 全绿。
- [ ] dev → main 的 PR 已合并，main 的 CI 全绿。
- [ ] 按第 0 节的决定处理旧 tag。

## 2. 打 tag 并发布镜像

```bash
git -C /home/yang/mycode/swarmjet fetch origin
git -C /home/yang/mycode/swarmjet tag -a v0.1.0 origin/main -m "SwarmJet v0.1.0"
git -C /home/yang/mycode/swarmjet push origin v0.1.0
```

- [ ] Release workflow 先跑 checks，再构建并推送四个镜像 tag：`ghcr.io/yyydddkkk/swarmjet:{v0.1.0,latest}` 和 `ghcr.io/yyydddkkk/swarmjet/agent:{v0.1.0,latest}`（linux/amd64）。
- [ ] 在 GitHub 的 Packages 设置里把 `swarmjet` 和 `swarmjet/agent` 两个包设为 Public。本机 gh 的 Token 没有 packages 权限，这一步由用户在网页上操作。
- [ ] 可选：用 CHANGELOG 的 v0.1.0 段落创建 GitHub Release（`gh release create v0.1.0 --notes-file ...`）。

## 3. 发布后验证

```bash
docker logout ghcr.io
docker pull ghcr.io/yyydddkkk/swarmjet:v0.1.0
docker pull ghcr.io/yyydddkkk/swarmjet/agent:v0.1.0
```

干净环境（新的 Linux x86_64 主机或虚拟机，不复用本机的 sj-r3 部署和端口）：

```bash
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/v0.1.0/deploy/install.py | python3 - --image-source ghcr --ref v0.1.0 --version v0.1.0
```

然后按 README 的"快速上手"走一遍：登录 → 创建 GitHub App → 填模型凭证 → 向导创建 Hive → 与 Queen 对话选方法论 → 交代一个小任务。

## 4. 已知问题（已写入 CHANGELOG，计划下一轮修复）

见 [acceptance-round5.md](acceptance-round5.md) 第 10 节：方法论并发上限覆盖 Hive 上限；自动回滚计数归属错误；重开 Queen 的引导文案；verifier FAIL 路径未做真实验证；第四轮遗留的 parked worker 占名额。
