# Phase 7: コンパニオン向けコマンド `--json` API・`status` ランナー情報拡張＆ドキュメント更新

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/004_companion_api.md`（本機能の正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* 既存コード: `internal/model/state.go`, `internal/model/run_state.go`, `internal/template/presets.go`, `internal/runner/agents.go`, `internal/runner/git.go`, `internal/runner/logs.go`, `internal/runner/runner.go`, `internal/app/preset_cmd.go`, `internal/app/sync_cmd.go`, `internal/app/link_cmd.go`, `internal/app/init_cmd.go`, `internal/app/status_cmd.go`, `cmd/reifu/main_test.go`

---

## 2. 今回の実装タスク

本フェーズでは `docs/specs/004_companion_api.md` の **Phase 7 範囲** を実装・検証し、設計書およびユーザー向けドキュメントを最新化する。

### 2.1 `internal/model` と `internal/template` の JSON モデル整備
* `internal/template/presets.go`:
  * `Preset` に JSON タグを追加: `Name string \`json:"name"\``, `Source string \`json:"source"\``。
* `internal/model/state.go`:
  * 以下の JSON レスポンス構造体を追加する:
    * `SyncReport`（`Root`, `DryRun`, `StateMissing`, `ChangedPaths`, `Warnings`, `Targets`, `Skills`）
    * `LinkCommandReport`（`Root`, `Mode`, `Check`, `HasIssues`, `Links`, `Skills`）
    * `SetupReport`（`Command`, `Root`, `Preset`, `DryRun`, `Files`, `Links`, `ChangedPaths`, `Warnings`）
    * `RunnerStatus`（`Blocked`, `GitRepo`, `GitDirty`, `Phases`, `Agents`, `LatestRun *RunSummary`）
    * `AgentStatus`（`Name`, `Available`, `Executable`, `IsCustom`）
  * `StatusReport` に `Runner *RunnerStatus \`json:"runner,omitempty"\`` を追加する（既存フィールドの名称・型・意味は一切変更しない）。

### 2.2 `internal/runner` のプロジェクト状態検査（`InspectStatus`）
* `internal/runner` に `InspectStatus(ctx context.Context, root string, cfg model.ReifuConfig) model.RunnerStatus` を実装する。
* **絶対にエラーを返さず、常にベストエフォートで値を埋める**（未初期化プロジェクト・非 Git 環境・エージェント未インストール環境でも `reifu status --json` が Exit `0` で動作するため）:
  * `Blocked`: プロジェクトルート直下の `BLOCKED_REPORT.md` を `os.Lstat` で確認し存在すれば `true`（`fsx.ValidateWithinRoot` 境界チェック遵守）。
  * `GitRepo` / `GitDirty`: 5 秒タイムアウト付き sub-context で `IsGitRepo` を確認。Git 未導入・リポジトリ外・エラー時は `GitRepo: false, GitDirty: false`。リポジトリ内なら `StatusPorcelain` を呼び、エラーなし＆非空なら `GitDirty: true`。
  * `Phases`: `prompts/phases` 配下の `.md` ファイル（ディレクトリ除く・名前不正や重複はスキップ）を昇順で列挙し、`.md` 除去済みの名前スライスを返す。不在・空なら空スライス `[]string{}`。
  * `Agents`: 組み込み 4 種（`agy`, `claude`, `codex`, `gemini`）と `cfg.Runner.Agents` のキーを重複排除・昇順ソートし、`ResolveAgent` → `lookPath(root)` で存在確認する。見つかれば `Available: true, Executable: absPath`、見つからなければ `Available: false`。`IsCustom` も正確に設定する。
  * `LatestRun`: `LoadRun(root, "")` が成功したら `summary := run.Summary(); status.LatestRun = &summary`。失敗・不在時は `nil`。

### 2.3 CLI コマンドの `--json` 対応（`internal/app`）
すべての `--json` 出力で `writeJSON(w io.Writer, v any) error` のような共通ヘルパー（`json.NewEncoder` + `SetEscapeHTML(false)`）を用い、`stdout` に純粋な単一 JSON のみを出力する。

1. **`internal/app/preset_cmd.go`**:
   * `reifu preset list` に `--json` を追加。`[]template.Preset`（空でも `[]`）を出力する。
2. **`internal/app/sync_cmd.go`**:
   * `reifu sync` に `--json` を追加。
   * `--json` 時は `options.Warnings = nil` にして警告が `stderr` に重複出力されないようにし、`model.SyncReport`（`ChangedPaths` は非 nil スライス）を `stdout` に出力する。
   * `--dry-run --json` でも書き込みなしで同じ JSON を返す。
3. **`internal/app/link_cmd.go`**:
   * `reifu link` に `--json` を追加。
   * `--check --json` 時は `model.LinkCommandReport` を `stdout` に出力し、`HasIssues` が `true` なら Exit `3`（`model.ExitLinkIntegrity`）を返す（`stdout` にプレーンテキストを一切出さない）。
   * 通常のリンク作成・修復時の `--json` でも、`Links` と `Skills` の結果を `model.LinkCommandReport` として `stdout` に出力する（プレーンテキスト行を出さない）。
4. **`internal/app/init_cmd.go`**:
   * `reifu new` / `reifu init` に `--json` を追加。
   * `--json` 時は `"Initialized ..."`, `"Would write ..."`, `"Would link ..."`, `createLinks` のプレーンテキスト出力、および `syncer` の `Warnings` の `stderr` 出力をすべて抑止し、`model.SetupReport` を `stdout` に出力する。
   * `--dry-run --json` でも書き込みなしで予定ファイル・リンク・変更予定パスを同スキーマで出力する。
5. **`internal/app/status_cmd.go`**:
   * `runner.InspectStatus(context.Background(), root, cfg)` を呼び出して `report.Runner = &runnerStatus` を設定する。
   * `report.HasIssues` および `status --quiet`（ドリフト時 Exit `2`）の判定ロジックは一切変更しない。
   * プレーンテキスト出力（`printStatus`）の既存フォーマットは維持する。
6. **`internal/app/app.go`**:
   * トップレベルヘルプ `usage` が必要なら `--json` 対応に合わせて簡潔に最新化する（既存行を壊さない）。

### 2.4 ドキュメント＆チェックリスト更新
* `docs/design/02_data_modeling.md`: `RunEvent`（NDJSON スキーマ）、`SyncReport` / `LinkCommandReport` / `SetupReport` / `RunnerStatus` のデータ構造を追記する。
* `docs/design/03_architecture.md`: `--stream-json`、`--cancel-on-stdin-eof`、各コマンドの `--json` 対応およびコンパニオン連携方式を追記する。
* `README.md` / `HOWTO.md`: コンパニオンアプリ・自動化スクリプト向けの利用例（`--stream-json`, `--cancel-on-stdin-eof`, 各 `--json` コマンド）を簡潔に追記する。
* `docs/specs/004_companion_api.md`: 受け入れ基準チェックリストをすべて `[x]` に更新する。

---

## 3. テスト要件

1. **`internal/runner/runner_test.go`**:
   * `InspectStatus` のユニットテスト:
     * 非 Git・`prompts/phases` 不在・`BLOCKED_REPORT.md` 不在・エージェント未存在（空 PATH や存在しないカスタムコマンド）でもパニックやエラーなく `GitRepo: false`, `GitDirty: false`, `Blocked: false`, `Phases: []`, `LatestRun: nil`, `Available: false` が返ること。
     * Git リポジトリ（クリーン → 未追跡ファイル追加で dirty）、`BLOCKED_REPORT.md` 作成、`prompts/phases/*.md` 昇順列挙、カスタムエージェント（存在する実行ファイルと存在しない実行ファイル）、`LatestRun` 取得がすべて正しく反映されること。
2. **`cmd/reifu/main_test.go`**:
   * `reifu preset list --json` の出力が有効な JSON 配列で組み込みプリセットを含むこと。
   * `reifu sync --dry-run --json` および `reifu sync --json` の出力が `SyncReport` としてデコードでき、`stdout` に非 JSON テキストが混入しないこと。
   * `reifu link --json` および `reifu link --check --json`（正常時 Exit `0`、リンク破損・欠損時 JSON 出力＋Exit `3`）を検証すること。
   * `reifu new --dry-run --json`、`reifu new --json`、`reifu init --dry-run --json`、`reifu init --json` が `SetupReport` としてデコードでき、`stdout` が純粋 JSON であることを検証すること。
   * `reifu status --json` が既存フィールドをすべて維持しつつ `runner`（`blocked`, `git_repo`, `git_dirty`, `phases`, `agents`, `latest_run`）を含み、エージェント未検出や `prompts/phases` 空でも `has_issues: false` / Exit `0` であることを検証すること。
   * `reifu status --quiet` の既存終了コード（健全時 Exit `0`、設定ドリフト時 Exit `2`）が変わらないことを検証すること。

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（一時バイナリを残さないこと）
* `docs/specs/004_companion_api.md` の全受け入れ基準チェックリストが `[x]` で満たされていること。
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テスト改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・残課題（あれば）を簡潔に報告すること。

---

## 5. Phase 6 からの引き継ぎ（ドキュメント反映時に記載すること）

`docs/design/02_data_modeling.md` / `03_architecture.md` / `README.md` へ `--stream-json` を記載する際は、Phase 6 実装の以下の挙動をそのまま正本として書くこと（コード変更は不要）。

* `RunEvent.elapsed_ms` は `omitempty` なしで全イベントに出力される（意味を持つのは `heartbeat` / `step_completed` / `phase_completed` / `phase_failed` のみ。その他は `0`）。他のフィールドは空なら省略される。
* 終端イベント（`run_plan` / `run_completed` / `run_failed`）は 1 ランにつき必ず 1 行だけ出る。ランナーが終端イベントを出す前にエラー終了した場合だけ、CLI がフォールバックの `run_failed` を出す。フォールバックの `status` は Exit `2`（`BLOCKED_REPORT.md` 存在）なら `blocked`、それ以外は `failed`。
* `--resume` で `implement_commit` / `review_commit` から再開した場合、そのステップの `step_started` は出ず、`commit` / `step_completed` だけが出る。
* キャンセル・タイムアウトは `status: "failed"` で、区別は `error` 文字列（`context canceled` / `context deadline exceeded`）で行う。
* `--stream-json` 時、`--notify` の端末フォールバック出力は `stderr` へ出し、`stdout` には出さない。ランサマリ書き込み失敗などの警告テキストは破棄される（`stdout` に混ぜないため）。
* `--cancel-on-stdin-eof` は `--dry-run` では無効。ラン開始前に `stdin` がすでに閉じていると、ランは即キャンセルされて `run_failed` になる。`stdin` を読む goroutine はラン完了後も `Read` が返るまで残る（CLI プロセスは終了するので実害はない）。
* イベント送出のロック順は `agentStream.mu` → `execution.eventMu` で固定している。`stdout` の読み手が止まると、エージェントの出力を書き込む処理もブロックされる（背圧）。
* **既知の不安定さ（未解決）**: Phase 6 レビュー時、Windows で `go test -race ./...` を実行したところ `TestFollowLog` が 1 回だけ 0.08 秒で失敗した（再実行 8 回では再現しなかった）。原因の候補は、ランナーが `.reifu.state.json` をアトミックに置き換えている最中に `LoadRun` → `loadState` が読むと、Windows では一時的に共有違反やアクセス拒否になり、読む側にはリトライがないこと。`InspectStatus` の `LatestRun` は実行中のランに対して `LoadRun` を呼ぶので、同じ失敗が起こりうる（ベストエフォートなので `nil` になるだけだが、GUI の表示がちらつく）。対応するなら、`fsx` に読み取り用の共有違反リトライを用意して `loadState` から使うこと。
