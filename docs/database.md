# データベース設計

テーブル名は 2 語の `snake_case` に統一し、`news_sources`、`news_articles`、`ingestion_runs`、`schema_migrations` を使用する。
旧開発スキーマの全テーブルを削除済みであるため、`001_initial.sql` と `002_article_search_trigram.sql` はこの名称で新規作成する初期定義とする。

## `news_sources`

取得対象と定期実行状態を保存する。

| 列 | 型 | 用途 |
| --- | --- | --- |
| `id` | `BIGINT` | 主キー |
| `name` | `TEXT` | 管理画面表示名 |
| `kind` | `TEXT` | `rss` または `reuters` |
| `endpoint_url` | `TEXT` | RSS URL、または Reuters の固定 URL `https://jp.reuters.com` |
| `enabled` | `BOOLEAN` | 定期取得の有効状態 |
| `fetch_interval_minutes` | `INTEGER` | 取得間隔 |
| `max_items` | `INTEGER` | 1 回の最大取得件数 |
| `settings` | `JSONB` | コネクタ固有設定 |
| `next_fetch_at` | `TIMESTAMPTZ` | 次回取得予定 |
| `last_fetched_at` | `TIMESTAMPTZ` | 最終実行完了日時 |
| `last_error` | `TEXT` | 直近エラー |
| `created_at` | `TIMESTAMPTZ` | 作成日時 |
| `updated_at` | `TIMESTAMPTZ` | 更新日時 |
| `deleted_at` | `TIMESTAMPTZ` | 論理削除日時 |

有効な行の名前は大文字小文字を区別せず一意とする。
削除は記事履歴を残すため論理削除とする。

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

## `news_articles`

記事は 1 リビジョンを 1 行として保存する。

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
| `raw_payload` | `JSONB` | 取得時の正規化データ。Reuters の上限超過時はサイズと SHA-256 |
| `fetched_at` | `TIMESTAMPTZ` | 取得日時 |

`(source_id, external_id, revision_id)` を一意制約とする。
アプリ側の事前確認に依存せず、`ON CONFLICT DO NOTHING` で同時実行時も冪等にする。

RSS の `external_id` は GUID、リンク、安定情報の SHA-256 の順で決める。
RSS にリビジョンがない場合は、見出し、本文、更新日時から SHA-256 を計算する。
Reuters は API の `id` と `revision_id` を使用する。

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
| `skipped_count` | `INTEGER` | 重複数 |
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
