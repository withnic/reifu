# 001_phase_runner — フェーズ実行・レビュー自動化オーケストレーター（`reifu run`）

## 1. 背景・目的

`reifu` では `prompts/phase0.md` や `prompts/add_feature.md` で実装フェーズ（`prompts/phases/01_*.md` 〜 `NN_*.md`）と共通レビュープロンプト（`prompts/review.md`）を生成できるが、従来は人間が各フェーズごとに以下を手動実行・監視する必要があった。

1. 実装担当エージェントへフェーズプロンプトを渡して実行する
2. 完了を確認して `git commit` する
3. レビュー担当エージェント（別ツールまたは同一ツールの新規セッション）へ `prompts/review.md` を渡してレビュー・修正させる
4. 完了を確認して `git commit` し、次のフェーズへ進む
5. 後から「どのフェーズでどんなレビュー指摘・修正が行われたか」を追跡する

本仕様では、この一連のサイクルを単一コマンド **`reifu run`** で自動実行・状態記録・中断再開・レビュー履歴保存まで完結させるオーケストレーター機能を追加する。

---

## 2. スコープ

### In-Scope
* 新サブコマンド **`reifu run [dir]`**（実行専用）と **`reifu logs [dir]`**（読み取り専用閲覧）の追加（Kubernetes / Docker 流に実行と閲覧を分離して誤爆を構造的に防止）
* 実装担当（`--implementer`）とレビュー担当（`--reviewer`）のエージェント指定（同一エージェントの別セッション、または異種エージェントの組み合わせに対応）
* 4 エージェント CLI の内蔵デフォルト対応: **`claude` / `codex` / `gemini` / `agy`（エイリアス: `antigravity`）**
* `.reifu.jsonc` の任意セクション **`runner`** によるデフォルト担当者・引数（モデル指定・モード指定等）・カスタムランナーの上書き対応
* 各フェーズごとの **実装 → ローカル `git commit` → レビュー（修正含む）→ ローカル `git commit`** ループの自動実行
* 実行中の進捗可視化（ステップ開始／完了バナー、子プロセス出力のリアルタイムストリーミング、無出力時の定期ハートビート表示）
* 進捗・チェックポイントファイル **`.reifu.run.json`** による状態永続化と **`--resume`** による中断ステップからの再開
* 人間が後から安全に閲覧できるログ・レビューレポートの自動保存（**`.reifu/runs/<run-id>/`** 配下に実装ログ・レビュー Markdown・ラン全体サマリーを保存、`.gitignore` 対象）および **`reifu logs`** による読み取り専用閲覧・`-f` 追従表示
* 異常終了・`BLOCKED_REPORT.md` 検出・タイムアウト時の安全停止
* `reifu run` 専用の多重起動防止ロック（`.reifu/run.lock`）と、子エージェントの `reifu hook verify` との非競合

### Out-of-Scope
* `git push` や PR 自動作成などのリモート操作（ローカル完結原則を維持）
* レビュアー NG 時に実装者へ自動差し戻しする多段リトライループ（レビュー修正は `prompts/review.md` の仕様通りレビュアー自身が完結させる）
* 複数フェーズの並列実行（フェーズ間依存があるため直列キュー実行のみ）
* 外部ファイルのポーリング監視による別プロセス間連携（`reifu run` が子プロセスを同期起動する）

---

## 3. 詳細仕様

### 3.1 CLI インターフェース

```text
reifu run [options] [dir]
```

| フラグ | 型 / デフォルト | 説明 |
| :---- | :---- | :---- |
| `--implementer <name>` | string（省略時は `.reifu.jsonc` の `runner.implementer`） | 実装担当エージェント名（`claude` / `codex` / `gemini` / `agy` / `antigravity` / カスタム名） |
| `--reviewer <name>` | string（省略時は `.reifu.jsonc` の `runner.reviewer`、未設定時は `--implementer` と同一） | レビュー担当エージェント名（毎回新規セッションで起動） |
| `--phase <name>` | string（省略時は全フェーズ） | 対象フェーズを 1 件に限定（ファイル名または部分一致せず完全一致/ベース名一致。例: `01_foundation.md` または `01_foundation`） |
| `--from <name>` | string | 指定フェーズ以降の未完了フェーズを実行 |
| `--no-review` | bool (`false`) | レビューステップをスキップし、実装（＋コミット）のみを順次実行 |
| `--no-commit` | bool (`false`) | 実装後・レビュー後の自動 `git commit` を行わない |
| `--allow-dirty` | bool (`false`) | 開始時に Git 作業ツリーに未コミット変更があっても続行する（デフォルトは安全のため拒否） |
| `--resume` | bool (`false`) | 最新の `.reifu.run.json` から未完了ステップ（`implement` または `review`）を再開する |
| `--timeout <duration>` | duration (`30m`) | 1 ステップ（実装またはレビュー 1 回）あたりのタイムアウト（`0` で無制限） |
| `--dry-run` | bool (`false`) | 子プロセス実行・ファイル書き込み・コミットを行わず、実行予定フェーズ・コマンド・ログ出力先を表示 |
| `--json` | bool (`false`) | 実行計画（`--dry-run`）または最終実行結果を単一 JSON で `stdout` へ出力（進捗・警告は `stderr` のみ） |

* `--implementer` がフラグでも `.reifu.jsonc` の `runner.implementer` でも未指定かつ `--resume` でもない場合はバリデーションエラー（Exit `1`）。
* `--phase` と `--from` の同時指定、および `--resume` と `--phase` / `--from` の同時指定はエラー（Exit `1`）。

#### 読み取り専用閲覧コマンド: `reifu logs [options] [dir]`

`reifu logs` は一切の書き込み・エージェント起動・Git 操作を行わない完全読み取り専用コマンドである。

| フラグ | 型 / デフォルト | 説明 |
| :---- | :---- | :---- |
| `--list` | bool (`false`) | `.reifu/runs/` 配下の過去ラン一覧（`<run-id>`・日時・ステータス・フェーズ進捗）を新しい順に表示 |
| `--run <id>` | string（省略時は最新ラン） | 閲覧対象の `<run-id>` を指定（`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` で検証しパストラバーサル拒否） |
| `--phase <name>` | string | 指定フェーズの実装ログ（`--review` 併用時はレビューレポート）を表示 |
| `--review` | bool (`false`) | レビューレポート（`<stem>_review.md`）を表示（`--phase` 未指定時は対象ラン内の全レビューを順に表示） |
| `-f` / `--follow` | bool (`false`) | 実行中の最新ログ（または `--phase` 指定ログ）を末尾追従表示する。ポーリングで差分を読み取った直後にファイルハンドルを都度閉じるため、Windows でも `reifu run` 側の書き込みを一切ブロックしない（ラン完了検知または Ctrl+C で終了） |
| `--json` | bool (`false`) | ラン一覧（`--list` 時）または対象ランの状態・ログメタ情報を単一 JSON で `stdout` へ出力（`--follow` との同時指定はエラー） |

* フラグ省略時（`reifu logs [dir]`）は、最新ランのフェーズ別進捗・コミット SHA・所要時間・ログ／レビューパスの一覧サマリーを表示する。
* ラン履歴が存在しない場合は案内メッセージを出力し Exit `1`（または `--list` 時は空一覧を出力し Exit `0`）。

### 3.2 `.reifu.jsonc` の `runner` 拡張スキーマ（任意・完全後方互換）

既存の `.reifu.jsonc`（`version: 1`）をそのまま維持しつつ、任意の `runner` フィールドを追加する。省略時は従来通り有効。

```jsonc
  "runner": {
    // 省略可。CLI の --implementer / --reviewer で上書き可能
    "implementer": "codex",
    "reviewer": "claude",
    // 任意: エージェント起動コマンド配列（シェル非経由で直接 exec）。
    // 内蔵名（claude / codex / gemini / agy）を指定するとデフォルトを上書きし、
    // 新規キー名を指定するとカスタムランナーとして --implementer / --reviewer で利用可能。
    "agents": {
      "claude": ["claude", "-p", "--model", "claude-sonnet-4-6"],
      "agy": ["agy", "--print", "--mode", "auto"]
    }
  }
```

#### バリデーションルール（`internal/model`）
* `runner.implementer` / `runner.reviewer`: 空文字許容。指定時は `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` に一致すること。
* `runner.agents`: 各キーは上記パターンに一致し、値は空でない `[]string`（先頭要素が空・空白のみ不可）、全要素に制御文字（`\r`, `\n`, `\x00` 等）を含まないこと。

### 3.3 内蔵エージェント起動コマンド仕様

すべてのエージェントは**シェル非経由（`exec.CommandContext`）**で起動し、プロンプト本文を UTF-8 テキストとして **`stdin`** から供給し、完了時に `stdin` を閉じる。

| エージェント名 | デフォルト引数配列 | 備考 |
| :---- | :---- | :---- |
| `claude` | `["claude", "-p"]` | Claude Code 非対話モード（`.claude/settings.json` の権限・Stop フックが適用される） |
| `codex` | `["codex", "exec", "-s", "<sandbox.mode>", "-"]` | `<sandbox.mode>` は `.reifu.jsonc` の `sandbox.mode`（デフォルト `workspace-write`） |
| `gemini` | `["gemini", "-p", ""]` | Gemini CLI 非対話モード（stdin をプロンプト入力として読み込み） |
| `agy`（エイリアス: `antigravity`） | `["agy", "--print", "--mode", "auto"]` | Antigravity CLI 非対話モード（`antigravity` 指定時は `agy` と同一扱い） |

* 実行開始前に `exec.LookPath(argv[0])` で対象バイナリの存在を事前検証（`--dry-run` を除く）し、未導入時は分かりやすいエラーで Exit `1` 終了する。
* `.reifu.jsonc` の `runner.agents` によりカスタムコマンド（内蔵デフォルト以外）が実行される場合は、信頼境界の原則に従い、実行コマンド配列を `stderr` に明示警告する。

### 3.4 フェーズ検出・実行ループ仕様

1. **事前チェック**:
   * `rejectHomeDir(root)` およびプロジェクト境界検証。
   * `.reifu/run.lock` で多重起動排他制御（既存 `.reifu.lock` とは独立させ、子エージェント内の `reifu hook verify` / `reifu sync` と競合させない）。
   * `BLOCKED_REPORT.md` が開始時点で存在する場合は即エラー停止（Exit `2`、`BLOCKED_REPORT.md` の確認・解消を案内）。
   * `--no-commit` が未指定の場合、`git rev-parse --is-inside-work-tree` で Git リポジトリであることを確認し、`--allow-dirty` が未指定なら `git status --porcelain` が空であることを検証（未コミット変更があれば Exit `1` で拒否）。
   * `prompts/phases/*.md` を収集し、ファイル名昇順（辞書順）でソートする（`--phase` / `--from` で絞り込み）。0 件ならエラー（Exit `1`）。
   * `--no-review` 未指定時は `prompts/review.md` の存在を確認する。
2. **各フェーズの実行フロー**:
   * **進捗表示（画面フリーズ防止）**:
     * 各ステップ開始・完了時に時刻と進捗 `[i/N]`・担当エージェント・コミット SHA・所要時間・出力パスを含むバナーをリアルタイム出力する（通常時は `stderr`、または `--json` 時も `stdout` 非汚染のため `stderr` のみ）。
     * 子プロセスの `stdout` / `stderr` は `io.MultiWriter` 等でターミナルとログファイルへ同時ストリーミングする。
     * `claude -p` / `agy --print` のように完了まで出力しない CLI でも生存確認できるよう、直近の出力から **30 秒**（テストでは差し替え可能）無出力が続くごとに `[HH:MM:SS]   ... running [i/N] <name> (<step>: <agent>, elapsed <dur>)` のハートビート行を `stderr` へ出力する。
   * **Step A: 実装（`implement`）**
     * 状態を `implementing` に更新し `.reifu.run.json` をアトミック保存。
     * `prompts/phases/<file>` の内容を `implementer` の `stdin` へ渡して実行。
     * ログファイル `.reifu/runs/<run-id>/<stem>_implement.log` は通常の追記モード（Windows の `FILE_SHARE_READ | FILE_SHARE_WRITE` 共有読み取り可）で逐次フラッシュ書き出しし、別プロセスからの閲覧と競合しないようにする。
     * 終了後、`BLOCKED_REPORT.md` の出現または非ゼロ終了コードを検知したら状態を `blocked` / `failed` にして `.reifu.run.json` 保存後、即座に安全停止（Exit `2`）。
     * `--no-commit` 未指定かつ作業ツリーにコミット対象差分がある場合、ローカル `git commit` を実行しコミット SHA を記録（コミットメッセージ例: `feat(phase): complete <stem>`）。
     * 状態を `implemented` に更新し `.reifu.run.json` を保存。
   * **Step B: レビュー（`review`、`--no-review` 時はスキップ）**
     * 状態を `reviewing` に更新し `.reifu.run.json` を保存。
     * `prompts/review.md` の本文の先頭に、対象フェーズのメタコンテキスト（対象フェーズファイルパス、実装担当エージェント名、実装コミット SHA、変更ファイル概要）を付与したプロンプトを `reviewer` の `stdin` へ渡して新規セッション実行。
     * `reviewer` の出力全文と変更差分サマリー（`git diff --stat`）を人間可読な Markdown として `.reifu/runs/<run-id>/<stem>_review.md` に保存。
     * `BLOCKED_REPORT.md` 出現または非ゼロ終了コードを検知したら状態を `blocked` / `failed` にして即座に安全停止（Exit `2`）。
     * `--no-commit` 未指定かつレビューによる修正差分がある場合、ローカル `git commit` を実行しコミット SHA を記録（コミットメッセージ例: `fix(review): address review for <stem>`）。
   * **Step C: フェーズ完了**
     * 状態を `completed` に更新し `.reifu.run.json` および `.reifu/runs/<run-id>/summary.md` をアトミック更新して次のフェーズへ進む。
     * 閲覧用サマリー（`summary.md`）の更新が万一 Windows のファイルロック等で失敗しても、ラン全体は失敗させず `stderr` に警告を出して続行する（`.reifu.run.json` およびランごとのスナップショット `.reifu/runs/<run-id>/run.json` にも状態を保持する）。
3. **誤コミット防止（Git ステージング境界）**:
   * `reifu run` がステージングする際は、`.gitignore` 未更新の既存リポジトリでもランタイムファイルやログを絶対にコミットしないよう、以下の pathspec 除外を必ず付与する:
     `git add -A -- . :!.reifu :!.reifu.run.json :!.reifu.state.json :!.reifu.lock :!.reifu.tmp.* :!BLOCKED_REPORT.md`

### 3.5 永続化ファイル仕様（すべて UTF-8 / LF、`.gitignore` 対象）

1. **`.reifu.run.json`**（最新ランの進捗・再開チェックポイント）:
   ```json
   {
     "version": 1,
     "run_id": "20261128T120000Z",
     "started_at": "2026-11-28T12:00:00Z",
     "updated_at": "2026-11-28T12:15:30Z",
     "implementer": "codex",
     "reviewer": "claude",
     "no_review": false,
     "no_commit": false,
     "run_dir": ".reifu/runs/20261128T120000Z",
     "phases": [
       {
         "file": "prompts/phases/01_foundation.md",
         "name": "01_foundation",
         "status": "completed",
         "implement_commit": "abc1234",
         "review_commit": "def5678",
         "implement_log": ".reifu/runs/20261128T120000Z/01_foundation_implement.log",
         "review_report": ".reifu/runs/20261128T120000Z/01_foundation_review.md",
         "duration_ms": 42100
       }
     ]
   }
   ```
2. **`.reifu/runs/<run-id>/<stem>_implement.log`**: 実装ステップのメタデータ＋標準出力・標準エラー全文。
3. **`.reifu/runs/<run-id>/<stem>_review.md`**: 人間向けのレビューレポート（フェーズ情報・コミット SHA・変更ファイル一覧・レビュアーの出力全文）。
4. **`.reifu/runs/<run-id>/summary.md`**: ラン全体の Markdown サマリーテーブル（各フェーズの状態・コミット・所要時間・レポートへの相対リンク）。
5. **`.gitignore` テンプレート更新**: `internal/template/embedded/common/gitignore.tmpl` および本リポジトリの `.gitignore` に `.reifu/` と `.reifu.run.json` を追加する。

### 3.6 セキュリティ制約・エッジケース
* **ホームディレクトリ拒否**: `rejectHomeDir(root)` を実行し、`~` 直下での実行を無条件拒否。
* **パス境界検証**: プロンプト読み込み・ログ書き出し・状態ファイル・ロックファイルはすべて `fsx.ValidateWithinRoot` を通過させる（`prompts/phases/` 配下のシンボリックリンク脱出も拒否）。
* **コマンドインジェクション排除**: すべての外部コマンド（エージェント CLI・`git`）は `exec.CommandContext` の引数配列で直接起動し、シェルを介さない。
* **制御文字拒否**: `runner` 設定値やフェーズ名に制御文字が含まれる場合は拒否。
* **`--json` ストリーム分離**: `--json` 指定時は `stdout` に単一 JSON のみを出力し、エージェント出力や進捗メッセージは一切 `stdout` に混入させない。
* **シグナル中断（Ctrl+C）**: `signal.NotifyContext` でキャンセルを子プロセスへ伝播し、現在状態と途中ログを保存した上で終了する。

---

## 4. 受け入れ基準チェックリスト

### モデル・設定・テンプレート
- [x] `internal/model` に `RunnerConfig`（`implementer` / `reviewer` / `agents`）とバリデーションが追加され、既存 `.reifu.jsonc` と 100% 後方互換である
- [ ] `internal/template/embedded/common/gitignore.tmpl` と本リポジトリ `.gitignore` に `.reifu/` と `.reifu.run.json` が含まれている
- [ ] `internal/template/embedded/prompts/README.md.tmpl` と `HOWTO.md` / `README.md` に `reifu run` の使い方が反映されている

### オーケストレーター実装（`internal/runner`）
- [x] `claude` / `codex` / `gemini` / `agy`（エイリアス `antigravity`）の 4 エージェントが内蔵デフォルトで解決・起動できる
- [x] `.reifu.jsonc` の `runner.agents` によるデフォルト上書きとカスタムランナー定義が動作し、カスタムコマンド使用時は `stderr` へ警告が出る
- [x] `prompts/phases/*.md` を辞書順で検出し、各フェーズについて `implement` → `git commit` → `review` → `git commit` を順次実行できる
- [x] レビュー実行時、`prompts/review.md` の内容に対象フェーズ名・実装コミット情報がコンテキストとして付与される
- [x] `.reifu/runs/<run-id>/` 配下に `<stem>_implement.log`・`<stem>_review.md`・`summary.md` が UTF-8 / LF で生成され、人間が後からレビュー内容を確認できる
- [x] `.reifu.run.json` に進捗がアトミック保存され、`--resume` で中断したフェーズまたはレビューステップから再開できる
- [x] `BLOCKED_REPORT.md` 検出・非ゼロ終了・タイムアウト時に安全停止（Exit `2`）し、状態を保存する
- [x] `git add` 時に `.reifu/`・`.reifu.run.json`・`.reifu.state.json`・`.reifu.lock`・`BLOCKED_REPORT.md` が絶対に誤コミットされない
- [x] `.reifu/run.lock` による多重起動防止が機能し、かつ `.reifu.lock` と競合しない

### CLI・セキュリティ・テスト
- [ ] `reifu run` の全フラグ（`--implementer`, `--reviewer`, `--phase`, `--from`, `--no-review`, `--no-commit`, `--allow-dirty`, `--resume`, `--timeout`, `--dry-run`, `--json`）と進捗表示（バナー・リアルタイムストリーミング・30 秒ハートビート）が正しく動作する
- [ ] 読み取り専用コマンド `reifu logs` の全フラグ（`--list`, `--run`, `--phase`, `--review`, `-f`/`--follow`, `--json`）が正しく動作し、ロック競合・誤書き込みを起こさない
- [ ] ホームディレクトリ直下拒否・未コミット作業ツリー拒否（`--allow-dirty` なし）・シンボリックリンク脱出拒否・`--run` ID のパストラバーサル拒否・`--json` の `stdout` 非汚染がテストで検証されている
- [ ] モックコマンド（実 AI API 非依存・クロスプラットフォーム）による `internal/runner` ユニット／結合テストと `cmd/reifu` E2E テストがすべて PASS する
- [ ] `gofmt -s -w .`、`go vet ./...`、`go test -v ./...`、`CGO_ENABLED=0 go build ./cmd/reifu` がすべて成功する
