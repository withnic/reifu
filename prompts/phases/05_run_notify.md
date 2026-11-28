# Phase 5: `reifu run` 完了・停止時のクロスプラットフォーム通知実装（`--notify` / `runner.notify`）

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/003_run_notify.md`（本機能の正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* 既存コード: `internal/model/run_state.go`, `internal/sysenv/`, `internal/runner/runner.go`, `internal/runner/steps.go`, `internal/app/run_cmd.go`, `cmd/reifu/main_test.go`

---

## 2. 今回の実装タスク

### 2.1 `internal/model` の拡張
* `internal/model/run_state.go`:
  * `NotifyMode` 型と定数 `NotifyOff = "off"`, `NotifyEnd = "end"`, `NotifyPhase = "phase"` を追加。
  * `RunnerConfig` に `Notify NotifyMode \`json:"notify,omitempty"\`` を追加し、`Validate()` で `""` / `"off"` / `"end"` / `"phase"` 以外を拒否する。
  * `RunState` に `Notify NotifyMode \`json:"notify,omitempty"\`` を追加する。

### 2.2 `internal/sysenv` のクロスプラットフォーム通知実装（ゼロ外部依存）
1. `internal/sysenv/notify.go`:
   * `Notification{Title, Message string; Success bool}` と `Notify(n Notification, tty io.Writer)` を定義する。
   * `os.Getenv("CI") != ""` または `os.Getenv("REIFU_NO_NOTIFY") != ""` のときは何もしない。
   * 制御文字（`\x00`〜`\x1f`, `\x7f`, ESC, BEL 等）をスペースへ置換・連続空白整理する `sanitizeNotificationText(s string) string` を実装し、OSC 端末エスケープ注入や制御文字混入を完全に防止する。
   * `isTerminal(w io.Writer) bool`: `w` が `*os.File` かつ `fi.Mode()&os.ModeCharDevice != 0` のときのみ true。
2. `internal/sysenv/notify_windows.go`（`//go:build windows`）:
   * `syscall.NewLazyDLL("user32.dll").NewProc("MessageBeep")` で成功時 `0x00000040`（`MB_ICONASTERISK`）、失敗時 `0x00000010`（`MB_ICONHAND`）を鳴らす（ロード失敗や呼び出しエラーは無視）。
   * `isTerminal(tty)` なら OSC 9 通知 `"\x1b]9;" + title + ": " + message + "\a"` を書き込む。
3. `internal/sysenv/notify_darwin.go`（`//go:build darwin`）:
   * `isTerminal(tty)` なら `"\a"` を書き込む。
   * `osascript` が PATH にあれば 3 秒タイムアウトの `exec.CommandContext` で、AppleScript の `on run argv` 経由（引数渡し・文字列連結によるインジェクション不可）により `display notification (item 1 of argv) with title (item 2 of argv) sound name (item 3 of argv)`（`"Glass"` / `"Basso"`）を実行する。
4. `internal/sysenv/notify_linux.go`（`//go:build linux`）:
   * `isTerminal(tty)` なら `"\a"` を書き込む。
   * `notify-send` が PATH にあれば 3 秒タイムアウトの `exec.CommandContext` で引数配列渡し（失敗時は `--urgency=critical`、`--` でタイトル・本文を区切る）で通知する。

### 2.3 `internal/runner` への組み込み
* `runner.Options` に `Notify model.NotifyMode` と `Notifier func(sysenv.Notification)` を追加する。
* `prepare()` で `e.run.Notify` を解決する（優先順位: CLI 指定 `e.Notify` > resume 時の保存値 `e.run.Notify` > `e.Config.Runner.Notify` > `model.NotifyOff`）。不正値なら Exit `1` エラー。
* `--dry-run` 時は絶対に通知を発火しない。
* `execute()` のフェーズループ:
  * `e.run.Notify == model.NotifyPhase` のとき、最終フェーズ以外のフェーズ完了時に `Success: true` で 1 回通知する（最終フェーズは終了通知と重複させない）。
  * `execute()` 完了時（`e.run.Notify == model.NotifyEnd || e.run.Notify == model.NotifyPhase`）:
    * 全成功時: `Success: true`（例: `Title: "reifu run"`, `Message: fmt.Sprintf("%s: completed (%d/%d)", projectLabel, summary.Completed, summary.Total)`）
    * 失敗・`BLOCKED_REPORT.md`・タイムアウト・キャンセル時: `Success: false`（例: `Title: "reifu run"`, `Message: fmt.Sprintf("%s: %s at %s (%d/%d completed)", projectLabel, status, phaseName, summary.Completed, summary.Total)`）
  * 通知は `e.Notifier`（nil なら `sysenv.Notify(n, e.Progress)`）を呼び、ベストエフォートでエラーにしない。

### 2.4 `internal/app/run_cmd.go` とドキュメント更新
* `internal/app/run_cmd.go`:
  * `IsBoolFlag() bool { return true }` を実装した `notifyFlag` を使い、`--notify`（`end`）、`--notify=end`、`--notify=phase`、`--notify=off`、`--notify=true`、`--notify=false` を受け付ける（不正値はパースエラー Exit `1`）。
  * `--json` 併用時も `stdout` に一切混入しないこと（`Progress` は常に `c.Stderr`）。
* `HOWTO.md` / `README.md`:
  * `reifu run --notify` / `--notify=phase` および `.reifu.jsonc` の `runner.notify` の説明を簡潔に追記する。
* `docs/specs/003_run_notify.md`:
  * 受け入れ基準チェックリストをすべて `[x]` に更新する。

---

## 3. テスト要件

1. **`internal/model/model_test.go`**:
   * `RunnerConfig.Notify` の正常値（`""`, `off`, `end`, `phase`）と異常値のバリデーションテスト。
2. **`internal/sysenv/detect_test.go`**（または `notify_test.go`）:
   * `sanitizeNotificationText` の制御文字・ESC・BEL・改行除去テスト。
   * `CI=1` や `REIFU_NO_NOTIFY=1` で安全に No-op になることの検証。
3. **`internal/runner/runner_test.go`**:
   * DI された `Notifier` モックを使い、以下を厳密に検証:
     * `off`（デフォルト）で通知 0 回
     * `end` で 2 フェーズ完走時に通知 1 回（`Success: true`）、途中失敗／`BLOCKED_REPORT.md` 時に通知 1 回（`Success: false`）
     * `phase` で 2 フェーズ完走時に通知 2 回（1 個目完了＋最終完了、最終フェーズで二重発火しないこと）
     * `--dry-run` で通知 0 回
     * `--resume` で保存済み `notify` 引き継ぎ、および `--notify=off` で一時上書きできること
4. **`cmd/reifu/main_test.go`**:
   * `REIFU_NO_NOTIFY=1` をセットした上で、`reifu run --notify --dry-run --json`、`reifu run --notify=phase --phase 01_first --no-review --no-commit --json`（`stdout` が厳密な単一 JSON であること）、`reifu run --notify=invalid`（Exit `1`）を検証。

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること（Linux / macOS 向けクロスビルドまたは `go vet` も含めて確認推奨）:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（一時バイナリを残さないこと）
* `docs/specs/003_run_notify.md` の受け入れ基準チェックリストがすべて満たされていること。
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テスト改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・残課題（あれば）を簡潔に報告すること。
