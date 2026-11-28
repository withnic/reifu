# 004_companion_api — コンパニオンアプリ連携向けストリーミング・キャンセル・JSON API 整備

## 1. 背景・目的

`reifu` を外部ツールや GUI コンパニオンアプリ（例: Rust 製コンパニオン + Zed エディタ併用ワークフロー）のバックエンドエンジンとして安定利用できるようにする。
現行（`v0.3.0`）では `status` / `version` / `logs` / `run`（最終結果のみ）が `--json` に対応しているが、GUI から安全・堅牢に連携するには以下の 4 点が不足している。

1. **`reifu run` のリアルタイム進捗取得**: 現状の `--json` は全フェーズ完了後に 1 回だけ `RunState` を出力するため、実行中のフェーズ遷移・エージェント生ログ・ハートビート・コミットを構造化受信できない。
2. **Windows 非コンソール環境からの安全なキャンセル**: GUI 親プロセスがコンソールなしで `reifu run` を起動した場合、`SIGINT`/`SIGTERM` を綺麗に送れず強制終了になるとチェックポイント保存や子エージェントの Job Object 終了が保証されない。
3. **操作系コマンドの `--json` 対応**: `preset list` / `sync` / `link` / `new` / `init` がプレーンテキスト出力のみで、GUI 側の新規作成・同期・リンク検査画面から構造化結果を得にくい。
4. **ダッシュボード向けランナー状況取得**: プロジェクトを開いた直後に、`prompts/phases/*.md` 一覧・利用可能エージェント CLI・`BLOCKED_REPORT.md` 有無・Git 状態（初期化済み / 未コミット変更）・最新ランサマリを 1 回の読み取りでエラーなく取得したい。

本仕様では、既存 CLI 挙動・終了コード・JSON スキーマとの 100% 後方互換性を維持しつつ、ゼロ外部依存（Go 標準ライブラリのみ）で上記を整備する。

---

## 2. スコープ

### In-Scope
* **Phase 6（実行コアの機械連携）**:
  * `reifu run --stream-json`: NDJSON（1行1 JSON オブジェクト、UTF-8 / LF）によるリアルタイムイベント＆ログストリーム出力
  * `reifu run --cancel-on-stdin-eof`: `stdin` の EOF / クローズを検知してランの `context` をキャンセルする opt-in キャンセル機構
* **Phase 7（CLI JSON API 拡張・ダッシュボード情報・ドキュメント）**:
  * `reifu preset list --json`
  * `reifu sync --json`（`--dry-run --json` および警告配列含む）
  * `reifu link --json`（`--check --json` 含む）
  * `reifu new --json` / `reifu init --json`（`--dry-run --json` 含む）
  * `reifu status --json` への `runner` セクション拡張（`blocked`, `git_repo`, `git_dirty`, `phases`, `agents`, `latest_run`）
  * 設計書（`docs/design/02_data_modeling.md`, `docs/design/03_architecture.md`）および `README.md` / `HOWTO.md` 更新

### Out-of-Scope
* HTTP/RPC サーバー・デーモン化・IPC ソケット（サブプロセス実行＋標準入出力で完結させる）
* グローバル設定ディレクトリ（`~/.claude/`, `~/.codex/`, `~/.gemini/` 等）の読み書き
* `reifu new` による自動 `git init`（既存の非破壊ポリシーを維持）

---

## 3. 詳細仕様

### 3.1 `reifu run --stream-json`（NDJSON ストリーム）

#### CLI ルール
* `reifu run` に `--stream-json`（bool, デフォルト `false`）を追加する。
* `--stream-json` と `--json` は排他とする（同時指定時は Exit `1` エラー）。
* `--stream-json` 指定時、`stdout` には **厳密な NDJSON（各行が単一の有効 JSON オブジェクト、`SetEscapeHTML(false)`、行末 `\n`）のみ** を出力する。
* エージェントの生出力や進捗バナー、ハートビートを `stdout` にプレーンテキスト混入させてはならない（また通常時の `stderr` 出力と重複させない）。ログファイル（`.reifu/runs/<id>/*_implement.log` / `*_review.md`）への書き込みは従来通り維持する。
* 複数 goroutine（ハートビートタイマー、`agentStream`、メインループ）から同時にイベントを発行しても JSON 行が破損しないよう、`sync.Mutex` でシリアライズして書き込む。

#### イベントスキーマ（`internal/model/run_state.go`）
* 循環依存を避けるため、`AgentCommand` と `RunPlan` を `internal/model` に定義し、`internal/runner` では型エイリアス（`type AgentCommand = model.AgentCommand`, `type Plan = model.RunPlan`）として後方互換を保つ。
* `RunEventType` 定数と `RunEvent` 構造体:
  * `run_plan`: `--dry-run --stream-json` 時に 1 行だけ出力（`Plan` 格納）
  * `run_started`: `execute()` 開始（初期チェックポイント保存直後、`State` 格納）
  * `phase_started`: フェーズ実行開始（`Phase`, `PhaseIndex` [1-based], `PhaseTotal`, `Step`）
  * `step_started`: `implement` または `review` エージェント起動（`Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Agent`, `Path`, カスタムエージェント時は `Warning`）
  * `log`: `agentStream` が UTF-8 / LF 正規化済みの非空テキストチャンクを確定するたびに発行（`Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Agent`, `Text`）
  * `heartbeat`: ハートビート間隔到達時（`Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Agent`, `ElapsedMS`）
  * `commit`: `implement` または `review` 後にコミット SHA を確定したとき（`Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Commit`）
  * `step_completed`: `implement` または `review` ステップ完了時（`Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Agent`, `Commit`, `Path`, `ElapsedMS`）
  * `phase_completed`: フェーズ完了＆保存時（`Phase`, `PhaseIndex`, `PhaseTotal`, `Status: "completed"`, `ElapsedMS`）
  * `phase_failed`: フェーズ失敗 / ブロック時（`Phase`, `PhaseIndex`, `PhaseTotal`, `Status: "failed"|"blocked"`, `Error`, `ElapsedMS`）
  * `run_completed`: ラン完走時（`Status: "completed"`, `State`）
  * `run_failed`: ラン失敗 / ブロック / タイムアウト / キャンセル / プリフライト失敗時（`Status: "failed"|"blocked"`, `Error`, `State` [存在時]）
* すべての `RunEvent` は `schema_version: 1`、`type`、`timestamp`（UTC RFC3339Nano）を含む。
* `run_started` 前にエラー終了した場合（dirty worktree、lock 取得失敗、missing implementer、`BLOCKED_REPORT.md` 存在等）でも、`--stream-json` 時は最終フォールバックとして `run_failed` を 1 行だけ `stdout` に出力し（二重発行は禁止）、従来通りの非 0 終了コードで終了する。フラグパースエラー時は従来通り `stderr` + Exit `1`。

#### `internal/runner` の設計
* `runner.Options` に `EventSink func(model.RunEvent)` を追加する。
* `EventSink != nil` の場合:
  * 構造化イベントを `EventSink` へ通知する。
  * `agentStream` はログファイルへ書き込むと同時に、`EventSink` へ `log` イベントを送る（`Progress` へのテキスト複製は行わない）。

---

### 3.2 `reifu run --cancel-on-stdin-eof`（安全なキャンセル）

* `reifu run` に `--cancel-on-stdin-eof`（bool, デフォルト `false`）を追加する。
  * デフォルト `false` を厳守し、既存の CLI 実行やリダイレクト（`< /dev/null`）の挙動を壊さない。
* `--cancel-on-stdin-eof` が `true` かつ `--dry-run` でない場合、`CLI.Stdin`（nil 時は監視しない）をバックグラウンド goroutine で読み込み、EOF または読み込みエラーを検知した時点でランの `context` をキャンセルする。
* キャンセル発火後は既存のキャンセル経路（`sysenv.KillTreeOnCancel` による子プロセスツリー終了 → `e.fail` でチェックポイント保存 → 非 0 終了）がそのまま動き、`--stream-json` 併用時は `phase_failed` および `run_failed` イベントが `stdout` に確実に出力される。
* ランが正常完了した場合に不要なパニックやレースを起こさないこと。

---

### 3.3 既存コマンドの `--json` 拡張

すべての `--json` 出力は `SetEscapeHTML(false)` を用い、`stdout` に純粋な単一 JSON のみを出力する。配列フィールドは `nil` ではなく空スライス `[]`（`omitempty` でない必須配列）としてシリアライズする。

1. **`reifu preset list --json`**
   * `template.Preset` に JSON タグ `name`, `source` を付与し、`[]template.Preset` を出力する。
2. **`reifu sync --json`**
   * `model.SyncReport` を出力する:
     ```json
     {
       "root": "...",
       "dry_run": false,
       "state_missing": false,
       "changed_paths": [".claude/settings.json"],
       "warnings": ["..."],
       "targets": { "claude": { "path": "...", "status": "IN_SYNC" } },
       "skills": [{ "target": ".claude/skills/foo", "status": "OK", "mode": "junction" }]
     }
     ```
   * `--json` 時は警告を `stderr` に書かず `warnings` フィールドへ格納する。
   * `--dry-run --json` でも書き込みを行わず同スキーマで出力する。
3. **`reifu link --json`**
   * `model.LinkCommandReport` を出力する:
     ```json
     {
       "root": "...",
       "mode": "auto",
       "check": true,
       "has_issues": false,
       "links": [{ "target": "CLAUDE.md", "status": "OK", "mode": "hard" }],
       "skills": [{ "target": ".claude/skills/foo", "status": "OK", "mode": "junction" }]
     }
     ```
   * `--check --json` で問題あり（`has_issues: true`）の場合も JSON を `stdout` に出力した上で Exit `3`（`ExitLinkIntegrity`）で終了する。
   * 通常実行（リンク作成・修復）の `--json` 時もプレーンテキスト行を `stdout` に出さず、結果の `links` / `skills` を JSON 出力する。
4. **`reifu new --json` / `reifu init --json`**
   * `model.SetupReport` を出力する:
     ```json
     {
       "command": "new",
       "root": "...",
       "preset": "rust-cli",
       "dry_run": false,
       "files": ["AGENTS.md", ".reifu.jsonc"],
       "links": [{ "target": "CLAUDE.md", "status": "OK", "mode": "hard" }],
       "changed_paths": [".claude/settings.json", ".reifu.state.json"],
       "warnings": []
     }
     ```
   * `--dry-run --json` でも書き込みを行わず予定ファイル・リンクを同スキーマで出力する。

---

### 3.4 `reifu status --json` の `runner` 情報拡張

* `model.StatusReport` に `Runner *model.RunnerStatus \`json:"runner,omitempty"\`` を追加する。
* `model.RunnerStatus`:
  ```go
  type RunnerStatus struct {
      Blocked   bool          `json:"blocked"`
      GitRepo   bool          `json:"git_repo"`
      GitDirty  bool          `json:"git_dirty"`
      Phases    []string      `json:"phases"`
      Agents    []AgentStatus `json:"agents"`
      LatestRun *RunSummary   `json:"latest_run,omitempty"`
  }

  type AgentStatus struct {
      Name       string `json:"name"`
      Available  bool   `json:"available"`
      Executable string `json:"executable,omitempty"`
      IsCustom   bool   `json:"is_custom"`
  }
  ```
* `runner.InspectStatus(ctx context.Context, root string, cfg model.ReifuConfig) model.RunnerStatus`:
  * **エラー非伝播（常にベストエフォートで Exit `0` を保証）**:
    * `Blocked`: プロジェクトルート直下に `BLOCKED_REPORT.md` が存在すれば `true`、無ければ `false`。
    * `GitRepo` / `GitDirty`: 短いタイムアウト（5 秒）付きで Git 状態を確認する。`git` コマンド不在または Git リポジトリ外なら `GitRepo: false, GitDirty: false`（エラーにしない）。Git リポジトリ内なら `StatusPorcelain` が非空のとき `GitDirty: true`。
    * `Phases`: `prompts/phases/*.md` を昇順列挙し、拡張子 `.md` を除いた名前一覧を返す。ディレクトリ不在・空・読取不可でも空スライス `[]string{}`（エラーにしない）。
    * `Agents`: 組み込み 4 エージェント（`agy`, `claude`, `codex`, `gemini`）と `cfg.Runner.Agents` のカスタムエージェントを重複排除・名前昇順で走査し、実行ファイルが見つかれば `Available: true, Executable: <abs path>`、見つからなければ `Available: false`。全エージェント未インストールでもエラーにしない。
    * `LatestRun`: `LoadRun(root, "")` が成功した場合のみ `Summary()` を格納。未実行や破損時もエラーにせず `nil`。
* **後方互換性の厳守**:
  * `StatusReport.HasIssues` および `reifu status --quiet` の終了コード判定（設定・リンクのドリフト時のみ Exit `2`）に `Runner` の内容は一切含めない。
  * `reifu status` のプレーンテキスト出力は既存フォーマットを維持する。

---

## 4. 受け入れ基準チェックリスト

### Phase 6: ランナーストリーム＆キャンセル
- [ ] `internal/model` に `AgentCommand`, `RunPlan`, `RunEventType`, `RunEvent` が定義され、`internal/runner` の既存公開型と後方互換である
- [ ] `reifu run --stream-json` が NDJSON イベント（`run_plan` / `run_started` / `phase_started` / `step_started` / `log` / `heartbeat` / `commit` / `step_completed` / `phase_completed` / `phase_failed` / `run_completed` / `run_failed`）を `stdout` にのみ出力し、同時書き込みでも JSON 行が破損しない
- [ ] `reifu run --stream-json` が `--json` と排他（Exit `1`）であり、`--dry-run --stream-json` およびプリフライト失敗時の `run_failed` 出力が検証されている
- [ ] `reifu run --cancel-on-stdin-eof` がデフォルト `false` であり、有効時に `stdin` クローズで実行中エージェントを安全に停止してチェックポイント保存＋終了イベント発行することがテストで検証されている

### Phase 7: コマンド JSON API・ダッシュボード状態・ドキュメント
- [ ] `reifu preset list --json` が `[{"name":..., "source":...}]` を出力する
- [ ] `reifu sync --json`（`--dry-run --json` 含む）が `SyncReport` を出力し、`stdout` に非 JSON 出力を混入させない
- [ ] `reifu link --json`（`--check --json` 含む）が `LinkCommandReport` を出力し、`--check` 異常時は JSON 出力後に Exit `3` で終了する
- [ ] `reifu new --json` / `reifu init --json`（`--dry-run --json` 含む）が `SetupReport` を出力する
- [ ] `reifu status --json` が `runner`（`blocked`, `git_repo`, `git_dirty`, `phases`, `agents`, `latest_run`）を返し、エージェント未検出・`prompts/phases` 不在・非 Git 環境でも Exit `0` で動作し、既存の `has_issues` / `--quiet` 判定を壊さない
- [ ] `docs/design/02_data_modeling.md`, `docs/design/03_architecture.md`, `README.md`, `HOWTO.md` が最新仕様に更新されている
- [ ] `gofmt -s -w .`, `go vet ./...`, `go test -v ./...`, `CGO_ENABLED=0 go build ./cmd/reifu` がすべて成功する
