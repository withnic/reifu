# 003_run_notify — `reifu run` 完了・停止時のクロスプラットフォーム通知（`--notify` / `runner.notify`）

## 1. 背景・目的

`reifu run` は複数フェーズの実装・レビューを放置で自動実行できるが、完了したタイミングやエラー・`BLOCKED_REPORT.md` で途中停止したタイミングに人間がすぐ気づけるようにしたい。
エージェント個別のグローバル通知設定に依存せず、Claude / Codex / Gemini / Antigravity のどの組み合わせでも一貫して通知音・デスクトップ通知を出せる機能を、Go 標準ライブラリのみ（ゼロ外部依存）で `reifu run` に追加する。

---

## 2. スコープ

### In-Scope
* `reifu run` のフラグ **`--notify`**（`--notify` で `"end"`、`--notify=end` / `--notify=phase` / `--notify=off` 指定可）
* `.reifu.jsonc` の任意フィールド **`runner.notify`**（`""` / `"off"` / `"end"` / `"phase"`、既存設定と 100% 後方互換）
* クロスプラットフォームの通知実装（`internal/sysenv` に OS 依存を完全隔離、ゼロ外部依存）：
  * **Windows**: Win32 `user32.dll` `MessageBeep`（成功音 / エラー音の鳴り分け）＋ 端末 TTY 向け OSC 9 通知（Windows Terminal 等）
  * **macOS**: 端末ベル `\a` ＋ `osascript` による通知＆システム音（`on run argv` 経由でインジェクション完全防止）
  * **Linux / WSL2**: 端末ベル `\a` ＋ `notify-send`（存在時のみ）
* 通知タイミング:
  * `end`: ラン終了時（全フェーズ完了 ＝ 成功通知、異常終了 / タイムアウト / `BLOCKED_REPORT.md` / 中断 ＝ 失敗通知）
  * `phase`: 各フェーズ完了時（最終フェーズはラン終了通知と重複させない）＋ ラン終了時
  * `off`（デフォルト）: 通知なし
* CI 環境（`CI` 非空）や `REIFU_NO_NOTIFY` 非空時の自動抑止、および `--dry-run` 時の無通知
* テスト容易性のための `runner.Options.Notifier` DI フック

### Out-of-Scope
* 外部通知サービス（Slack / Discord / Webhook 等のネットワーク通信）
* カスタム音声ファイルの読み込みや外部音声ライブラリ依存

---

## 3. 詳細仕様

### 3.1 モデル＆設定（`internal/model`）
* `NotifyMode` 型と定数を追加:
  * `NotifyOff NotifyMode = "off"`
  * `NotifyEnd NotifyMode = "end"`
  * `NotifyPhase NotifyMode = "phase"`
* `RunnerConfig` に `Notify NotifyMode \`json:"notify,omitempty"\`` を追加。
* `RunnerConfig.Validate()` で `""`, `"off"`, `"end"`, `"phase"` 以外をバリデーションエラーとする。
* `RunState` に `Notify model.NotifyMode \`json:"notify,omitempty"\`` を記録し、`--resume` 時は CLI の `--notify` が指定されていれば CLI を優先、未指定時は保存済みまたは `runner.notify` を使用する。

### 3.2 CLI フラグ（`internal/app/run_cmd.go`）
* `--notify` は `IsBoolFlag() bool { return true }` を実装したカスタム `flag.Value` とする：
  * 未指定: `.reifu.jsonc` の `runner.notify`（未設定なら `off`）を使用
  * `--notify`（値なし）または `--notify=true`: `"end"`
  * `--notify=false` または `--notify=off`: `"off"`（設定ファイルの `runner.notify` を一時的に無効化可能）
  * `--notify=end` / `--notify=phase`: それぞれのモード
  * それ以外の値: バリデーションエラー（Exit `1`）
* `--json` 併用時も `stdout` にベルやエスケープシーケンスを一切混入させない（端末出力は必ず `c.Stderr` のみ）。

### 3.3 通知実装（`internal/sysenv`）
* 共通型 `sysenv.Notification`:
  ```go
  type Notification struct {
      Title   string
      Message string
      Success bool
  }
  ```
* `sysenv.Notify(n Notification, tty io.Writer)`:
  * `os.Getenv("CI") != ""` または `os.Getenv("REIFU_NO_NOTIFY") != ""` の場合は即 return（無音 No-op）。
  * `Title` / `Message` から制御文字（`\x00`〜`\x1f`, `\x7f`, ESC 等）を空白へサニタイズし、端末エスケープインジェクション（OSC 注入等）を構造的に防止する。
  * **Windows (`notify_windows.go`)**:
    * `user32.dll` の `MessageBeep` を `MB_ICONASTERISK (0x40)`（成功）または `MB_ICONHAND (0x10)`（失敗）で呼び出す。
    * `tty` が `*os.File` かつ `os.ModeCharDevice` の場合、OSC 9 シーケンス `"\x1b]9;" + title + ": " + message + "\a"` を書き込む。
  * **macOS (`notify_darwin.go`)**:
    * `tty` が `*os.File` かつ `os.ModeCharDevice` なら `"\a"` を書き込む。
    * `osascript` が PATH にあれば 3 秒タイムアウトで `on run argv` スクリプトへ `message`・`title`・sound名（`"Glass"` / `"Basso"`）を引数渡しして通知する（文字列埋め込みによる AppleScript インジェクション禁止）。
  * **Linux / WSL2 (`notify_linux.go`)**:
    * `tty` が `*os.File` かつ `os.ModeCharDevice` なら `"\a"` を書き込む。
    * `notify-send` が PATH にあれば 3 秒タイムアウトで引数配列経由（失敗時は `--urgency=critical`、`--` 区切り）で通知する。
* 通知の失敗（`osascript` / `notify-send` 未存在やタイムアウト等）はラン全体のエラーにしない（ベストエフォート）。

### 3.4 `internal/runner` での通知発火ルール
* `runner.Options` に `Notify model.NotifyMode` と `Notifier func(sysenv.Notification)` を追加（`Notifier == nil` 時は `sysenv.Notify(n, opts.Progress)` を使用）。
* `--dry-run` 時は発火しない。
* `Notify == "phase"` の場合、最終フェーズ以外のフェーズが `completed` になったタイミングで `Success: true` の通知を 1 回発火する。
* `Notify == "end"` または `"phase"` の場合、実行ループ終了時に以下を 1 回発火する：
  * 全フェーズ成功時: `Success: true`（例: `reifu run` / `completed 2/2 phases`）
  * 失敗・`BLOCKED_REPORT.md`・タイムアウト・キャンセル時: `Success: false`（例: `reifu run` / `failed at 03_skills_core (0/2 completed)`）

---

## 4. 受け入れ基準チェックリスト

- [ ] `internal/model` に `NotifyMode`（`off` / `end` / `phase`）、`RunnerConfig.Notify`、`RunState.Notify` とバリデーションが追加され、既存 `.reifu.jsonc` と 100% 後方互換である
- [ ] `internal/sysenv` にクロスプラットフォーム通知（Windows `MessageBeep` + OSC 9、macOS `osascript` + ベル、Linux `notify-send` + ベル）と制御文字サニタイズ、`CI`/`REIFU_NO_NOTIFY` 抑止が実装されている
- [ ] `internal/runner` が `end` / `phase` / `off` に応じて通知を発火し、`--dry-run` では発火せず、最終フェーズで二重通知しないことがテストで検証されている
- [ ] `internal/app/run_cmd.go` が `--notify` / `--notify=phase` / `--notify=end` / `--notify=off` を受け付け、`--json` 併用時も `stdout` を汚染しないことが E2E テストで検証されている
- [ ] `README.md`、`HOWTO.md`、`docs/design/02_data_modeling.md`、`docs/design/03_architecture.md` に `--notify` / `runner.notify` の説明が反映されている
- [ ] `gofmt -s -w .`、`go vet ./...`、`go test -v ./...`、`CGO_ENABLED=0 go build ./cmd/reifu` がすべて成功する
