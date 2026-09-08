# データベース設計

テーブル名は 2 語の `snake_case` に統一し、`news_sources`、`news_articles`、`ingestion_runs`、`schema_migrations` を使用する。
旧開発スキーマの全テーブルを削除済みであるため、`001_initial.sql` と `002_article_search_trigram.sql` はこの名称で新規作成する初期定義とする。
通常画面の JSON API と MCP が共有する日時順検索用インデックスは、後続の `003_mcp_article_order.sql` で追加する。
取得対象の表示順序は、後続の `004_source_display_order.sql` で既存テーブルへ追加する。
記事の保存キーは `005_article_timestamp_identity.sql` で更新日時単位へ変更し、同じ記事の同日時の重複を整理する。
取得対象の種類には `007_fisco_connector.sql` で `fisco` を追加する。

## `news_sources`

取得対象と定期実行状態を保存する。

| 列 | 型 | 用途 |
| --- | --- | --- |
| `id` | `BIGINT` | 主キー |
| `name` | `TEXT` | 管理画面表示名 |
| `kind` | `TEXT` | `rss`、`reuters`、`fisco` のいずれか |
| `endpoint_url` | `TEXT` | RSS URL、Reuters の固定 URL `https://jp.reuters.com`、または Fisco の固定 URL `https://web.fisco.jp/FiscoWeb/news` |
| `enabled` | `BOOLEAN` | 定期取得の有効状態 |
| `fetch_interval_minutes` | `INTEGER` | 取得間隔 |
| `max_items` | `INTEGER` | 1 回の最大取得件数 |
| `settings` | `JSONB` | コネクタ固有設定 |
| `display_order` | `BIGINT` | 管理画面と記事検索条件で共通する表示順序 |
| `next_fetch_at` | `TIMESTAMPTZ` | 次回取得予定 |
| `last_fetched_at` | `TIMESTAMPTZ` | 最終実行完了日時 |
| `last_error` | `TEXT` | 直近エラー |
| `created_at` | `TIMESTAMPTZ` | 作成日時 |
| `updated_at` | `TIMESTAMPTZ` | 更新日時 |
| `deleted_at` | `TIMESTAMPTZ` | 論理削除日時 |

有効な行の名前は大文字小文字を区別せず一意とする。
削除は記事履歴を残すため論理削除とする。
未削除の取得対象は `display_order`、`id` の昇順で安定して表示する。
新規作成時は `display_order` の既定値で末尾へ追加し、利用者が並べ替えたときに未削除行へ 1 から始まる連番を保存する。
定期取得の実行順序は `next_fetch_at` で決まり、`display_order` の影響を受けない。

`settings` はコネクタ種別ごとに必要な項目だけを保存する。

RSS の例:

```json
{
  "fetch_full_article": true
}
```

Reuters の新規保存例:

```json
{
  "section_id": "/economy/"
}
```

RSS は `fetch_full_article`、Reuters はホワイトリストで検証済みの `section_id` だけを使用する。
Reuters の URL は `endpoint_url` に固定値 `https://jp.reuters.com` を保存する。
`website=reuters-japan` と `offset=0` はコード上の固定値とし、Reuters の新規作成・更新時に `settings` へ保存しない。
初期スキーマでは、Reuters の固定 URL と `section_id` をこの形式で保存する。

Fisco の新規保存例:

```json
{}
```

Fisco は固有設定を持たず、`settings` には空の JSON オブジェクトを保存する。
`endpoint_url` は固定値 `https://web.fisco.jp/FiscoWeb/news` とし、一覧の `isRemarkable=false` と詳細 API はコードで固定する。
一覧の `limit` には共通設定 `max_items`（1 以上 50 以下）を使用する。

## `news_articles`

記事は取得対象、外部記事 ID、更新日時の組み合わせごとに 1 行として保存する。

| 列 | 型 | 用途 |
| --- | --- | --- |
| `id` | `BIGINT` | 主キー |
| `source_id` | `BIGINT` | `news_sources.id` |
| `provider` | `TEXT` | 取得元識別子 |
| `external_id` | `TEXT` | 取得元の記事 ID |
| `revision_id` | `TEXT` | 取得元リビジョンまたは内容ハッシュ |
| `canonical_url` | `TEXT` | 記事の正規 URL |
| `published_at` | `TIMESTAMPTZ` | 公開日時 |
| `updated_at` | `TIMESTAMPTZ` | 取得元更新日時 |
| `headline` | `TEXT` | 見出し |
| `body_text` | `TEXT` | プレーンテキスト本文 |
| `raw_payload` | `JSONB` | 取得時のデータ。Reuters / Fisco の上限超過時はサイズと SHA-256 |
| `fetched_at` | `TIMESTAMPTZ` | その行を初めて保存した取得日時。上書き時も維持 |

`(source_id, external_id, COALESCE(updated_at, published_at, '-infinity'::timestamptz))` を一意キーとする。
更新日時がない記事は公開日時を使い、両方ない記事も同じ取得対象・外部記事 ID なら 1 行にまとめる。
アプリ側の事前確認に依存せず、`ON CONFLICT DO UPDATE` で同時実行時も重複を防ぐ。
同じ日時の記事はリビジョン、見出し、本文などを最新の取得内容で上書きし、記事 ID と `fetched_at` は維持する。
日時が異なる場合は別行に保存する。`revision_id` は取得内容の識別と Reuters の再取得省略に使用し、保存行の一意キーには含めない。

RSS の `external_id` は GUID、リンク、安定情報の SHA-256 の順で決める。
RSS にリビジョンがない場合は、見出し、本文、更新日時から SHA-256 を計算する。
Reuters は API の `id` と `revision_id` を使用する。
Fisco は一覧の `newsId` を `external_id` とし、見出し、公開日時、本文から `revision_id` の SHA-256 を計算する。
Fisco の公開日時には一覧の `newsDateTime` を日本時間（UTC+9）として使用し、更新日時は未設定とする。
更新日時を変えずに本文やリビジョンだけが変化した場合も、保存行は増えない。

通常画面の JSON API と MCP では `COALESCE(published_at, fetched_at)` を記事の実効日時として扱う。
公開日時がある記事は公開日時、ない記事は取得日時を日時範囲の判定と並べ替えに使用する。
実効日時の降順、同日時では `id` の降順に並べる。

`conf/retention.toml` の `[retention] enabled` が `true` の場合だけ、記事の保持期間を 7 日（168 時間）に制限する。
既定値は `false` で、設定の省略時も自動削除と保存期限による制限を行わない。同日時の記事の上書きは設定の有無にかかわらず有効とする。
有効時の削除条件は `COALESCE(updated_at, published_at, fetched_at) <= CURRENT_TIMESTAMP - INTERVAL '168 hours'` とし、境界の日時を含む。
更新日時、公開日時、初回取得日時の順に存在する日時で判定し、起動時と 1 時間ごとに期限を過ぎた記事を削除する。
保存時も期限を確認し、期限を過ぎた記事を新たに保存しない。上書き時は `fetched_at` を維持し、日時のない既存行の保持期限を延長しない。
取得対象の設定と取得履歴は削除しない。

## `ingestion_runs`

取得実行の監査情報を保存する。

| 列 | 型 | 用途 |
| --- | --- | --- |
| `id` | `BIGINT` | 主キー |
| `source_id` | `BIGINT` | `news_sources.id` |
| `trigger_type` | `TEXT` | `scheduled`、`manual`、`cli` |
| `status` | `TEXT` | `running`、`succeeded`、`partial`、`failed` |
| `started_at` | `TIMESTAMPTZ` | 開始日時 |
| `finished_at` | `TIMESTAMPTZ` | 終了日時 |
| `fetched_count` | `INTEGER` | コネクタが返した記事数 |
| `inserted_count` | `INTEGER` | 新規保存数 |
| `skipped_count` | `INTEGER` | 上書き、変更なし、保持設定が有効な場合の期限切れなど、新規保存を省略した件数 |
| `warning_count` | `INTEGER` | 個別警告数 |
| `error_message` | `TEXT` | エラーまたは警告要約 |

## マイグレーション

SQL はバイナリへ埋め込み、起動設定 `postgres.auto_migrate` が有効な場合にファイル名順で適用する。
手動適用はプロジェクトルートで `go run . migrate -conf conf` を実行する。
複数プロセスからの同時適用は PostgreSQL advisory lock で直列化する。
適用済みファイルは `schema_migrations` で名前と SHA-256 を管理し、同じマイグレーションを再実行しない。
適用済み SQL の変更はチェックサム不一致として起動エラーにするため、新しい変更は連番ファイルとして追加する。
今回のテーブル命名変更は開発 DB の全テーブル削除後に行ったため、初期マイグレーション `001_initial.sql` と
検索索引マイグレーション `002_article_search_trigram.sql` を新名称へ直接更新し、リネーム用マイグレーションは追加しない。
旧開発スキーマの `schema_migrations` まで削除されていない DB では、更新後の初期マイグレーションを適用せず、DB 状態を確認してから再作成する。
チェックサム導入前の開発版 DB に `checksum IS NULL` の履歴がある場合は自動で信用せず停止する。
その場合はスキーマを検証して履歴へ正しい SHA-256 を設定するか、開発 DB を再作成してから移行する。

記事の部分一致検索には `pg_trgm` の GIN インデックスを使用する。
`002_article_search_trigram.sql` が拡張と見出し・本文用インデックスを作成する。
この索引作成はトランザクション内の通常 `CREATE INDEX` である。
既存記事が多い DB へ導入する場合は書き込み停止時間と 10 分のマイグレーション期限を事前に検証する。

`003_mcp_article_order.sql` は、通常画面の JSON API と MCP が共有する keyset pagination と並べ替えに対応する次の式インデックスを作成する。

- `news_articles_effective_at_idx`: `COALESCE(published_at, fetched_at) DESC, id DESC`
- `news_articles_source_effective_at_idx`: `source_id, COALESCE(published_at, fetched_at) DESC, id DESC`

前者は全取得対象の記事検索、後者は `source_id` を指定した記事検索で使用する。
日時と記事 ID の組をカーソルに含めることで、`COUNT` と `OFFSET` を使わずに次ページを取得する。
`003_mcp_article_order.sql` もトランザクション内の通常 `CREATE INDEX` で適用するため、
既存記事が多い DB へ初めて適用する場合は書き込み停止時間と 10 分のマイグレーション期限を事前に検証する。

`004_source_display_order.sql` は `news_sources.display_order` を `BIGINT NOT NULL` として追加する。
既定値には `BIGINT` の最大値を使用し、新しいバイナリだけでなく列を指定しない旧バイナリから作成された取得対象も末尾へ配置する。
既存の未削除行は、移行前の管理画面と同じく有効状態、設定名、ID の順に `ROW_NUMBER` で連番化する。
一覧取得を効率化するため、未削除行だけを対象とする `news_sources_display_order_idx` を
`display_order, id` に作成する。

`005_article_timestamp_identity.sql` は、同じ取得対象・外部記事 ID・保存判定日時の行を
`fetched_at DESC, id DESC` で並べ、先頭の 1 行を残して重複を削除する。
その後、旧制約 `news_articles_source_revision_uq` を削除して更新日時単位の一意インデックスを作成する。
更新日時と公開日時が両方ない既存記事も、同じ取得対象・外部記事 ID ごとに 1 行を残す。

`006_article_retention.sql` は、`COALESCE(updated_at, published_at, fetched_at)` の式インデックスを作成する。
索引の作成だけでは記事を削除せず、保持設定が有効な場合の期限検索に使用する。適用済み SQL のチェックサムは維持する。

`007_fisco_connector.sql` は `news_sources_kind_check` を置き換え、`rss`、`reuters` に加えて `fisco` を許可する。
既存の取得対象と記事は保持し、適用済みの初期マイグレーションは変更しない。
