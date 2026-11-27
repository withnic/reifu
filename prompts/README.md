# Prompts — reifu 実行ガイド（人間向け）

このフォルダは AI エージェントへ渡すプロンプトを管理します。

* `phase0.md` — 【初回・対話】仕様整理、`docs/design/` と `prompts/phases/01_*.md`〜 を生成
* `add_feature.md` — 【追加開発・対話】新仕様 `docs/specs/NNN_*.md` 作成、`docs/design/` 更新、追加フェーズ生成
* `review.md` — 【レビュー】各フェーズ実装後の品質・セキュリティ確認と `docs/design/` への知見還元
* `phases/` — 【自動実行キュー】`01_xxx.md`, `02_xxx.md` ... の実装フェーズ専用（`*.md` でそのまま順番実行可能）

---

## ヘッドレスでの実行例（人間がシェルから実行）

### 1 フェーズずつ実行 → レビュー → コミット（推奨）

**PowerShell（Codex CLI の例）**:
```powershell
Get-Content -Raw prompts/phases/01_feature.md | codex exec --trust -s workspace-write -
Get-Content -Raw prompts/review.md | claude -p
git add -A && git commit -m "feat: complete phase 1"
```

**bash / zsh（Claude Code の例）**:
```sh
claude -p < prompts/phases/01_feature.md
claude -p < prompts/review.md
git add -A && git commit -m "feat: complete phase 1"
```

### `prompts/phases/*.md` を連続で自動実行する

**PowerShell（Codex CLI）**:
```powershell
Get-ChildItem prompts/phases/*.md | Sort-Object Name | ForEach-Object {
    Write-Host "=== Running $($_.Name) ==="
    Get-Content -Raw $_.FullName | codex exec -s workspace-write -
    if ($LASTEXITCODE -ne 0) { throw "Stopped at $($_.Name)" }
}
```

**bash / zsh（Claude Code）**:
```sh
for f in prompts/phases/*.md; do
  echo "=== Running $f ==="
  claude -p < "$f" || break
done
```
