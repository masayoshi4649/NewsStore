# コネクタ設計

## 共通契約

各コネクタは DB へ記事を書き込まず、取得対象を受け取って共通形式の記事一覧を返す。
共通記事は少なくとも、取得元 ID、リビジョン ID、見出し、本文を持つ。
見出しまたは本文が空の記事は保存対象にしない。

Reuters コネクタには、保存済みリビジョンの存在だけを問い合わせる抽象インターフェースを注入する。
一覧レスポンスの `(source_id, id, revision_id)` が保存済みなら詳細 API を省略し、本文を毎回取り直さない。
実際の保存と実行履歴更新は引き続き取り込みサービスが担当する。
保存行は取得対象・外部記事 ID・更新日時で識別し、更新日時がない場合は公開日時を使用する。
両日時がない場合も同じ取得対象・外部記事 ID は 1 行にまとめる。
RSS の内容ハッシュや Reuters のリビジョンが変わっても、保存判定に使う日時が同じなら最新の取得内容で上書きする。
日時が異なる場合だけ別行を作成し、上書き時は記事 ID と `fetched_at` を維持する。
`[retention] enabled = true` の場合だけ、保存処理で更新日時、公開日時、初回取得日時の順に保持期限を判定し、168 時間以上前の記事を保存対象外にする。
既定の `false` または未指定の場合は、記事の古さによる保存制限を行わない。同日時上書きはこの設定にかかわらず有効とする。

外部 HTTP には、コンテキスト、タイムアウト、User-Agent、レスポンスサイズ上限を適用する。
一覧取得に失敗した場合は実行全体を失敗とし、一覧中の個別記事だけが失敗した場合は警告として他の記事を継続する。
管理画面を閉域ネットワークで運用する構成でも、外部取得に対する SSRF 防止は維持する。
ループバック、プライベート、リンクローカル、共有、文書用、ベンチマーク、予約済み等の特殊用途アドレスは接続直前に拒否する。

管理画面は選択中のコネクタに対応する固有欄だけを表示する。
非表示側の入力要素は無効化し、選択したコネクタの設定へ別のコネクタの固有値が混入することを防ぐ。
Fisco は固有入力欄を持たず、RSS / Reuters の固有入力欄を両方無効化する。

## RSS/Atom

本文の優先順位は次のとおり。

1. `fetch_full_article=true` の場合に、リンク先の `article` または `main` 要素から抽出した段落
2. RSS の `content:encoded` または Atom の `content`
3. `description`

リンク先取得や抽出が失敗しても、フィード内に本文があれば記事を保存する。
本文は HTML タグを除去し、段落を改行で連結したプレーンテキストとして保存する。
Atom の `content type="text"` は HTML として解釈せず、リテラルテキストとして保持する。
相対記事 URL は、HTTP リダイレクトがあった場合には最終フィード URL を基準に解決する。

## Reuters

### 参照した挙動

[masayoshi4649/GetArticleFromReuters](https://github.com/masayoshi4649/GetArticleFromReuters) の現在の挙動を仕様確認に使った。
参照元は AGPL-3.0 のためコードはコピーせず、以下の通信・マッピング仕様から独立して実装する。

### 一覧 API

```text
GET https://jp.reuters.com/pf/api/v3/content/fetch/articles-by-section-alias-or-id-v1
```

`query` パラメーターへ次の JSON を URL エンコードして渡す。

```json
{
  "arc-site": "reuters-japan",
  "fetch_type": "collection",
  "offset": 0,
  "section_id": "/economy/",
  "size": 5,
  "website": "reuters-japan"
}
```

取得先 URL は `https://jp.reuters.com`、`arc-site` と `website` は `reuters-japan`、`offset` は `0` にコードで固定する。
`section_id` だけを管理画面の取得内容ホワイトリストから決め、`size` には取得対象の共通設定 `max_items` を使う。

### 詳細 API

```text
GET https://jp.reuters.com/pf/api/v3/content/fetch/article-by-id-or-url-v1
```

一覧に `id` がある場合は `id` と固定値の `website=reuters-japan` を渡す。
`id` がない場合は `website_url` と同じ固定 `website` を渡す。
一覧 API 名と詳細 API 名は利用者入力や DB 設定から変更せず、コードで固定する。

### 取得内容ホワイトリスト

利用者は管理画面の「取得内容」だけを次のホワイトリストから選択する。

| 画面表示 | 識別子 | `section_id` | 根拠 |
| --- | --- | --- | --- |
| 経済 | `economy` | `/economy/` | 参照現行実装で取得を実証済み |
| 日本 | `japan` | `/world/japan/` | 現行一覧 API で取得確認済み |
| ワールド | `world` | `/world/` | 同上 |
| 欧州 | `europe` | `/world/europe/` | 同上 |
| ウクライナ情勢 | `ukraine` | `/world/ukraine/` | 同上 |
| ロシア | `russia` | `/world/russia/` | 同上 |
| 中東・アフリカ | `mideast` | `/world/mideast/` | 同上 |
| 特集 安全保障問題 | `security` | `/world/security/` | 同上 |

各セクションは一覧 API への実通信で HTTP 200 と記事一覧を確認しているが、内部 API のため継続提供を保証するものではない。
画面からは識別子を送信し、サーバー側で対応する `section_id` へ変換してホワイトリスト外の値を拒否する。
旧版で保存された日本セクション `/ja/` は、読み込み時に現行値 `/world/japan/` へ変換する。

### マッピング

| 共通記事 | Reuters 詳細レスポンス |
| --- | --- |
| `provider` | `reuters` 固定 |
| `external_id` | `result.id` |
| `revision_id` | `result.revision_id` |
| `canonical_url` | `result.canonical_url` |
| `published_at` | `result.published_time` |
| `updated_at` | `result.updated_time` |
| `headline` | `result.title` |
| `body_text` | `dateline[]` と `content_elements[type=paragraph].content` |

日時は RFC 3339 Nano として解析する。
本文は空要素を除き、データライン、段落の順に改行で連結する。

### 変更耐性

この API は Reuters 公開 Web が利用する内部 API であり、安定した公式 REST API 契約ではない。
本番のベース URL、Web サイト識別子、開始位置、一覧 API、詳細 API はコードで固定する。
DB の `news_sources.endpoint_url` には固定 URL を保持し、新規作成・更新時の `settings` には選択された `section_id` だけを保存する。
固定 JSON を用いた単体テストでレスポンス構造の差異を検出する。

## Fisco

### 一覧 API

```text
GET https://web.fisco.jp/FiscoWeb/news?isRemarkable=false&limit=10
```

一覧 URL と `isRemarkable=false` はコードで固定する。
`limit` には取得対象の共通設定 `max_items` を使用し、1 以上 50 以下で指定する。
カテゴリや取得内容の選択は行わず、取得先 URL も利用者に入力させない。
レスポンスの `data` 配列から次の項目を取得する。

```json
{
  "data": [
    {
      "newsId": "0009340020260908019",
      "newsTitle": "記事のタイトル",
      "newsDateTime": "20260908173220"
    }
  ]
}
```

### 詳細 API

```text
GET https://web.fisco.jp/FiscoWeb/news/<newsId>
```

一覧の `newsId` を固定 URL の末尾へ付け、レスポンスの `news.newsReport` を本文として取得する。
一覧には本文のリビジョン識別子がないため、保存済みの記事も毎回詳細 API を取得する。

### マッピング

| 共通記事 | Fisco の取得値 |
| --- | --- |
| `provider` | `fisco` 固定 |
| `external_id` | 一覧の `newsId` |
| `revision_id` | 見出し、公開日時、本文から計算した SHA-256 |
| `canonical_url` | `https://web.fisco.jp/platform/market-news/<newsId>` |
| `published_at` | 一覧の `newsDateTime` |
| `updated_at` | 未設定 |
| `headline` | 一覧の `newsTitle` |
| `body_text` | 詳細の `news.newsReport` |

日時は `yyyyMMddHHmmss` 形式を日本時間（UTC+9）として解析する。
たとえば `20260908173220` は `2026-09-08T17:32:20+09:00` になる。
見出しと本文の前後空白を除去し、本文内の改行や銘柄コードの `<6905>` などはプレーンテキストとして保持する。
`raw_payload` には詳細レスポンスを保存し、512 KiB を超える場合は元のサイズと SHA-256 のメタデータを保存する。

### 設定

取得先の一覧 API と詳細 API はコードで固定する。
DB の `news_sources.endpoint_url` には固定値 `https://web.fisco.jp/FiscoWeb/news` を保存する。
新規作成・更新時の `settings` は空の JSON オブジェクト `{}` とし、RSS / Reuters の設定を保存しない。
