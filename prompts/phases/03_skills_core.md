# Phase 3: Skills 連携基盤実装（`internal/model`, `internal/fsx` ディレクトリリンク＆Windows ジャンクション）

## 1. 参照ドキュメント（実装前に必読）

* `AGENTS.md`（行動規範・ゼロ外部依存・UTF-8/LF・3段階エスカレーション修復ループ）
* `docs/specs/002_skills_sync.md`（本機能の正式仕様書・受け入れ基準チェックリスト）
* `docs/design/01_project_overview.md` 〜 `03_architecture.md`
* 既存コード: `internal/model/config.go`, `internal/model/state.go`, `internal/fsx/link.go`, `internal/fsx/link_windows.go`, `internal/fsx/link_unix.go`, `internal/fsx/pathguard.go`

---

## 2. 今回の実装タスク

### 2.1 `internal/model` の拡張
1. `internal/model/config.go`:
   * `LinkModeJunction LinkMode = "junction"` 定数を追加する（`links.mode` の設定入力バリデーションは従来通り `auto` / `hard` / `symlink` / `copy` のみ許可し、`junction` はディレクトリリンクの結果・診断レポートモードとして使用する）。
   * `ReifuConfig` に任意の `Skills SkillsConfig \`json:"skills,omitempty"\`` を追加（既存 `.reifu.jsonc` と 100% 後方互換）。
     ```go
     type SkillsConfig struct {
         Enabled *bool    `json:"enabled,omitempty"`
         Source  string   `json:"source,omitempty"`
         Targets []string `json:"targets,omitempty"`
     }
     ```
   * ヘルパーメソッド `SkillsEnabled() bool`（`Enabled == nil || *Enabled`）、`EffectiveSkillsSource() string`（空時は `".agents/skills"`）、`EffectiveSkillsTargets(agents []AgentName) []string`（`Targets != nil` ならそのまま返し、nil 時は `agents` に `AgentClaude` を含めば `[]string{".claude/skills"}`、含まなければ nil）。
   * `Validate()` に `Skills` のバリデーションを追加:
     * `skills.source` が非空なら `validRelativePath` を満たすこと。
     * `skills.targets` の各要素が `validRelativePath` を満たし、重複せず（大文字小文字正規化）、かつ `EffectiveSkillsSource()` と同一・親・子孫（包含関係）でないこと。
2. `internal/model/state.go`:
   * `ReifuState` に `ManagedSkills map[string][]string \`json:"managed_skills,omitempty"\`` を追加。
   * `StatusReport` に `Skills []LinkReport \`json:"skills,omitempty"\`` を追加（スキル対象がないプロジェクトでは `omitempty` で既存 JSON と完全互換）。

### 2.2 `internal/fsx` のクロスプラットフォーム・ディレクトリリンク実装
1. **`internal/fsx/dirlink.go`（共通ロジック）**:
   * `DiscoverSkills(root, sourceDir string) ([]string, error)`:
     * `sourceDir` が存在しない場合は `nil, nil` を返す。
     * `sourceDir` 自体およびその親にプロジェクト外シンボリックリンク脱出がないことを `withinRoot` で検証する。
     * `sourceDir` が通常ディレクトリでない場合はエラー。
     * 直下のエントリをファイル名昇順で走査し、`.` 始まりの隠しディレクトリを除外しつつ、通常ディレクトリ（シンボリックリンク／ジャンクションでないこと）かつ直下に通常ファイル `SKILL.md` があるエントリ名 `<name>` を返す（大文字小文字衝突も検出してエラー）。
   * `CheckDirLink(root, source, target string) (model.LinkReport, error)`:
     * `withinRoot` で境界検証（ターゲット自身がシンボリックリンク／ジャンクションの場合は親ディレクトリがルート内であることを検証し、リンク先がルート外を指す場合はエラーまたは `DIFFERENT` として安全に扱う）。
     * 判定:
       * 未存在 → `LinkStatusMissing`
       * リンク／ジャンクション切れ（`os.Stat` が `ErrNotExist`）→ `LinkStatusBroken`（モード `symlink` または `junction`）
       * `os.SameFile(srcInfo, dstInfo)` が true → `LinkStatusOK`（モード `symlink` または `junction`）
       * 通常ディレクトリかつ配下の全通常ファイル相対パス・バイト内容が `source` と完全一致 → `LinkStatusCopy`（モード `copy`）
       * それ以外 → `LinkStatusDifferent`
   * `EnsureDirLink(root, source, target string, mode model.LinkMode, force bool) (model.LinkMode, error)`:
     * `source` がプロジェクト内の通常ディレクトリであることを確認。
     * 既に期待モード（`auto`/`hard` なら `symlink` または `junction`、`symlink` なら `symlink`、`copy` なら一致コピー）でリンク済みなら冪等に No-op 成功。
     * 既存があり `--force` 未指定なら非破壊エラー（`os.ErrExist`）。
     * 作成戦略:
       * `copy`: 一時ディレクトリへ安全コピー（内部のシンボリックリンク脱出拒否、ファイルパーミッション保持）後に差し替え。
       * `symlink`: 相対パス（`filepath.Rel(filepath.Dir(dst), src)`）で `os.Symlink`（失敗時はエラー）。
       * `auto` / `hard`:
         1. 相対シンボリックリンクを試行（成功 → `LinkModeSymlink`）。
         2. 環境によりフォールバック可能なエラー（Windows の特権不足 `ERROR_PRIVILEGE_NOT_HELD` 等）なら `createJunction(dst, src)` を試行（Windows のみ実装、成功 → `LinkModeJunction`）。
         3. それもフォールバック可能なエラーならディレクトリコピーへフォールバック（成功 → `LinkModeCopy`、`stderr` に通知）。
     * `--force` 時の既存差し替えは、まずリンク先本体を絶対に傷つけない `RemoveDirLink` 相当の安全処理（または一時退避→作成→失敗時復元）で行うこと。
   * `RemoveDirLink(root, target string) error`:
     * `withinRoot` で親ディレクトリを検証（ターゲット自身がシンボリックリンク／ジャンクションの場合は `os.Lstat` で判別し、**`os.Remove` でリンクエントリ自体だけを削除し、リンク先ディレクトリの中身には一切触れない**）。
     * 通常ディレクトリの場合のみ `os.RemoveAll` を実行する（念のため `source` 配下でないこと・ルート外でないことを検証）。
2. **`internal/fsx/dirlink_windows.go`（`//go:build windows`）**:
   * 外部依存なし（Go 標準の `syscall` + `kernel32.dll` `DeviceIoControl` + `FSCTL_SET_REPARSE_POINT`）で管理者権限・開発者モード不要の NTFS ディレクトリジャンクションを作成する `createJunction(linkPath, targetAbs string) error` を実装する（`cmd.exe` を起動しないこと）。
   * リパースポイントがジャンクション（`IO_REPARSE_TAG_MOUNT_POINT`）かシンボリックリンク（`IO_REPARSE_TAG_SYMLINK`）かを判別する `isJunction(path string) bool` / `isDirReparsePoint(info os.FileInfo, path string) bool` を実装する。
3. **`internal/fsx/dirlink_unix.go`（`//go:build !windows`）**:
   * `createJunction` は非対応エラー（`syscall.ENOTSUP`）を返し、`isJunction` は常に `false` を返す。

---

## 3. テスト要件

1. **`internal/model/model_test.go`**:
   * `SkillsConfig` のデフォルト解決（`enabled` 省略時 true、`claude` あり/なし時のデフォルト `targets`）とバリデーション（絶対パス、`..`、source と target の同一・包含関係、重複、制御文字の拒否）をテスト。
2. **`internal/fsx/fsx_test.go`**（または `dirlink_test.go`）:
   * `DiscoverSkills`: 正常検出（辞書順）、`SKILL.md` 無しディレクトリ・隠しディレクトリの無視、`.agents/skills` 未存在時の空返却、外部シンボリックリンク脱出の拒否。
   * `EnsureDirLink` / `CheckDirLink`:
     * `auto` モードでのリンク作成・冪等再実行・`CheckDirLink` が `OK`（`symlink` または `junction`）を返すこと
     * `copy` モードでのディレクトリ複製・内容一致時の `COPY` 判定・ファイル変更後の `DIFFERENT` 判定・`--force` 再同期
     * フォールバック注入テスト（`symlink` 失敗 → `junction` 成功、`junction` も失敗 → `copy` フォールバック）
     * 非破壊保護（既存ディレクトリが `--force` なしでエラー、`--force` で差し替え）
   * `RemoveDirLink` 安全性テスト:
     * リンク／ジャンクションに対して `RemoveDirLink` を実行したとき、リンク自体だけが消え、**正本 `.agents/skills/<name>/SKILL.md` および配下の `scripts/` が無傷で残ること**を厳格検証。
     * プロジェクト外を指すシンボリックリンク／ジャンクションがソース内にある場合の拒否検証。

---

## 4. 完了条件

* 依存追加禁止（Go 標準ライブラリのみ）。全生成ファイル UTF-8（BOM なし）/ LF 統一。
* 以下のコマンドがすべてエラーゼロで通ること:
  * `gofmt -s -w .`
  * `go vet ./...`
  * `go test -v ./...`
  * `CGO_ENABLED=0 go build ./cmd/reifu`（一時バイナリを残さないこと）
* ビルド・テスト失敗時は `AGENTS.md` の 3 段階エスカレーション修復ループ（上限 9 回）に従い、解決不能な場合のみ `BLOCKED_REPORT.md` を出力して停止する。テスト改ざん・スキップ・アサーション緩和は厳禁。
* 完了時は、変更ファイル一覧・テスト結果・Phase 4（`link` / `status` / `sync` / `init` 統合、E2E テスト、ドキュメント更新）への引き継ぎ事項を簡潔に報告すること。
