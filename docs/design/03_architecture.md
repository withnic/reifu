# **03\_architecture — 霊符（reifu）**

# **1\. アーキテクチャ概要と設計原則**

霊符（reifu）は **Go 1.23+ 標準ライブラリのみ（ゼロ外部依存）** で構築するシングルバイナリ CLI アプリケーションである。

環境依存（OS種別、WSL2判定、ファイルシステム境界、パス境界検証、ハードリンク/シンボリックリンクAPI、アトミックファイル書き込み）を `internal/sysenv` および `internal/fsx` に完全に隔離し、ドメインロジック（テンプレート展開、設定コンパイラ、3-wayマージ、ドリフト検知）を純粋関数・インターフェース駆動でテスト可能に保つ。

# **2\. コンポーネント分割と依存方向**

```text
cmd/reifu/main.go               # CLIエントリーポイント・フラグ解析・終了コード制御
  └── internal/app/             # サブコマンド別ユースケース（new / init / link / sync / status / preset / hook / run / logs / version）
        ├── internal/model/     # ドメインモデル・定数・バリデーション・構造化エラー型（依存なし）
        ├── internal/jsonc/     # ゼロ依存 JSONC ストリッパー・パーサー・コメント保持編集
        ├── internal/compiler/  # .reifu.jsonc <-> Claude JSON / Codex TOML / Gemini JSON 変換・3wayマージ
        ├── internal/syncer/    # .reifu.state.json を使ったドリフト検知・--import 逆取り込みエンジン
        ├── internal/runner/    # `reifu run` フェーズ実装→コミット→レビュー→コミット自動オーケストレーター
        ├── internal/template/  # go:embed 内蔵プリセット + カスタムデリミタ（[[ .Var ]]）展開エンジン
        ├── internal/sysenv/    # OS・WSL2・FS境界（NTFS vs ext4）自動判別アダプタ
        └── internal/fsx/       # パス境界検証・アトミック書き込み・リンク生成・inode検証・排他ロック
```

* **依存ルール**: 上位レイヤーから下位レイヤーへの単方向依存のみ許可。`internal/model`、`internal/jsonc`、`internal/compiler` は `os` パッケージへの直接依存を持たず、`io.Reader` / `io.Writer`・純粋引数（例: `targetOS string`）・抽象インターフェース経由で動作する。
* **設定ディレクトリの DI**: `internal/template` におけるユーザーカスタムプリセットの解決先（デフォルトは `os.UserConfigDir()/reifu/presets/`）はコンストラクタ／オプションで差し替え可能にし、テストやサンドボックス環境（Codex `workspace-write` 等）では必ず `t.TempDir()` を注入してホスト側ユーザー設定ディレクトリへ書き込まないこと。

# **3\. ディレクトリ構成**

```text
reifu/
├── .github/
│   └── workflows/
│       └── ci.yml                      # Windows / macOS / Linux マトリクス CI
├── README.md                           # 概要・インストール・クイックスタート
├── HOWTO.md                            # 詳細ガイド・カスタムプリセット作成・セキュリティ仕様
├── AGENTS.md                           # 【単一ソース】全AIエージェント共通行動規範
├── CLAUDE.md                           # AGENTS.md へのハードリンク
├── GEMINI.md                           # AGENTS.md へのハードリンク
├── .claude/
│   └── settings.json                   # Claude Code 用設定（Stopフック・権限ガードレール）
├── .codex/
│   └── config.toml                     # Codex CLI 用設定（workspace-write & network_access=false）
├── .gemini/
│   └── settings.json                   # Gemini CLI 用設定（AGENTS.md読込 & AfterAgentフック）
├── .gitignore
├── .reifu.jsonc                        # プロジェクト設定マスター（ドッグフーディング）
├── go.mod                              # module github.com/withnic/reifu (go 1.23, 外部requireなし)
├── cmd/
│   └── reifu/
│       ├── main.go
│       └── main_test.go
├── internal/
│   ├── app/
│   │   ├── app.go
│   │   ├── init_cmd.go
│   │   ├── link_cmd.go
│   │   ├── sync_cmd.go
│   │   ├── status_cmd.go
│   │   ├── preset_cmd.go
│   │   ├── hook_cmd.go
│   │   ├── run_cmd.go
│   │   ├── logs_cmd.go
│   │   └── version_cmd.go
│   ├── runner/
│   │   ├── runner.go                   # フェーズ実行→レビュー→コミット自動ループ・中断再開・レポート生成
│   │   ├── steps.go                    # 実装・レビュー各ステップ、子プロセス起動・ストリーミング・ハートビート
│   │   ├── agents.go                   # claude / codex / gemini / agy 起動コマンド解決
│   │   ├── agy_output.go               # agy stream-json / json 解析・失敗判定・コマンド履歴抽出
│   │   ├── git.go                      # ローカル git status / add(除外pathspec付き) / commit / diff --stat
│   │   ├── logs.go                     # `reifu logs` 用読み取り専用ログ・レビュー・追従リーダー
│   │   └── runner_test.go
│   ├── model/
│   │   ├── config.go
│   │   ├── state.go
│   │   ├── run_state.go                # RunnerConfig / RunState / PhaseRunRecord / ValidateRunID
│   │   ├── errors.go
│   │   └── model_test.go
│   ├── jsonc/
│   │   ├── strip.go
│   │   ├── edit.go                     # コメント保持配列要素挿入＋Unmarshal 自己検証
│   │   └── strip_test.go
│   ├── compiler/
│   │   ├── agy.go
│   │   ├── claude.go
│   │   ├── codex.go
│   │   ├── gemini.go
│   │   └── compiler_test.go
│   ├── syncer/
│   │   ├── drift.go
│   │   ├── importer.go                 # 危険コマンドガード＋jsonc.edit 呼び出し
│   │   └── syncer_test.go
│   ├── template/
│   │   ├── engine.go
│   │   ├── presets.go
│   │   ├── engine_test.go
│   │   └── embedded/                   # go:embed 対象テンプレート群
│   │       ├── common/
│   │       │   ├── AGENTS.md.tmpl
│   │       │   ├── gitattributes.tmpl
│   │       │   ├── reifu.jsonc.tmpl
│   │       │   └── gitignore.tmpl
│   │       ├── docs/
│   │       │   ├── 01_project_overview.md.tmpl
│   │       │   ├── 02_data_modeling.md.tmpl
│   │       │   └── 03_architecture.md.tmpl
│   │       ├── specs/
│   │       │   └── README.md.tmpl
│   │       ├── prompts/
│   │       │   ├── README.md.tmpl
│   │       │   ├── phase0.md.tmpl
│   │       │   ├── add_feature.md.tmpl
│   │       │   ├── review.md.tmpl
│   │       │   └── phases/
│   │       │       └── .gitkeep
│   │       └── presets/
│   │           ├── go-cli.jsonc
│   │           ├── ts-web.jsonc
│   │           ├── ts-electron.jsonc
│   │           ├── rust-cli.jsonc
│   │           └── minimal.jsonc
│   ├── sysenv/
│   │   ├── detect.go
│   │   ├── detect_windows.go
│   │   ├── detect_unix.go
│   │   ├── process_windows.go          # 子プロセスツリー終了（taskkill /T /F）
│   │   ├── process_unix.go             # 子プロセスツリー終了（プロセスグループ SIGKILL）
│   │   ├── notify.go                   # 完了・停止通知（CI/REIFU_NO_NOTIFY抑止・サニタイズ）
│   │   ├── notify_windows.go           # Windows MessageBeep + OSC 9 通知
│   │   ├── notify_darwin.go            # macOS osascript + ベル通知
│   │   ├── notify_linux.go             # Linux/WSL2 notify-send + ベル通知
│   │   └── detect_test.go
│   └── fsx/
│       ├── pathguard.go                # プロジェクトルート境界・シンボリックリンク脱出検証
│       ├── directory.go                # 出力ルート親探索・安全ディレクトリ作成
│       ├── link_paths.go               # リンク先と保護ファイル間の衝突・エイリアス検証
│       ├── atomic.go
│       ├── lock.go
│       ├── link.go
│       ├── link_windows.go
│       ├── link_unix.go
│       ├── dirlink.go                  # スキルディレクトリ単位のリンク作成・検証・安全削除
│       ├── dirlink_windows.go          # Windows 管理者不要 NTFS ジャンクション作成・判定
│       ├── dirlink_unix.go
│       └── fsx_test.go
├── docs/
│   ├── assets/
│   │   └── reifu.jpg
│   ├── design/
│   │   ├── 01_project_overview.md
│   │   ├── 02_data_modeling.md
│   │   └── 03_architecture.md
│   └── specs/
│       └── README.md
└── prompts/
    ├── README.md
    ├── add_feature.md
    ├── review.md
    └── phases/
        └── .gitkeep
```

# **4\. OS / 環境差異の吸収・セキュリティ基盤（`internal/sysenv` & `internal/fsx`）**

## **4.1 実行環境（OS・WSL2・ファイルシステム境界）の自動判別**

`reifu init` / `reifu link` / `reifu status` 実行時、対象ディレクトリの絶対パス（`filepath.EvalSymlinks` 解決後）に対して以下を判別する。

1. **Windows ネイティブ (`EnvWindows`)**: `runtime.GOOS == "windows"`。NTFS ボリューム上では管理者権限不要でハードリンク（`os.Link`）が作成可能（隔離サンドボックス等で権限拒否された場合はフォールバック）。
2. **WSL2 ネイティブ Linux FS (`EnvWSL2LinuxFS`)**: `runtime.GOOS == "linux"` かつ環境変数 `WSL_DISTRO_NAME != ""` または `/proc/version` に `microsoft` / `WSL` を含み、対象パスが `/mnt/[a-zA-Z](/|$)` 配下**ではない**（ext4 等の上にある）。`os.Link` でハードリンク作成可能。
3. **WSL2 Windows マウント境界 (`EnvWSL2DrvFS`)**: WSL2 上で対象パスが `/mnt/[a-zA-Z](/|$)`（9P/DrvFs 経由の Windows NTFS）配下にある場合。ファイルシステム仕様上 `os.Link` が `EXDEV` または `EPERM` で失敗するため、`mode == "auto"` の場合は警告メッセージを stderr に出力しつつ **シンボリックリンク（失敗時はファイルコピー）へ自動フォールバック** する。
4. **macOS (`EnvDarwin`) / ネイティブ Linux (`EnvLinux`)**: 同一デバイス内であれば `os.Link` でハードリンク作成可能。

## **4.2 プロジェクトルート境界・シンボリックリンク脱出検証（`fsx.ValidateWithinRoot`）**

すべてのファイル書き込み・リンク作成・削除の前に、対象パスがプロジェクトルート配下に収まっていることを検証する。

1. ルートディレクトリを絶対パス化し、`filepath.EvalSymlinks` で正規化する。
2. 対象パスに絶対パスや `..` によるルート外脱出が含まれないことを `filepath.Rel` で検証する。
3. 対象ファイルおよびその既存親ディレクトリについて `os.Lstat` および `filepath.EvalSymlinks` を確認し、**プロジェクト外を指すシンボリックリンク**（例: `.claude -> ~/.claude`）が途中に存在する場合は即座にエラーを返して書き込み・リンク操作を拒否する。

## **4.3 ハードリンク生成と同一性検証（`os.SameFile`）**

* **リンク作成 (`fsx.EnsureLink`)**:
  1. `ValidateWithinRoot` でソース（`AGENTS.md`）およびターゲット（`CLAUDE.md` / `GEMINI.md` 等）がルート配下かつ外部シンボリックリンクでないことを確認する。
  2. ソースファイル（`AGENTS.md`）の存在を確認する。
  3. ターゲットファイルが既に存在する場合、`os.Lstat` および `os.Stat` と `os.SameFile(srcInfo, dstInfo)` で既に同一実体を指しているか検証する。
  4. 既にリンク済みであれば No-op（冪等）として成功を返す。
  5. 別実体ファイルが存在し `--force` が未指定の場合は上書きせずエラーを返す。`--force` 指定時は一時ファイルへのリンク作成＋`os.Rename`（またはバックアップ退避後のリンク作成）で安全に差し替える。
  6. `mode == "auto"` で `os.Link` がクロスデバイスエラー（`EXDEV`）または非対応・権限エラー（`ENOTSUP` / `EPERM` / `EACCES`）を返した場合、自動的に `os.Symlink`（相対パス `"AGENTS.md"`）へフォールバックし、それも権限不足（Windows開発者モード無効等）で失敗した場合はファイルコピーへフォールバックする。

スキルディレクトリはスキルごとにリンクする。 `auto` / `hard` では相対シンボリックリンクを試し、Windows では NTFS ジャンクション、さらにディレクトリコピーへフォールバックする。 `internal/syncer` が計画と管理履歴を扱い、`internal/fsx` がパス境界の検証とリンク自体の作成・削除を担当する。

## **4.4 アトミック書き込み・リトライ・排他ロック（`fsx.WriteFileAtomic` / `fsx.WithFileLock`）**

* **アトミック書き込み (`fsx.WriteFileAtomic`)**:
  * 同一ディレクトリ内に `os.CreateTemp`（暗号論的または十分なランダム性を持つ一時ファイル `.reifu.tmp.<pid>.*`、作成パーミッション `0600`）を作成し、改行コードを LF（`\n`）へ正規化した UTF-8 バイト列を書き込み、`Sync()` 後に `Close()` して `os.Rename` で目的パスへ置換する。エラー発生時は `defer` で一時ファイルを確実に `os.Remove` する。
  * 既存ファイルがある場合はそのパーミッション（`0644` 等）へ `Chmod` してから置換する（新規ファイルは `0644`）。
* **Windows 共有違反リトライ**: Windows 環境でウイルス対策ソフトやエディタ（VS Code / Zed）が一時的にファイルハンドルを保持している場合を考慮し、`os.Rename` および `os.Link` が `ERROR_ACCESS_DENIED` / `ERROR_SHARING_VIOLATION` を返した場合は、初回 20ms から最大 5 回（20ms, 40ms, 80ms, 160ms, 320ms）の指数バックオフリトライを行う。ランナーの状態ファイル・ログの読み取り（`fsx.OpenRegular`）も、置換中の一時的な共有違反に対して同じリトライを行う。
* **プロセス間排他ロック (`fsx.WithFileLock`)**: 複数エージェントの同時実行による書き込み競合を防ぐため、`init` / `sync` / `link` 実行時はプロジェクトルートの `.reifu.lock` を `O_CREATE|O_EXCL` で取得し、自身の PID と UTC タイムスタンプを書き込む（タイムアウト 5 秒）。5 秒以上経過した古いロックファイルは異常終了の残骸とみなして回収するが、他プロセスとの TOCTOU 競合を避けるため回収直後に再度 `O_CREATE|O_EXCL` で排他取得を試行するループとする。`.reifu.lock` と `.reifu.tmp.*` は `.gitignore` に自動登録する。
* **I/O ストリーム分離**: `--json` フラグ指定時、標準出力（`stdout`）には単一の JSON 値を出力する。`sync` とセットアップの警告はレポートの `warnings` に格納し、その他の診断テキストは標準エラー出力（`stderr`）へ送る。

## **4.5 テンプレートデリミタの衝突回避とデータ制約**

* 生成対象の Markdown や JSON/TOML テンプレート内には `{{ ... }}`（GitHub Actions や Go template のサンプルコード）が含まれるため、`text/template` のデリミタを **`[[` と `]]`** に変更してパースする（例: `[[ .ProjectName ]]`、`[[ .Year ]]`）。
* テンプレートへ渡すコンテキストはファイル操作や環境参照などのメソッドを持たないプレーンなデータ構造体（`TemplateData`）のみに限定する。
* **`AGENTS.md.tmpl` の標準コマンド一覧**: `Makefile` 等の外部タスクランナーに依存せず人間と AI が共通手順を参照できるよう、`AGENTS.md.tmpl` にはプリセット別（`go-cli` / `ts-web` / `minimal`）の **セットアップ（インストール）→ 実行 → Lint/フォーマット → テスト → ビルド** コマンド一覧を言語標準コマンドで必ず展開し、各プリセットの `.reifu.jsonc`（`allow_commands` / `verify_command`）と整合させる。

## **4.6 `reifu run` の子プロセス・ロック・再開の制約**

* **ロック**: `reifu run` は `.reifu/run.lock`（`fsx.WithNamedFileLock`）を使い、`.reifu.lock` とは独立させる。子エージェント内の `reifu sync` / `reifu hook verify` は `.reifu.lock` を取るので競合しない。ラン実行中のロックは OS ロックで生存判定されるため、5 秒を過ぎても他プロセスに回収されない。
* **子プロセスツリーの終了**: タイムアウトや Ctrl+C では、直下の子だけでなくプロセスツリー全体を止める（`sysenv.KillTreeOnCancel`）。Windows では `claude` / `codex` / `gemini` が `.cmd` シム（`cmd.exe` → `node`）であることが多く、直下の子だけを止めると本体が作業ツリーの編集を続けてしまう。Windows は `taskkill /T /F`、Linux / macOS はプロセスグループ（`Setpgid`）への `SIGKILL` で止める。制限付きアカウント（サンドボックス用ユーザー等）では、自分が起動したプロセスでも `taskkill` がアクセス拒否になることがあるため、失敗時は Toolhelp スナップショットで子孫を辿って `TerminateProcess` し、最後に直下の子を止める。PID 再利用で無関係なプロセスを止めないよう、親より前に作成されたプロセスは子孫とみなさない。
* **エージェントのパス解決**: `runner.agents` の先頭要素は、名前だけなら `PATH` から探す。相対パス（`./scripts/agent` 等）は、エージェントの作業ディレクトリと同じプロジェクトルートを基準に解決する。
* **再開時の作業ツリー**: `--resume` でも未コミット変更の検査は省かない。失敗したステップが残した変更をそのままコミットしないよう、確認した上で `--allow-dirty` を付けて再開する。再開時の `no_review` / `no_commit` は `.reifu.run.json` に記録した値を使う。
* **コミット済みチェックポイント**: コミット直前に `commit_base`（その時点の HEAD）を保存する。コミット後・状態保存前に中断された場合、再開時に HEAD の親とコミット件名が一致すればそのコミットを再利用し、二重コミットしない。
* **通知** — ランナーは `--notify` と `runner.notify` から通知のタイミングを決め、`internal/sysenv` が本文をサニタイズして OS ごとの通知を送る。Windows は MessageBeep と OSC 9、macOS は端末ベルと `osascript`、Linux / WSL2 は端末ベルと `notify-send` を使う。端末向けの出力は標準エラーが端末の場合に限る。Windows の OSC 9 は、コンソールの VT 処理（`ENABLE_VIRTUAL_TERMINAL_PROCESSING`）を有効にできた場合だけ書き込み、書き込み後に元のモードへ戻す（旧コンソールにエスケープが生のまま表示されるのを防ぐ）。通知処理はベストエフォートで、外部コマンドは最大 3 秒で打ち切る。`--dry-run`、`CI`、`REIFU_NO_NOTIFY` は通知を抑止する。

## **4.7 ADR（アーキテクチャ決定記録）**

### ADR-001: 引数プロンプト差し込み（`{prompt}`）と長文プロンプトのファイル退避

* **背景**: Antigravity CLI（`agy` 1.5.18）の `--print` / `-p` / `--prompt` はフラグ値としてプロンプト文字列を受け取り、標準入力（`stdin`）から生 Markdown を読まない（`--input-format stream-json` は独自 NDJSON を要求する）。一方、Windows の `CreateProcessW` にはコマンドライン全体で 32,767 UTF-16 コード単位の上限があり、レビュープロンプト（`prompts/review.md`＋差分サマリー）や長大なフェーズ仕様書は上限に達しうる。Linux でも 1 引数あたり `MAX_ARG_STRLEN`（128 KiB）の制約がある。
* **決定**:
  1. `runner.agents` の `argv[1:]` に `{prompt}` を含む場合、シェルを介さず `exec.CommandContext` の引数要素へプロンプト本文（BOM 除去・LF 正規化）を展開し、`cmd.Stdin` は `nil` とする。`argv[0]` への `{prompt}` 配置はバリデーションで拒否する。
  2. Windows 互換エスケープ後のコマンドライン長（UTF-16 コード単位、NUL 終端含む）を全 OS 共通で算出し、**24,000 UTF-16 コード単位**を超える場合は本文を `.reifu/runs/<run-id>/<phase>_<step>_prompt.md` へアトミック書き出しした上で、`Read the full instructions from <path> and follow them completely.` という短い指示文を `{prompt}` へ展開する。退避後なお 32,767 単位を超える場合のみ明確なエラーとする。
* **採用理由**: 上限超過時にエラー停止する案に比べ、規模の大きいフェーズやレビュー差分でも自動実行が途中で破綻せず完走できる。`.reifu/runs/<run-id>/` はプロジェクトワークスペース配下かつ Git コミット対象外であり、`.gitignore` 配下であっても `agy` から追加権限なしで読み取れることを実機で確認済みである。また全 OS 共通のしきい値（24,000 UTF-16 単位 ≒ 最大約 72 KiB UTF-8）にすることで、Windows の 32,767 上限と Linux の 128 KiB 引数上限の双方を余裕をもって回避しつつ、CI 上でのテスト挙動を決定的に保てる。

### ADR-002: `agy` のヘッドレス実行モード（`--mode accept-edits`）と `--settings` によるセッション権限適用

* **背景**: 公式仕様および `agy` 1.5.18 実機検証の結果、`agy` はプロジェクト内の `.gemini/settings.json` を読み込まず、永続設定はグローバルの `~/.gemini/antigravity-cli/settings.json`（および `~/.gemini/config/projects/`）のみである。また `--mode auto` はビジネス／エンタープライズアカウントやターミナルサンドボックス非対応環境では利用できず `default` モードへフォールバックする。さらにヘッドレス実行では `permissions.allow` に合致しないコマンド実行が自動拒否される。
* **決定**:
  1. 内蔵 `agy` の既定起動引数を `["agy", "--mode", "accept-edits", "--print={prompt}"]` とする（`--dangerously-skip-permissions` は既定にしない）。
  2. `.reifu.jsonc` の `permissions` にルールが存在する場合、`reifu run` が `internal/compiler.CompileAgySettings` で `.reifu/runs/<run-id>/agy-settings.json` を生成し、`--settings <path>` で当該セッションにのみ渡す（`runner.agents.agy` でカスタム定義した場合はユーザー指定引数を改変しない）。
  3. コマンドルールは `agy` の POSIX 語プレフィックス一致と Windows 行全体 `regex:` 一致の双方で機能するよう `command(...)` と `command(regex:...)` へ変換し、`deny_commands` が `Deny > Ask > Allow` の優先順位で確実に効くようにする。`deny_paths` は `read_file(...)` がパスプレフィックス一致であるためリテラルパスのみ出力する。
* **採用理由**: `reifu` の「薄いグローバル・自己完結型ローカル」原則を厳守して `~/.gemini/` を一切書き換えず、`reifu sync` の管理対象ファイルやドリフト面も増やさずに `.reifu.jsonc` の `allow_commands` / `deny_commands` / `deny_paths` をそのまま `agy` のヘッドレス実行へ反映できるため。また `--mode accept-edits` を採用することで、アカウント種別やサンドボックス有無に依存せずワークスペース内のファイル編集を自動承認しつつ、コマンド実行は明示的な `allow` / `deny` ルールで安全に制御できる。

### ADR-003: `agy` の `--output-format stream-json` 解析によるサイレント失敗検知とエージェント非依存の空実行ガード

* **背景**: `agy` 1.5.18 のヘッドレスモードでは、未許可コマンドが自動拒否されて何も作業せずに終了しても、プロセス終了コードが `0` かつ `status: "SUCCESS"`（`response: ""`・`denied_actions` あり）で返るため、終了コードのみの監視では空のフェーズがすべて `completed` と誤認される。
* **決定**:
  1. 内蔵 `agy` の既定引数に `--output-format stream-json` を付与し、`internal/runner/agy_output.go` で `stdout` の NDJSON をリアルタイム解析する（`--output-format json` のカスタム指定も同様に終端エンベロープを解析する）。人間向けログ（`<phase>_implement.log`・`<phase>_review.md`・進捗表示・`--stream-json` の `log` イベント）には生 JSON を流さず、`[tool] ...` の 1 行要約と `text_delta` / `response` 本文のみを出力する。
  2. プロセス終了コードが `0` であっても、次のいずれかに該当する場合はステップを `failed` にして停止する（既存の `--resume` で同一ステップからやり直し可能）:
     * 終端の `result` イベントが欠落している（途中切断・壊れた JSON のみ等）
     * `result.status != "SUCCESS"`
     * `result.denied_actions` が非空、または `step_update` でヘッドレス自動拒否（`headless_auto_deny`）が検出された
     * `result.response` が空白のみ（空レスポンス）
     * ※ `deny_commands` による明示拒否（`Matches user-configured deny rule.`）は `result.denied_actions` に含まれず、エージェントが拒否に従って正常完走（`status == "SUCCESS"` かつ非空 `response`）した場合は失敗扱いしない。
  3. 壊れた JSON 行・途中切断・未知のイベント種別でもパニックせず安全に処理する（未知イベントは無視、終端 `result` 欠落時は明確なエラー）。
  4. **エージェント非依存の安全策**: Git リポジトリ上の実装ステップ（`implement`）において、エージェントが終了コード `0` で終了したにもかかわらず「作業ツリー差分なし・HEAD 未更新・エージェント出力も完全空（空白のみ）」のすべてを満たす場合は `failed` とする。一方、正当な差分ゼロフェーズ（既に要件充足・確認のみ等）ではエージェントが応答テキストを返すため、出力が非空であれば正常完了を許容する。
* **採用理由**: `--output-format json` と比較して `stream-json` は終端 `result` に加えてリアルタイムの `step_update`（ツール名・`CommandLine`・成功/拒否状態・`text_delta`）を得られるため、進捗可視化・ハートビート維持・正確なコマンド抽出を同時に満たせる。また差分ゼロ＋出力ゼロの組み合わせのみを全エージェント共通の失敗条件とすることで、正当な差分ゼロフェーズの誤停止を避けつつサイレント異常終了を捕捉できる。

### ADR-004: フェーズ別コマンド履歴（`<phase>_commands.json`）の記録と拒否コマンド提示

* **背景**: ヘッドレス実行が権限不足で止まった際、`agy` の `--output-format json` の `denied_actions` にはツール種別（`command` / `Bash`）しか含まれず、どのコマンドを `.reifu.jsonc` の `permissions.allow_commands` に追加すべきか利用者が判別できない。
* **決定**:
  1. `agy` の `stream-json` `step_update`（`tool_info.parameters.CommandLine` 等）から実行・拒否されたコマンドを抽出し、フェーズごとに `.reifu/runs/<run-id>/<phase>_commands.json` へアトミック保存する（`--output-format json` 等でコマンド文字列が取れない場合は `result.denied_actions` のツール名／アクション名をフォールバック記録する）。
  2. `denied_by` で許可不足の自動拒否（`headless_auto_deny`）と明示拒否ルール（`deny_rule`）を区別し、`headless_auto_deny` に該当したコマンドのみを `PhaseRunRecord.denied_commands`・`RunEvent.denied_commands`（`phase_failed` / `run_failed`）・`summary.md`・`reifu logs` / `reifu logs --json` および停止時エラーメッセージへ「`permissions.allow_commands` に足す候補」として表示する（`deny_commands` で意図的にブロックしたコマンドは許可候補に出さない）。
  3. 記録をもとに `.reifu.jsonc` へ許可を自動追記することはしない（追加判断はユーザーに委ねる）。
  4. **他エージェントのスコープ**: 内蔵の `claude` / `codex` / `gemini` は既定でプレーンテキスト出力のためコマンド抽出は現時点では `agy` のみ対象としつつ、`<phase>_commands.json` のスキーマは `agent` フィールドを持つ汎用構造として将来の拡張に備える。
* **採用理由**: `--log-file` 等の外部ログ解析に依存せず、`agy` 公式仕様の `stream-json` `tool_info.parameters.CommandLine` からゼロ外部依存かつクロスプラットフォームで正確なコマンドを取得できるため。また許可の自動昇格を行わないことで `reifu` のセキュリティ境界を維持しつつ、`reifu logs` → `.reifu.jsonc` 追記 → `reifu run --resume` の最短復旧ループを実現できる。

# **5\. エージェントディレクトリ存在判定ポリシー**

* `reifu sync` 実行時、`.reifu.jsonc` の `agents.targets` に含まれる各エージェント（`claude`, `codex`, `gemini`）について、プロジェクト直下の対応ディレクトリ（`.claude/`, `.codex/`, `.gemini/`）の存在を確認する。
* ディレクトリが存在しない場合:
  * `agents.create_missing == false`（デフォルト）かつ `--create` フラグ未指定であれば、**未導入・不要エージェントとみなしてエラーにせず静かにスキップ**する（`status` では `SKIPPED` と表示）。
  * `reifu init` 実行時は `--no-settings` が指定されない限り、指定されたターゲットのディレクトリと設定ファイルを初期生成する。

# **6\. テスト戦略**

1. **ユニットテスト（メモリ・一時ディレクトリ駆動）**:
   * `internal/model`: 全バリデーション（`version`、`preset` パストラバーサル拒否、`links.targets` 境界、`danger-full-access` × `approval_policy=never` 拒否、改行・制御文字拒否）の網羅テスト。
   * `internal/jsonc`: コメント（行・ブロック・文字列内の擬似コメント）、トレイリングカンマ、不正構文エラー行番号、および `edit.go` のコメント保持配列挿入（空配列・末尾カンマ・インラインコメント・1行配列・特殊文字エスケープ）の網羅テスト。
   * `internal/compiler`: `.reifu.jsonc` から Claude JSON / Codex TOML / Gemini JSON への変換、既存カスタムキー・TOML コメント保持、3-way diff による `allow` / `deny` / `deny_paths`（`Read`/`Edit`、旧 `Write` の移行除去含む）/ `hooks` の追加・更新・削除反映、OS 別 `verify_command` 解決、TOML インジェクション耐性のテスト。
   * `internal/syncer`: 全状態（`STATE_MISSING`, `IN_SYNC`, `MASTER_UPDATED`, `DRIFTED`, `CONFLICT`, `SKIPPED`）の判定、`--import` 逆取り込みラウンドトリップ、危険コマンドおよび `deny_commands` 衝突ガードのテスト。
2. **ファイルシステム・リンク結合テスト (`t.TempDir()`)**:
   * `os.SameFile` を使ったハードリンク作成・冪等再実行・異なるファイル存在時の非破壊保護・`--force` 上書きの検証。
   * 擬似 `EXDEV` エラー注入による `auto` モードのフォールバック挙動の検証。
   * `ValidateWithinRoot` による `..` パストラバーサルおよびプロジェクト外シンボリックリンク追従の拒否検証。
3. **CLI E2E テスト（全テスト `t.TempDir()` 完結）**:
   * `init` → `status`（通常・`--json`・`--quiet`）→ 外部編集シミュレート → `sync`（Exit 2 検証）→ `sync --import` → `.codex` / `.gemini` への反映確認 → `CLAUDE.md` ハードリンク破壊後の `link --check`（Exit 3）→ `link --force` 修復までの一連ワークフローテスト。
   * `.reifu.state.json` 削除状態（`git clone` 直後シミュレート）からの `status --quiet`（Exit 2）および `reifu sync` ベースライン復元テスト、ならびに `.reifu.jsonc` 内の `//` コメントが `reifu sync --import` 後も保持されることの検証。
   * `preset list` / `preset export` の DI された一時設定ディレクトリ上での動作確認。
   * `CGO_ENABLED=0 go build ./cmd/reifu` および `go test -race ./...` によるデータ競合ゼロ検証。

## コンパニオンとのプロセス連携

コンパニオンは `reifu` をサブプロセスとして起動し、標準入出力と終了コードを扱う。`preset list`、`sync`、`link`、`new`、`init`、`status`、`logs`、`run`、`version` の `--json` は標準出力に単一の JSON 値を返す。`sync` とセットアップの警告は JSON の `warnings` で取得できる。`link --check --json` の Exit 3 でも検査結果を読める。ダッシュボードは `status --json` の `runner` から Git 状態、フェーズ、エージェントの検出結果、最新ランを取得する。Git 検査の上限は 5 秒で、最新ランは読み取りに成功した場合に返す。

`run --stream-json` は `--json` と排他で、進捗と正規化済みエージェント出力を NDJSON で送る。ログファイルへの保存は継続する。イベント書き出しを直列化し、ロック取得順は `agentStream.mu` → `execution.eventMu` とする。標準出力の読み手が止まると、エージェント出力の書き込みにも背圧がかかる。親プロセスは標準出力と標準エラーを並行して読み続ける。端末通知のフォールバックは標準エラーに送り、ランサマリ書き込み失敗などの警告テキストはストリーム時に破棄する。

終端イベントは 1 ラン 1 行で、ランナーが送出前に失敗したときは CLI が `run_failed` を補う。フォールバックはブロックによる Exit 2 で `blocked`、それ以外で `failed`。各行に `elapsed_ms` があり、`heartbeat`、`step_completed`、`phase_completed`、`phase_failed` 以外では `0` になる。`implement_commit` / `review_commit` から再開したステップは `commit` / `step_completed` のみを発行する。フィールドの詳細は [データモデル](02_data_modeling.md) を参照。

`--cancel-on-stdin-eof` を有効にする親プロセスは、実行開始時に stdin のパイプを開いたままにする。親が書き込み側を閉じると、ランのコンテキストがキャンセルされ、子プロセスツリーを停止してチェックポイントを保存する。開始前に stdin が閉じていれば即キャンセルされ `run_failed` が出る。このフラグは dry-run では無効。ラン完了後、stdin の読み取り goroutine は `Read` が戻るか CLI プロセスが終了するまで残る。キャンセルとタイムアウトはともに `failed` で、`error` はそれぞれ `context canceled` / `context deadline exceeded` を含む。

Windows では、状態ファイル（`.reifu.run.json` / `run.json`）のアトミック置換中に読み取ると、一時的な共有違反（`ERROR_SHARING_VIOLATION`）やアクセス拒否が起こる（Phase 6 の race テストで `TestFollowLog` が一度失敗した原因の候補）。ランナーの読み取りは `fsx.OpenRegular` を通し、書き込み側と同じ指数バックオフ（最大 5 回）で再試行する。実行中のランを `status --json` や `logs -f` で読んでも、`runner.latest_run` が一時的に抜けにくい。再試行を使い切った場合だけ、ベストエフォートとして `latest_run` を省略する。
