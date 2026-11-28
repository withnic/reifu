# Phase 4: Skills 連携の CLI 統合（`link` / `status` / `sync` / `init`）・E2E テスト・ドキュメント更新

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/002_skills_sync.md`（正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* Phase 3 で実装済みの `internal/model`（`SkillsConfig`, `LinkModeJunction`, `ReifuState.ManagedSkills`, `StatusReport.Skills`）および `internal/fsx`（`DiscoverSkills`, `EnsureDirLink`, `CheckDirLink`, `RemoveDirLink`）

---

## 2. 今回の実装タスク

### 2.1 スキル同期ロジックの統合（`internal/syncer` / `internal/app`）
1. **共通のスキル計画・同期エンジン**（`internal/syncer/skills.go` 等に整理し、`sync` / `link` / `status` / `init` で共有する）:
   * `.reifu.jsonc` の `skills` 設定（未指定時はデフォルト `.agents/skills` → `.claude/skills`、`agents.targets` に `claude` がある場合）と `.reifu.state.json` の `managed_skills` を読み込む。
   * `.agents/skills/` が存在せず、かつ `managed_skills` も空の場合は完全 No-op（既存プロジェクトの出力・挙動を一切変えない）。
   * 同期対象ターゲットディレクトリ（例: `.claude/skills`）の親エージェントディレクトリ（例: `.claude`）の存在チェック:
     * `reifu sync` 実行時、`.claude/` が存在せず `agents.create_missing == false` かつ `--create` 未指定の場合はスキップする（既存エージェントディレクトリ存在ポリシーに準拠）。
     * `reifu link` 実行時、`.agents/skills/` にスキルがある場合はターゲット親ディレクトリ（`.claude/skills`）を `fsx.EnsureDirWithinRoot` で作成してリンクを張る。
   * **削除済み管理スキルのクリーンアップ**:
     * 前回 `managed_skills[targetDir]` に記録されていて、今回 `.agents/skills/` から消えたスキル `<removed>` について:
       * `<targetDir>/<removed>` がシンボリックリンク／ジャンクション（リンク切れ含む）なら `fsx.RemoveDirLink` で安全に削除する。
       * 通常ディレクトリ（ユーザーが独自に作り替えた、または管理外）なら `--force` なしでは削除せず警告を `stderr` へ出す（`--force` 時のみ削除）。
     * `.agents/skills/` にも `managed_skills` にも含まれない `.claude/skills/<manual>`（手動の Claude 専用スキル）は一切触らない。
   * **非破壊保護**:
     * `.claude/skills/<name>` にすでに同名の独立ディレクトリ（または内容の異なるコピー）が存在し `--force` 未指定の場合はエラー終了する。

### 2.2 各サブコマンドへの組み込み（`internal/app`）
1. **`reifu link [dir]`**（`internal/app/link_cmd.go`）:
   * `AGENTS.md` のリンク検査・作成に加えて、スキルリンクの検査・作成・管理済み削除スキルのクリーンアップを行う。
   * `--check`: 各スキルリンクの `target  STATUS (mode)` を出力し、不整合（`auto`/`hard`/`symlink` モードで `OK` 以外、`copy` モードで `COPY` 以外）が 1 件でもあれば Exit `3`（`ExitLinkIntegrity`）。
   * 非 `--check`: スキルリンクを作成／更新し、`.reifu.state.json` が既存であれば `managed_skills` も更新する。
2. **`reifu status [dir]`**（`internal/app/status_cmd.go`）:
   * スキルリンクの状態を検査して `StatusReport.Skills` に格納する（スキル対象が 0 件なら `nil` のままにし、`--json` の `skills` フィールドを省略して既存出力と完全互換にする）。
   * 通常出力でスキルリンクがあれば各行を表示する。
   * スキルリンクに不整合があれば `HasIssues = true` とし、`--quiet` 時は Exit `2` を返す。
3. **`reifu sync [dir]`**（`internal/app/sync_cmd.go` & `internal/syncer/sync.go`）:
   * ターゲット設定ファイルの同期とあわせてスキルリンクの同期・クリーンアップを実行し、変更パスを `result.ChangedPaths` に含め、`.reifu.state.json` の `managed_skills` を更新する（`--dry-run` 時は変更予定のみ表示し書き込みを行わない）。
   * `reifu init [dir]`（`internal/app/init_cmd.go`）でも、既存ディレクトリに `.agents/skills/` がある場合は初回同期の一環としてスキルリンクを生成する（`.agents/skills/` が無ければ作成しない）。

### 2.3 ドキュメント更新
* **`README.md`**: 特長またはコマンド概要に `.agents/skills/` → `.claude/skills/` のマルチエージェント Skills 連携（`reifu link` / `reifu sync` / `reifu status`）を簡潔に追記。
* **`HOWTO.md`**: 日常開発または新セクションとして、`.agents/skills/<name>/SKILL.md` を正本にして 4 エージェント（Antigravity / Codex / Gemini / Claude）で共有する手順・`.reifu.jsonc` の `skills` 設定・`git clone` 後の `reifu link --force`・手動 Claude 固有スキルとの共存ルールを記載。
* **`docs/specs/002_skills_sync.md`**: 受け入れ基準チェックリストをすべて `[x]` に更新。

---

## 3. テスト要件

1. **`internal/syncer/syncer_test.go`**（または `skills_test.go`）:
   * `.agents/skills/` 未存在時の完全 No-op 検証。
   * `.agents/skills/my-skill/SKILL.md` がある状態での `Sync`（`--dry-run` 含む）による `.claude/skills/my-skill` リンク作成と `.reifu.state.json` の `managed_skills` 記録。
   * 手動配置の `.claude/skills/claude-only/SKILL.md` が同期・削除クリーンアップ後も無傷で残ること。
   * 正本 `.agents/skills/my-skill` を削除した後の `Sync` で `.claude/skills/my-skill` リンクが安全に削除され、他の手動スキルは残ること。
   * `skills.enabled: false` でスキル同期が無効化されること。
2. **`cmd/reifu/main_test.go`**（CLI E2E テスト、`t.TempDir()` 完結）:
   * `.agents/skills/deploy-check/SKILL.md`（＋ `scripts/check.sh`）と手動 `.claude/skills/local-only/SKILL.md` を配置したプロジェクトで以下を一気通貫検証:
     * `reifu sync --dry-run` が `.claude/skills/deploy-check` の予定を表示しつつ書き込まないこと
     * `reifu sync` 後に `.claude/skills/deploy-check/SKILL.md` が読め、`reifu status --json` の `skills` に `OK`（`symlink` または `junction`）が出力され、`reifu status --quiet` と `reifu link --check` が Exit `0` になること
     * `git clone` シミュレート（`.claude/skills/deploy-check` を独立コピーディレクトリへ置換）後に `reifu link --check` が Exit `3`、`reifu status --quiet` が Exit `2`、`reifu link`（`--force` なし）が非破壊保護で Exit `1`、`reifu link --force` で修復されて再び Exit `0` になること
     * `.agents/skills/deploy-check` 削除後の `reifu sync` で `.claude/skills/deploy-check` がクリーンアップされ、手動 `.claude/skills/local-only/SKILL.md` は残ること
     * `.agents/skills/` が無いプロジェクトの `reifu status --json` 出力に `"skills"` キーが出ないこと（後方互換性検証）

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（一時バイナリを残さないこと）
* `docs/specs/002_skills_sync.md` の受け入れ基準チェックリストがすべて満たされていること。
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テスト改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・残課題（あれば）を簡潔に報告すること。
