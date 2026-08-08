# 管理画面の記事参照 API

## 目的

管理画面の記事一覧 `/admin/articles` と記事詳細 `/admin/articles/:id` は、記事データを HTML へ埋め込まず、空の画面シェルだけを返す。
ブラウザーはローカル配信する Axios `/lib/axios/axios.min.js`、Grid.js `/lib/gridjs/gridjs.umd.js`、画面スクリプト `/static/articles.js` を読み込み、同一オリジンの JSON API から記事データを取得して描画する。

記事検索と本文分割取得には MCP と共通の PostgreSQL Repository を使う。
これにより、通常画面と MCP で検索条件、並び順、件数制限が異ならないようにする。
API と MCP の keyset cursor は維持するが、通常画面は 1 回の API 応答内を Grid.js でローカルページングする。

## 画面の責務

### 記事一覧

`GET /admin/articles` はナビゲーション、既定で閉じた検索パネル、空の一覧領域を含む HTML を返す。
取得対象や記事は SSR せず、画面表示後に次の順で取得する。

1. `GET /api/sources` から取得対象の選択肢を読み込む。
2. `GET /api/articles` を検索条件付きで呼び出す。
3. API が返した最大件数内の記事を、実効日時の新しい順を維持して Grid.js へ渡す。
4. Grid.js のローカルページングで 1 ページ 10 件ずつ表示する。

検索条件はすべて省略できる。
画面の条件を消去した場合は無条件検索へ戻り、`conf/mcp.toml` の `[MCP] DefaultArticleLimit` 件を最新記事から表示する。

一覧はGrid.jsの1列内にヘッドライン、取得対象、更新日時をまとめて表示し、API が返す `body_excerpt` は表示しない。
更新日時は `updated_at` を優先し、値がない場合は `published_at` を投稿日時として表示する。どちらもない場合はダッシュ表示とする。
通常画面から `next_cursor` による追加取得は行わず、「もっと読む」ボタン、取得件数、続きの有無を示す文言も表示しない。
検索パネルは必要なときだけ開ける形式とし、日時の開始と終了を `～` で横並びにする。
検索語は最大数の説明を添えた 1 欄ではなく、5 個の個別入力欄を縦に並べ、最大件数は狭い数値入力欄とする。
最大件数欄は「既定」というプレースホルダーではなく、`DefaultArticleLimit` の実際の設定値を初期表示する。検索語欄は見出しの「AND」表記だけで意味を示し、同内容の補足文は表示しない。

### 記事詳細

`GET /admin/articles/:id` はパスの記事 ID だけを画面初期化に使用し、記事本文やメタデータを SSR しない。
画面表示後に `GET /api/articles/:id` から先頭の本文断片を取得する。

本文に続きがある場合は「続きを読む」を表示する。
クリック時は直前の `next_body_offset` を `body_offset` に指定し、取得済み本文へ次の断片を追加する。
一度に本文全体を取得しないため、長い記事でもブラウザー、Go プロセス、PostgreSQL のメモリー使用量を抑える。
追加本文は既存本文全体を再設定せず、安全なテキストノードとして末尾へ追加する。

### ブラウザー描画

API から受け取った文字列は `innerHTML` へ渡さない。
一覧のヘッドラインは Grid.js の仮想 DOM へテキスト子として渡し、本文や取得対象名は `textContent` で描画する。
元記事リンクは URL の解析に成功し、スキームが `http` または `https` の場合だけ表示する。
リンクは別タブで開き、遷移先へブラウザー操作権限や不要な参照元情報を渡さない。

API 要求中だけ読み込み状態を表示し、完了後に読み込み文言を残さない。
該当記事なし、入力エラー、API エラーはそれぞれ画面上で区別する。
API の内部エラー詳細、SQL、PostgreSQL 接続情報は画面へ表示しない。

## 共通仕様

- API はすべて `GET` とする。
- 応答は JSON とし、`Cache-Control: no-store` を付ける。
- 管理画面と同じ閉域ネットワークで利用し、アプリケーション内の認証は設けない。
- Axios は同一オリジンの相対 URL だけを呼び出し、CORS は有効化しない。
- DB 問い合わせ期限は 60 秒とする。
- 3 API は共通の最大 8 実行枠を使い、満杯時は待機せず `Retry-After: 1` 付きの HTTP 503 を返す。
- 入力不正は HTTP 400、対象なしは HTTP 404、DB 問い合わせ期限超過は HTTP 504 とする。
- 予期しない内部エラーはサーバー側へ記録し、HTTP 500 の本文には一般化したメッセージだけを含める。
- `news_articles.raw_payload` はどの API 応答にも含めない。

エラー応答は次の形式とする。

```json
{
  "error": "利用者へ公開できるエラーメッセージ"
}
```

## `GET /api/sources`

記事検索条件として選択できる取得対象を返す。
`news_sources.deleted_at IS NULL` の設定を対象とし、保存済み記事を検索できるよう、現在無効な設定も除外しない。
取得対象画面で保存した表示順で返し、同じ表示順の場合は `id` の昇順で安定させる。
記事画面の検索条件ドロップダウンは、この配列順をそのまま選択肢の順序として使用する。

応答例:

```json
{
  "sources": [
    {
      "id": 1,
      "name": "Reuters 経済",
      "kind": "reuters",
      "enabled": true
    }
  ]
}
```

設定は `id`、設定名、種類、有効状態を返す。

## `GET /api/articles`

保存済み記事を MCP と同じ条件と順序で検索する。

### クエリ

| 名前 | 型 | 必須 | 内容 |
| --- | --- | --- | --- |
| `source_id` | 1 以上の整数 | いいえ | `news_sources.id`。指定した取得対象の記事だけを検索 |
| `date_from` | RFC3339 文字列 | いいえ | 単独指定時の実効日時の下限。指定日時と同じ記事を含む |
| `date_to` | RFC3339 文字列 | いいえ | 単独指定時の実効日時の上限。指定日時と同じ記事を含む |
| `query` | 文字列 | いいえ | 単一の検索語 |
| `keywords` | 反復可能な文字列 | いいえ | 追加の検索語。`keywords[]` の指定も受け付ける |
| `limit` | 1～100 の整数 | いいえ | 返す最大件数 |
| `cursor` | 文字列 | いいえ | 直前の応答が返した不透明な `next_cursor` |

`query`、反復する `keywords`、反復する `keywords[]` を結合し、空文字と大文字小文字を区別しない重複を除外する。
正規化後の検索語は最大 5 個とし、すべての語を AND 条件で結合する。
各語は、見出しまたは本文のどちらかに部分一致すればその語へ一致したものとする。
`%`、`_`、`\` は SQL のワイルドカードではなく通常文字として扱う。

取得対象、日時範囲、検索語は任意に組み合わせられ、すべて指定することも、すべて省略することもできる。
`date_from` と `date_to` の両方を指定した場合は入力順を問わず、早い日時を下限、遅い日時を上限へ自動的に正規化し、両端を検索対象に含める。
片方だけを指定した場合は、`date_from` を下限、`date_to` を上限として扱う。
管理画面も同じ正規化を使用するため、開始欄と終了欄のどちらへ早い日時を入力しても検索結果は同じになる。
`limit` 省略時は `conf/mcp.toml` の `[MCP] DefaultArticleLimit` を使う。

日時範囲と並べ替えには、公開日時を優先し、ない場合は取得日時を使う次の実効日時を使用する。

```sql
COALESCE(published_at, fetched_at)
```

実効日時の降順、同日時では記事 ID の降順に並べる。
一致件数が `limit` を超える場合は、この順序で新しい記事を優先して返す。

### ページング

ページングは実効日時と記事 ID を組み合わせた不透明な keyset cursor で行う。
SQL は `limit + 1` 件だけを取得して続きの有無を判定し、全件の `COUNT` と `OFFSET` は実行しない。

`has_more` が `true` の場合は、検索条件を維持したまま `next_cursor` を次の要求の `cursor` へ指定する。
カーソル自体に期限は設けないが、記事の追加や削除後に同じ結果集合が維持されることは保証しない。

### 応答

応答例:

```json
{
  "articles": [
    {
      "id": 101,
      "source_id": 1,
      "source_name": "Reuters 経済",
      "provider": "reuters",
      "external_id": "2HRC2V6GORIUDPPFE7LY7IKM7M",
      "revision_id": "1",
      "canonical_url": "https://jp.reuters.com/example/",
      "published_at": "2026-07-26T08:00:00Z",
      "updated_at": "2026-07-26T08:30:00Z",
      "headline": "記事見出し",
      "body_excerpt": "本文先頭の抜粋",
      "fetched_at": "2026-07-26T08:31:00Z",
      "effective_at": "2026-07-26T08:00:00Z"
    }
  ],
  "has_more": true,
  "next_cursor": "不透明なカーソル"
}
```

本文抜粋は PostgreSQL の `LEFT` で先頭 500 Unicode 文字だけを読み出す。
記事一覧へ本文全体を載せない。
通常画面はこの `body_excerpt` を描画せず、ヘッドライン、取得対象、更新日時を Grid.js の1列へまとめて渡す。
`has_more` と `next_cursor` は API と MCP のページング互換性のため維持するが、通常画面の Grid.js ページングには使用しない。

## `GET /api/articles/:id`

記事 ID と本文断片の位置を指定して、記事メタデータと本文の一部を返す。
本文取得には MCP の `get_article` と共通の Repository を使用する。

### クエリ

| 名前 | 型 | 必須 | 既定値 | 内容 |
| --- | --- | --- | --- | --- |
| `body_offset` | 0～10,000,000 の整数 | いいえ | `0` | 本文先頭から読み飛ばす Unicode 文字数 |
| `body_limit` | 1～16,000 の整数 | いいえ | `8,000` | 1 回に返す最大 Unicode 文字数 |

Repository は PostgreSQL の `SUBSTRING` で指定範囲と続き判定用の 1 文字だけを読み出す。
本文のバイト位置ではなく Unicode 文字位置として扱う。

応答例:

```json
{
  "article": {
    "id": 101,
    "source_id": 1,
    "source_name": "Reuters 経済",
    "provider": "reuters",
    "external_id": "2HRC2V6GORIUDPPFE7LY7IKM7M",
    "revision_id": "1",
    "canonical_url": "https://jp.reuters.com/example/",
    "published_at": "2026-07-26T08:00:00Z",
    "updated_at": "2026-07-26T08:30:00Z",
    "headline": "記事見出し",
    "body_text": "指定位置から取得した本文",
    "body_offset": 0,
    "has_more": true,
    "next_body_offset": 8000,
    "fetched_at": "2026-07-26T08:31:00Z",
    "effective_at": "2026-07-26T08:00:00Z"
  }
}
```

続きがなければ `has_more` は `false` となり、`next_body_offset` は省略する。

## 検索性能

実効日時と記事 ID の並びには `003_mcp_article_order.sql` の式インデックスを使用する。
取得対象を指定した場合も専用の複合式インデックスを利用できる。

部分一致検索は `002_article_search_trigram.sql` が作成する `pg_trgm` の GIN インデックスを利用する。
1～2 文字の検索語は有効な trigram を作れず、記事数が多い環境では索引の効果が限定される。
短い検索語を使う場合は、取得対象または日時範囲を併用して走査対象を狭めることを推奨する。

## 提供しない機能

- 書き込み API
- CORS による別オリジンからの呼び出し
- 全件数の集計
- オフセットページング
- OR 検索
- 関連度や類似度による並べ替え
- 日本語形態素解析による単語検索
