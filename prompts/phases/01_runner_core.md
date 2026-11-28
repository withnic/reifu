# Phase 1: フェーズ実行・レビュー自動化オーケストレーター基盤実装（`internal/model`, `internal/fsx`, `internal/runner`）

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/001_phase_runner.md`（本機能の正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md`
* `docs/design/02_data_modeling.md`
* `docs/design/03_architecture.md`

---

## 2. 今回の実装タスク

### 2.1 `internal/model` の拡張
1. `internal/model/config.go`:
   * `ReifuConfig` に任意の `Runner RunnerConfig \`json:"runner,omitempty"\`` を追加（既存 `.reifu.jsonc` との 100% 後方互換を維持）。
   * `RunnerConfig` は `Implementer string`、`Reviewer string`、`Agents map[string][]string` を持つ。
   * `Validate()` に以下を追加:
     * `runner.implementer` / `runner.reviewer`: 非空時は `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` を満たすこと。
     * `runner.agents`: 各キーが上記パターンを満たし、コマンド配列が空でなく先頭要素が空白のみでなく、全要素に制御文字（`\r`, `\n`, `\x00` 等）を含まないこと。
2. `internal/model/run_state.go`（または `state.go` 拡張）:
   * `RunState`、`PhaseRunRecord`、`PhaseStatus`（`pending` / `implementing` / `implemented` / `reviewing` / `completed` / `failed` / `blocked`）、`RunSummary`（`reifu logs --list` 用）などの構造体を定義する。
   * `ValidateRunID(id string) error` を用意し、`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` に一致しない ID（パストラバーサル等）を拒否する。

### 2.2 `internal/fsx` のロック汎用化
* `internal/fsx/lock.go`:
  * 既存の `WithFileLock(root, run)`（`.reifu.lock` 使用）の挙動を一切壊さず、プロジェクト内任意パスの排他ロックを取得できる `WithNamedFileLock(root, target string, run func() error) error`（または同等の API）を追加する。
  * `reifu run` は `.reifu/run.lock` を使用し、既存 `.reifu.lock`（`sync` / `link` / `init` 用）と一切競合しないこと。
  * 長時間実行される `reifu run` のロックが 5 秒経過で他プロセスに誤回収されないよう、OS ロック（`tryLock`）で生存判定される既存仕組みを確実に維持・テストすること。

### 2.3 `internal/runner` パッケージの新規作成
1. **`internal/runner/agents.go`（エージェント起動解決）**:
   * 内蔵 4 エージェントのデフォルト起動コマンド配列（シェル非経由）を解決する:
     * `claude` -> `["claude", "-p"]`
     * `codex` -> `["codex", "exec", "-s", string(cfg.Sandbox.Mode), "-"]`
     * `gemini` -> `["gemini", "-p", ""]`
     * `agy` / `antigravity`（正規化名 `agy`） -> `["agy", "--print", "--mode", "auto"]`
   * `.reifu.jsonc` の `runner.agents` に一致するキーがあればそちらを優先し、`IsCustom: true` として返す（`antigravity` 指定時は `runner.agents["antigravity"]` -> `runner.agents["agy"]` -> 内蔵デフォルトの順で解決）。
   * カスタムコマンド使用時は `stderr` に実行コマンドを明示警告する。
   * 実行前（`--dry-run` 除く）に `exec.LookPath(argv[0])` でバイナリ存在を確認する。
2. **`internal/runner/git.go`（ローカル Git 操作）**:
   * すべて `exec.CommandContext(ctx, "git", ...)` でシェル非経由実行し、`git push` / `git reset --hard` は絶対に呼ばない。
   * `IsGitRepo(ctx, root)`、`StatusPorcelain(ctx, root)`、`HeadShortSHA(ctx, root)`、`DiffStat(ctx, root, baseCommit)`、`CommitAll(ctx, root, message)` を実装する。
   * `CommitAll` のステージングは必ず除外 pathspec 付きで実行し、`.gitignore` 未更新リポジトリでもランタイムファイルを絶対に誤コミットしないこと:
     `git add -A -- . :!.reifu :!.reifu.run.json :!.reifu.state.json :!.reifu.lock :!.reifu.tmp.* :!BLOCKED_REPORT.md`
   * ステージング後にコミット対象差分が空（`git diff --cached --quiet` が 0）ならコミットをスキップして空 SHA・nil エラーを返す。
3. **`internal/runner/runner.go`（オーケストレーター本体）**:
   * `prompts/phases/*.md` を辞書順ソートで検出し、`--phase` / `--from` の絞り込み・`--resume` 時の `.reifu.run.json` 復元を行う。全パスを `fsx.ValidateWithinRoot` で検証する（シンボリックリンク脱出拒否）。
   * 開始時に `BLOCKED_REPORT.md` が既に存在する場合は即 Exit `2` 相当エラー（`model.ExitDrift`）で停止。
   * 進捗表示:
     * 各ステップ開始・完了バナーを `Options.Progress`（通常 `stderr`）へ即時出力。
     * 子プロセスの `stdout` / `stderr` を `io.MultiWriter` で進捗出力と追記ログファイル（`.reifu/runs/<run-id>/<stem>_implement.log`）へリアルタイム同時出力。
     * 無出力時間が `HeartbeatInterval`（デフォルト 30 秒、テストで短縮注入可能）経過するごとに経過時間付きハートビート行を `Options.Progress` へ出力。
   * 各フェーズのループ:
     * `implement`: フェーズ本文を `stdin` で渡し実行（`--timeout` 付き `context.WithTimeout`）。終了後に `BLOCKED_REPORT.md` 存在または非ゼロ終了なら状態を `blocked`/`failed` で保存して Exit `2` 停止。`--no-commit` でなければ `feat(phase): complete <stem>` でコミット。
     * `review`（`--no-review` 未指定時）: `prompts/review.md` の先頭に対象フェーズ情報（ファイルパス・実装担当・実装コミット SHA・差分サマリー）を付けたプロンプトを `stdin` で渡して新規セッション実行。出力全文と変更差分サマリーを `.reifu/runs/<run-id>/<stem>_review.md` に UTF-8/LF で保存。異常・`BLOCKED_REPORT.md` 時は安全停止。`--no-commit` でなければ `fix(review): address review for <stem>` でコミット。
     * 各遷移時に `.reifu.run.json` と `.reifu/runs/<run-id>/run.json` を `fsx.WriteFileAtomic` で保存。完了時に `.reifu/runs/<run-id>/summary.md` を生成（`summary.md` の書き込み失敗は警告のみ出してランを落とさない）。
   * `--dry-run`: ロック取得や書き込み・子プロセス起動・Git 変更を行わず、実行予定フェーズ・コマンド・ログパスを構造化結果として返す。
4. **`internal/runner/logs.go`（`reifu logs` 用読み取り専用リーダー）**:
   * `ListRuns(root)`（`.reifu/runs/*/run.json` を新しい順で一覧取得）、`LoadRun(root, runID)`（空なら `.reifu.run.json` または最新ラン）、`ReadPhaseLog(root, run, phaseName)`、`ReadReviews(root, run, phaseName)` を実装。
   * `FollowLog(ctx, root, run, phaseName, pollInterval, out)`: 進行中ログをポーリングで読み取り差分を `out` へ出力する。毎回読み取り直後にファイルハンドルを閉じる方式とし、Windows でも `reifu run` 側の書き込みを一切ロック競合させない。ランが完了状態（`completed`/`failed`/`blocked`）かつ残出力を読み切ったら正常終了する。

---

## 3. テスト要件

1. **`internal/model/model_test.go`**:
   * `RunnerConfig` の正常系・異常系（不正エージェント名、空コマンド配列、制御文字混入）および `ValidateRunID`（パストラバーサル拒否）の単体テスト。
2. **`internal/fsx/fsx_test.go`**:
   * `WithNamedFileLock` の排他動作と `.reifu.lock` と `.reifu/run.lock` が同時に干渉せず取得できることのテスト。
3. **`internal/runner/runner_test.go`**（実 AI API 非依存・クロスプラットフォーム）:
   * テスト内で Go テストバイナリ自身（`os.Args[0]` + `-test.run=TestHelperAgentProcess` + 環境変数）をモックエージェントとして使うことで、Windows / macOS / Linux すべてでシェル依存なく高速・決定的にテストする。
   * 網羅すべきケース:
     * 4 エージェント（`claude` / `codex` / `gemini` / `agy` / `antigravity`）のコマンド解決＋カスタム上書き＋警告出力
     * 複数フェーズ（`01_alpha.md`, `02_beta.md`）のフルループ（実装→コミット→レビュー→コミット）と `.reifu/runs/<run-id>/`（ログ・レビュー Markdown・`summary.md`）の生成内容検証
     * 実装成功・レビュー失敗からの `--resume` で「実装をやり直さずレビューから再開」して完了できること
     * `BLOCKED_REPORT.md` 作成時の即停止（Exit `2`、`status: blocked`）
     * 無出力時のハートビート出力確認（短い `HeartbeatInterval` を注入）
     * `--no-review`、`--no-commit`、`--phase`、`--from`、`--dry-run`、`--timeout`
     * `.gitignore` が空のリポジトリでも `.reifu/` や `.reifu.run.json` が絶対にコミットされないこと
     * `ListRuns` / `LoadRun` / `ReadPhaseLog` / `ReadReviews` / `FollowLog`（リアルタイム追記追従＋完了終了）と不正 `runID`・シンボリックリンク脱出拒否

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（ビルド後に一時バイナリが残らないようにする）
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テストの改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・Phase 2（`reifu run` / `reifu logs` CLI 接続とドキュメント更新）への引き継ぎ事項を簡潔に報告すること。
