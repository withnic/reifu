# 005_run_skip_completed — 完了済みフェーズを引き継いで残りだけを実行する

## 1. 背景・目的

機能追加（`prompts/add_feature.md`）で `prompts/phases/` に新しいフェーズを足したあと、新しいフェーズだけを実行する簡単な方法がない。

* `reifu run`（新しいラン）は `prompts/phases/*.md` をすべて拾うので、完了済みのフェーズも最初からやり直す。
* `reifu run --resume` は前のランの状態を再開するだけで、あとから足したフェーズは入らない。前のランが完了していれば、何もせずに終わる。
* `reifu run --from <name>` なら新しいフェーズから始められるが、どれが新しいかを人やコンパニオンが調べて名前を渡す必要がある。

コンパニオン（shikiban）では、機能追加のあとの「run 開始」が `--resume` で何もせずに終わる、または全フェーズをやり直すことが実際に起きた。

本仕様では、最新のランで完了したフェーズを新しいランに引き継ぎ、残りだけを実行する `--skip-completed` を足す。あわせて、`status --json` の `runner` に未完了のフェーズの一覧を足し、コンパニオンが「実行すべきフェーズがあるか」を 1 回の読み取りで判断できるようにする。既存のフラグ・終了コード・JSON スキーマの後方互換は保つ。

## 2. スコープ

### In-Scope
* `reifu run --skip-completed`
* `reifu status --json` の `runner.pending_phases`
* 設計書（`docs/design/02_data_modeling.md`・`03_architecture.md`）、`README.md`・`HOWTO.md` の更新

### Out-of-Scope
* 完了後にフェーズのファイルが変わったことの検出（内容のハッシュ比較など）。名前だけで判断する
* 最新より前のランの状態を見ること（最新のラン `.reifu.run.json` だけを見る）
* 完了済みフェーズのコミットが今の Git の履歴に残っているかの検査

## 3. 詳細仕様

### 3.1 `reifu run --skip-completed`

* 新しいランを作る（新しい `run_id`）。`prompts/phases/*.md` を今と同じ規則で拾う。
* 最新のランの状態（`.reifu.run.json`）を読み、`status` が `completed` のフェーズを名前（大文字小文字を区別しない。今の衝突検査と同じ）で引き当てる。引き当たったフェーズは、前のランの記録（`status`・`implement_commit`・`review_commit`・`implement_log`・`review_report`・`commands_log`・`duration_ms`）を新しいランにそのまま写す。
  * ログやレビューのパスは前のランのディレクトリを指したままにする（コピーしない）。
* 引き当たらなかったフェーズ（新しいフェーズ、前のランで未完了・失敗・停止だったフェーズ）は、今の新しいランと同じく `pending` で始める。
* 実行の順序は今と同じ（ファイル名の辞書順）。完了済みのフェーズは、今の `--resume` と同じく飛ばす。
* `.reifu.run.json` がない・読めない場合は、エラーにせず通常の新しいランと同じ動きにする（警告を stderr に出す）。
* 未完了のフェーズが 1 つもなければ、エージェントを起動せずにランを `completed` で終える（`--dry-run` でない限り新しいランの状態は保存する。標準エラーに「実行するフェーズがない」と出す）。
* `--phase` / `--from` / `--resume` とは同時に指定できない（今の排他のエラーに足す）。`--no-review` / `--no-commit` / `--allow-dirty` / `--dry-run` / `--json` / `--stream-json` / `--cancel-on-stdin-eof` / `--notify` / `--timeout` は今と同じく使える。
* `--stream-json` では、写したフェーズについて `phase_started` などのイベントを出さない。`run_started` の `state` と終端イベントの `state` には写したフェーズも含める（`completed` / `total` は全フェーズで数える）。
* `--dry-run` では、写すフェーズを `completed` として計画に出す。

### 3.2 `reifu status --json` の `runner.pending_phases`

* `runner.phases` のうち、最新のランで `completed` になっていないフェーズの名前の配列（昇順）。最新のランがなければ `runner.phases` と同じ。
* 必須配列として、該当がなければ `[]` を出す。
* `phases` と同じく、エラーにせずベストエフォートで埋める（ランの状態が読めなければ `runner.phases` と同じにする）。

## 4. 受け入れ基準

- [x] `--skip-completed`: 前のランで 01〜04 が完了、05 を足した状態で、05 だけが実行され、ランの `completed` / `total` が 5 / 5 になる
- [x] 前のランで 02 が `failed` なら、`--skip-completed` で 02 から実行される（01 は写される）
- [x] 前のランの記録（コミット・ログのパス）が新しいランに写っている
- [x] 未完了のフェーズがなければ、エージェントを起動せずに `completed` で終わり、終了コードは `0`
- [x] `.reifu.run.json` がなければ、警告を出して通常の新しいランとして全フェーズを実行する
- [x] `--phase` / `--from` / `--resume` と同時に指定するとエラー（終了コード `1`）
- [x] `--stream-json`: 写したフェーズのイベントが出ず、`run_started` と終端イベントの `state` に写したフェーズが含まれる
- [x] `--dry-run`: 計画で写すフェーズが `completed` になっている
- [x] `status --json`: `runner.pending_phases` が、最新のランがない・全完了・一部未完了・新しいフェーズあり・ランの状態が壊れている、でそれぞれ正しい
- [x] 既存のテスト（`--resume`・新しいラン・`--from`）がそのまま通る
- [x] 設計書・`README.md`・`HOWTO.md`（「コンパニオンから CLI を呼び出す」に機能追加のあとの実行方法）を更新している
