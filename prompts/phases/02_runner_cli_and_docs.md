# Phase 2: `reifu run` / `reifu logs` CLI 接続・E2E テスト・テンプレート＆ドキュメント更新

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/001_phase_runner.md`（正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* Phase 1 で実装済みの `internal/model`、`internal/fsx`、`internal/runner`

---

## 2. 今回の実装タスク

### 2.1 `reifu run` サブコマンド（`internal/app/run_cmd.go`）
* `reifu run [options] [dir]` を実装し、`internal/app/app.go` のコマンドディスパッチ・`usage`・`help` に追加する。
* 対応フラグ:
  * `--implementer <agent>` / `--reviewer <agent>`
  * `--phase <name>` / `--from <name>`
  * `--no-review` / `--no-commit` / `--allow-dirty` / `--resume`
  * `--timeout <duration>`（デフォルト `30m`、`0` で無制限）
  * `--dry-run` / `--json`
* `c.rejectHomeDir(root)` を必ず実行し、ホームディレクトリ直下を無条件拒否する。
* `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`（Windows 互換シグナル含む）で Ctrl+C 中断を子プロセスへ伝播し、途中状態を保存した上で終了する。
* `--json` 指定時は `stdout` に単一 JSON（実行計画または最終 `RunState`）のみを出力し、進捗バナー・ハートビート・エージェント出力・警告はすべて `stderr` へ出力する。

### 2.2 `reifu logs` サブコマンド（`internal/app/logs_cmd.go`）
* 読み取り専用の `reifu logs [options] [dir]` を実装し、`internal/app/app.go` のコマンドディスパッチ・`usage`・`help` に追加する。
* 対応フラグ:
  * `--list`
  * `--run <id>`
  * `--phase <name>`
  * `--review`
  * `-f` / `--follow`（両フラグ名を登録し `--json` との併用はエラー）
  * `--json`
* `c.rejectHomeDir(root)` を実行し、`reifu logs` 内でファイルの書き込み・ロック取得・外部プロセス起動を一切行わないこと。
* `--follow` / `-f` は `signal.NotifyContext` で Ctrl+C によるクリーン終了に対応する。

### 2.3 テンプレート・`.gitignore`・ドキュメント更新
1. **`internal/template/embedded/common/gitignore.tmpl`** および **本リポジトリの `.gitignore`**:
   * `.reifu/` と `.reifu.run.json` を追加する。
2. **`internal/template/embedded/prompts/README.md.tmpl`** および **本リポジトリの `prompts/README.md`**:
   * `reifu run --implementer codex --reviewer claude`（および `agy` 併用例）、`reifu run --resume`、`reifu logs` / `reifu logs --review` / `reifu logs -f` の使い方を追加する（既存の単発コマンド例も整理して整合させる）。
3. **`README.md`** および **`HOWTO.md`**:
   * コマンド一覧に `reifu run [dir]` と `reifu logs [dir]` を追加する。
   * `HOWTO.md` に `reifu run` / `reifu logs` の実践ガイド（4 エージェント `claude` / `codex` / `gemini` / `agy` 指定、`.reifu.jsonc` の `runner` 設定例、中断再開、レビュー履歴確認、安全ガード）を追記する。
4. **`docs/specs/001_phase_runner.md`**:
   * 実装・検証が完了した受け入れ基準チェックリスト（`- [x]`）を最新状態に更新する。

---

### 2.4 Phase 1 レビューからの引き継ぎ
* `runner.Run` は `--implementer` も `runner.implementer` もないとき `unknown runner agent ""` を返す。CLI 側で先に「`--implementer` か `runner.implementer` を指定してください」というバリデーションエラー（Exit `1`）にすること（`--resume` 時は `.reifu.run.json` の値を使うので不要）。
* `--resume` は `.reifu.run.json` に記録した `no_review` / `no_commit` を使い、CLI の `--no-review` / `--no-commit` を無視する。同時指定されたら `stderr` に警告するか、エラーにすること。
* 失敗ステップの再開では作業ツリーが dirty なのが普通なので、Exit `1` で拒否したときの案内に「変更を確認して `--resume --allow-dirty`」を含めること（`docs/design/03_architecture.md` 4.6）。
* Ctrl+C（親 context のキャンセル）も `failed` として保存され Exit `2` になる。`signal.NotifyContext` から渡すだけでよい。
* 実装エージェントが自分で `git commit` した場合、`implement_commit` は空になり、レビューに渡る差分サマリーも空になる（`reifu run` は自分のコミットしか記録しない）。HOWTO に「フェーズプロンプトでエージェントにコミットさせない」旨を書くこと。

## 3. テスト要件

1. **`cmd/reifu/main_test.go`**（CLI E2E テスト、すべて `t.TempDir()` 完結・実 AI API 非依存）:
   * `.reifu.jsonc` の `runner.agents` にテストバイナリ自身のヘルパープロセス（モックエージェント）を設定し、CLI 経由で以下を一気通貫検証する:
     * `reifu run --dry-run --json` の出力が厳密な単一 JSON であり、ファイル変更・コミットが起きないこと
     * 未コミット変更がある状態での `reifu run` 拒否（Exit `1`）と、`--allow-dirty` での許可
     * `reifu run` のフル実行（2 フェーズ: 実装→コミット→レビュー→コミット）と、Git 履歴に `.reifu/`・`.reifu.run.json` が一切コミットされていないことの検証
     * `reifu logs`（進捗サマリー）、`reifu logs --list`、`reifu logs --review`、`reifu logs --phase 01_first`、`reifu logs --json` の出力検証
     * レビュー失敗または `BLOCKED_REPORT.md` 発生での安全停止（Exit `2`）→ 原因解消後の `reifu run --resume` による途中再開と完走
     * ホームディレクトリ直下での `reifu run` / `reifu logs` 拒否、`reifu logs --run ../escape` 拒否、`reifu help run` / `reifu help logs` の正常終了
2. **`internal/template/engine_test.go`**:
   * 生成される `.gitignore` に `.reifu/` と `.reifu.run.json` が含まれることの検証（既存テストが壊れないことも確認）。

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（ビルド後に一時バイナリが残らないようにする）
* `docs/specs/001_phase_runner.md` の受け入れ基準チェックリストがすべて満たされていること。
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テストの改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・残課題（あれば）を簡潔に報告すること。
