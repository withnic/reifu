# Phase 6: `reifu run` リアルタイム NDJSON ストリーム＆`stdin` EOF キャンセル実装

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/004_companion_api.md`（本機能の正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* 既存コード: `internal/model/run_state.go`, `internal/runner/agents.go`, `internal/runner/runner.go`, `internal/runner/steps.go`, `internal/app/run_cmd.go`, `cmd/reifu/main_test.go`

---

## 2. 今回の実装タスク

本フェーズでは `docs/specs/004_companion_api.md` の **Phase 6 範囲（`--stream-json` および `--cancel-on-stdin-eof`）** のみを実装・検証する（Phase 7 の `preset`/`sync`/`link`/`new`/`init`/`status` JSON 拡張および一般ドキュメント更新は次フェーズに委ねる）。

### 2.1 `internal/model` のイベント・計画型定義
* `internal/model/run_state.go` に以下を追加する:
  * `AgentCommand`（`Name string`, `Argv []string`, `IsCustom bool`）および `RunPlan`（`Implementer AgentCommand`, `Reviewer *AgentCommand`, `State RunState`）
  * `RunEventType` 定数:
    * `EventRunPlan = "run_plan"`
    * `EventRunStarted = "run_started"`
    * `EventPhaseStarted = "phase_started"`
    * `EventStepStarted = "step_started"`
    * `EventLog = "log"`
    * `EventHeartbeat = "heartbeat"`
    * `EventCommit = "commit"`
    * `EventStepCompleted = "step_completed"`
    * `EventPhaseCompleted = "phase_completed"`
    * `EventPhaseFailed = "phase_failed"`
    * `EventRunCompleted = "run_completed"`
    * `EventRunFailed = "run_failed"`
  * `RunEvent` 構造体（`SchemaVersion int`, `Type RunEventType`, `Timestamp time.Time`, `RunID`, `Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Agent`, `Commit`, `Path`, `Text`, `ElapsedMS`, `Status`, `Error`, `Warning`, `Plan *RunPlan`, `State *RunState`）
* `internal/runner/agents.go` / `internal/runner/runner.go` で `type AgentCommand = model.AgentCommand`, `type Plan = model.RunPlan` の型エイリアスを用い、既存の外部参照・テストと 100% 互換を維持する。

### 2.2 `internal/runner` へのイベントストリーム組み込み
* `runner.Options` に `EventSink func(model.RunEvent)` を追加する。
* `execution` 内に `sync.Mutex` 付きの `emit(ev model.RunEvent)` ヘルパーを用意し、必ず `ev.SchemaVersion = 1`、`ev.Timestamp = time.Now().UTC()`、`ev.RunID`（未設定時 `e.run.RunID`）を補完して `e.EventSink` を直列呼び出しする。
* `--dry-run` 時: `EventSink != nil` なら `EventRunPlan`（`Plan: plan`）を 1 回発行する。
* `execute()` / `executePhase()` / `runPhase()` / `agent()` の各ポイントでイベントを発行する:
  * `execute()` 開始（初期 `e.save()` 成功後）: `EventRunStarted`（`State` のディープコピーまたはスナップショット）
  * 各フェーズ開始: `EventPhaseStarted`（1-based `PhaseIndex`, `PhaseTotal`, `Phase`, `Step`）
  * `agent()` 開始: `EventStepStarted`（`Phase`, `PhaseIndex`, `PhaseTotal`, `Step`, `Agent`, `Path`, `a.IsCustom` 時は `Warning`）
  * `agentStream.writeText()`: UTF-8 / LF 正規化済みの非空チャンクが確定するたびに `EventLog`（`Text`）を発行する。
    * `EventSink != nil` のときはログファイル `log` には書きつつ `e.Progress` へは重複出力しない。
    * `EventSink == nil` のときは従来通り `io.MultiWriter(log, e.Progress)` で完全に同じ挙動を保つ。
  * ハートビート発火時: `EventHeartbeat`（`ElapsedMS`）を発行（`EventSink == nil` 時は従来通り `e.Progress` へテキスト出力）。
  * `implement` / `review` 後にコミット SHA が得られたとき: `EventCommit`（`Step`, `Commit`）を発行。
  * `implement` / `review` ステップ完了時: `EventStepCompleted`（`Step`, `Agent`, `Commit`, `Path`, `ElapsedMS`）を発行。
  * フェーズ完了＆保存後: `EventPhaseCompleted`（`Status: model.PhaseCompleted`, `ElapsedMS`）を発行。
  * フェーズ失敗時（`e.fail` 内）: `EventPhaseFailed`（`Status: phase.Status`, `Error`, `ElapsedMS`）を発行。
  * `execute()` 終了時: 成功なら `EventRunCompleted`（`Status: model.PhaseCompleted`, `State`）、失敗なら `EventRunFailed`（`Status: summary.Status` [blocked でなければ `failed`], `Error`, `State`）を発行する。
* `State` をイベントに含める際は、`Phases` スライスをクローンしたスナップショットを渡し、後続フェーズの変更でイベント内容が書き換わらないようにする。

### 2.3 `internal/app/run_cmd.go` の `--stream-json` と `--cancel-on-stdin-eof`
* フラグ追加:
  * `--stream-json`: NDJSON イベントストリームを `stdout` に出力する。`--json` と同時指定された場合はエラー（Exit `1`）。
  * `--cancel-on-stdin-eof`: `stdin` の EOF / クローズを検知したら実行中のランをキャンセルする（デフォルト `false`）。
* `--stream-json` の配線:
  * `json.NewEncoder(c.Stdout)` に `SetEscapeHTML(false)` を設定し、`sync.Mutex` 保護した `EventSink` を `opts.EventSink` に渡す。
  * `opts.Progress = io.Discard` とし、プレーンテキスト進捗やバナーが `stderr` にも `stdout` にも漏れないようにする（`--notify` の端末出力がある場合は従来通り `c.Stderr` を渡すか、`opts.Progress` とは分離して `stdout` を絶対に汚染しないこと）。
  * `runner.Run` が `EventRunPlan` / `EventRunCompleted` / `EventRunFailed` のいずれも発行せずにエラー返却した場合（プリフライト失敗・ロック失敗・`BLOCKED_REPORT.md` 存在・missing implementer 等）は、CLI 側でフォールバックの `EventRunFailed` を 1 行だけ `stdout` に出力する（二重出力しないこと）。
  * `--stream-json` 時は、通常の `printRunState` や単一 JSON 出力を行わない。
* `--cancel-on-stdin-eof` の配線:
  * `ctx, cancel := context.WithCancel(sigCtx)` を作り、`*cancelOnStdinEOF && !opts.DryRun && c.Stdin != nil` の場合に goroutine で `c.Stdin` を小バッファで読み続け、EOF または読み込みエラー時に `cancel()` を呼ぶ。
  * `defer cancel()` を必ず呼び、ラン完了後のリソース解放を確実に行う。

### 2.4 チェックリスト更新
* `docs/specs/004_companion_api.md` の **Phase 6 チェックリスト項目** を `[x]` に更新する（Phase 7 項目は未完了 `[ ]` のまま残す）。

---

## 3. テスト要件

1. **`internal/runner/runner_test.go`**:
   * `EventSink` を用いて、2 フェーズ完走時（実装＋レビュー＋コミット）のイベント順序（`run_started` → `phase_started` → `step_started` → `log` → `commit` → `step_completed` → ... → `phase_completed` → `run_completed`）および `SchemaVersion == 1`、`PhaseIndex`/`PhaseTotal`、スナップショット不変性を検証する。
   * 短い `HeartbeatInterval` と遅延モックエージェントで `heartbeat` イベントが発行され、同時書き込みでもデータ競合・イベント欠損がないことを検証する。
   * フェーズ失敗時・`BLOCKED_REPORT.md` 作成時に `phase_failed` と `run_failed`（`Status == blocked` / `failed`）が発行されることを検証する。
   * `--dry-run` で `run_plan` が 1 回だけ発行され、エージェント起動・ファイル書き込みが起きないことを検証する。
2. **`cmd/reifu/main_test.go`**:
   * `reifu run --stream-json --json` が Exit `1` エラーになることを検証する。
   * `reifu run --stream-json --dry-run` の `stdout` が厳密な 1 行 NDJSON（`type == "run_plan"`）であることを検証する。
   * `reifu run --stream-json --no-review --no-commit` の `stdout` 全行が有効な JSON であり、先頭 `run_started`、途中に `log`、末尾 `run_completed` が含まれ、非 JSON テキストが一切混入しないことを検証する。
   * プリフライト失敗（例: `--implementer` 未指定または `BLOCKED_REPORT.md` 存在）時に `--stream-json` が 1 行の `run_failed` NDJSON を `stdout` に出して非 0 終了することを検証する。
   * `io.Pipe()` を `CLI.Stdin` に接続し、待機モックエージェント実行中に書き込み端を閉じたとき `--cancel-on-stdin-eof` で安全にキャンセルされ（子プロセス終了・チェックポイント保存）、`--stream-json` 併用時に `run_failed` が出力されることを検証する。
   * `--cancel-on-stdin-eof` 未指定時は `CLI.Stdin` が即 EOF でもキャンセルされずに完走することを検証する。

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（一時バイナリを残さないこと）
* `docs/specs/004_companion_api.md` の Phase 6 受け入れ基準チェックリストがすべて満たされていること。
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テスト改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・残課題（あれば）を簡潔に報告すること。
