# Phase 8: 完了済みフェーズを引き継いで残りだけを実行する（`--skip-completed`・`runner.pending_phases`）

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/005_run_skip_completed.md`（本機能の正式仕様書・受け入れ基準チェックリスト。すべて本フェーズの担当）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* 既存コード: `internal/app/run_cmd.go`、`internal/runner/runner.go`（`Run` の排他検査・`prepare`・`discover`・`preflight`）、`internal/runner/steps.go`（`resumeStep`）、`internal/runner/status.go`（`InspectStatus`）、`internal/model/run_state.go`、`internal/model/state.go`（`RunnerStatus`）、`cmd/reifu/main_test.go`

## 2. 今回の実装タスク

1. `internal/app/run_cmd.go` に `--skip-completed` を足し、`runner.Options` に渡す。
2. `internal/runner`: `Run` の排他検査に `--skip-completed` を足す。`prepare` の新しいランの作成で、最新のランの状態から完了済みのフェーズの記録を写す（仕様 §3.1）。未完了のフェーズがない場合の終わり方、`--stream-json` で写したフェーズのイベントを出さないこと、`--dry-run` の計画を仕様どおりにする。
3. `internal/model/state.go` の `RunnerStatus` に `PendingPhases []string \`json:"pending_phases"\`` を足し、`InspectStatus` で埋める（仕様 §3.2）。
4. `docs/design/02`（フラグ一覧・コンパニオン API のレスポンス・`RunnerStatus`）と `03`、`README.md`・`HOWTO.md` を更新する。
5. `docs/specs/005_run_skip_completed.md` の済んだ項目を `- [x]` にする。

## 3. テスト要件

* 仕様 §4 の受け入れ基準にあるテストをすべて足す。偽のエージェント（既存のテストと同じ方法）を使い、本物の AI エージェントを起動しない。
* Git が要るテストは一時ディレクトリで本物の `git` を使い、グローバル設定を変えない（既存のテストと同じ方法）。

## 4. 完了条件

* `AGENTS.md` の検証コマンド（`go vet` / `go test` / リンターなど、`reifu hook verify` の各ステップ）がすべて通ること。
* 失敗時は AGENTS.md の 3 段階エスカレーション修復ループ（各段階 3 回、計 9 回まで）で直す。9 回で解決しなければ `BLOCKED_REPORT.md` に試した内容と根本原因をまとめて終了する。
* テストのアサーションの緩和・削除・スキップで通したことにしない。
* 一時ファイル・デバッグ出力を残さない。
* コミットは実行環境が行うので、このフェーズでは自分でコミットしない。

## 5. 完了時の出力

* 変更・追加したファイルの一覧
* 各検証コマンドの結果
* コンパニオン（shikiban）への引き継ぎ: `--skip-completed` の使い方、`runner.pending_phases` の意味
