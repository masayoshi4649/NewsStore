# NewsStore

RSS / Atom と Reuters からニュース記事の見出し・本文を取得し、PostgreSQL へリビジョン単位で保存する Go アプリです。
Gin 製の管理画面から取得対象、記事、取得履歴を管理できます。
通常の記事画面は同一オリジンの JSON API を Axios から呼び出して表示します。
保存済み記事は、同じ HTTP サーバーの読み取り専用 MCP から参照できます。

## 主な機能

- `conf/*.toml` のファイル名順マージと厳格な設定検証
- PostgreSQL マイグレーションの自動適用
- 2 語の `snake_case` に統一した PostgreSQL テーブル名
- PostgreSQL advisory lock による複数プロセス間の重複取得防止
- RSS 2.0 / Atom の見出し・本文取得
- 任意設定による RSS リンク先の本文抽出
- Reuters の一覧・詳細 API を使った見出し・本文取得
- DB 一意制約と `ON CONFLICT` による冪等保存
- 取得間隔に基づく定期実行
- キューを使った非同期の手動取得
- CSRF、HTML 自動エスケープを備えた管理画面
- バイナリへ埋め込んだローカル Bulma と既定のダークテーマによる管理画面表示
- ローカル Axios、Grid.js、JSON API による記事一覧・詳細の非同期表示
- MCP と共通の複数条件 AND 検索、日時順カーソル、本文分割取得
- コネクタ種別に応じた RSS / Reuters 固有入力欄の排他表示
- ステートレスな JSON 応答による読み取り専用 MCP
- 取得対象名の一覧、複数条件の AND 検索、本文分割取得による軽量な記事参照

設計情報は [docs/architecture.md](docs/architecture.md)、
[docs/database.md](docs/database.md)、
[docs/connectors.md](docs/connectors.md)、
[docs/web-api.md](docs/web-api.md)、
[docs/mcp.md](docs/mcp.md) を参照してください。

## 必要な環境

- Go 1.24.2 以上
- PostgreSQL
- 取得先へ接続できるネットワーク
- 管理画面へ接続できる端末を制限した閉域ネットワーク

## 設定

実行設定は `conf/system.toml`、MCP 設定は `conf/mcp.toml`、PostgreSQL 接続設定は `conf/postgres.toml` に分けて配置します。
全設定の記載例は `conf/conf.toml.sample` を参照してください。
`conf/postgres.toml` はパスワードを含むため Git 管理対象外です。

複数ファイルがある場合はファイル名昇順で読み込み、後のファイルで指定した項目だけを上書きします。
通常は次の責務で分割します。

- `conf/system.toml`: `[SYSTEM]` の待受ポート
- `conf/mcp.toml`: `[MCP]` の無条件検索時の既定記事件数
- `conf/postgres.toml`: `[postgres]`

`conf/system.toml`:

```toml
[SYSTEM]
Port = 8080
```

`conf/mcp.toml`:

```toml
[MCP]
DefaultArticleLimit = 20
```

`DefaultArticleLimit` は 1 以上 100 以下で指定します。
`conf/system.toml` には `[SYSTEM] Port` 以外を追加しません。

`conf/postgres.toml` の最小例:

```toml
[postgres]
host = "127.0.0.1"
port = 5432
username = "newsstore"
password = "change-me"
dbname = "newsstore"
sslmode = "require"
```

外部 HTTP は既定で、ループバック、プライベート、リンクローカル、共有、文書用、ベンチマーク、予約済み等の特殊用途アドレス宛を接続直前に拒否します。

管理画面と MCP は閉域ネットワーク内での利用を前提とします。
HTTP サーバーは `conf/system.toml` の `Port` を `:Port` 形式の待受アドレスへ変換し、既定の `:8080` で全インターフェースに待ち受けます。
アプリケーション内に利用者認証は設けていないため、インターネットへ直接公開せず、ファイアウォール、VPN、接続元制限などで到達可能な端末を管理してください。
管理画面の状態変更には、閉域網内でも CSRF 検証を適用します。

PostgreSQL は既定で `sslmode = "require"` です。
TLS 非対応のローカル開発 DB に限り、明示的に `sslmode = "disable"` へ変更してください。
初回マイグレーションでは記事の部分一致検索用に `pg_trgm` 拡張を作成します。
DB ユーザーには対象データベースで拡張を作成できる権限が必要です。

## 起動

プロジェクトルートで次のコマンドを実行します。

```powershell
go run .
```

同じ端末から確認する場合は、ブラウザーで `http://localhost:8080/` を開きます。
トップ URL `/` とロゴは記事一覧 `/admin/articles` へ遷移します。
閉域ネットワーク内の別端末からは、`localhost` を NewsStore 稼働端末の IP アドレスへ置き換えてください。
有効な取得対象は作成後すぐに定期取得キューへ入ります。

管理画面はダークテーマを既定とします。
`web/lib` のローカル Bulma、Axios、Grid.js をバイナリへ埋め込んで配信するため、CDN への接続は不要です。
取得対象の編集画面ではコネクタ種別に応じた固有欄だけを表示し、非表示側の入力は無効化します。

### 通常画面の記事参照

記事一覧 `http://localhost:8080/admin/articles` と記事詳細 `/admin/articles/:id` は、空の HTML シェルを返した後、ローカルの Axios から同一オリジンの JSON API を呼び出して描画します。

- `GET /api/sources`: 未削除の取得対象について `news_sources.id`、設定名、種類、有効状態を返す
- `GET /api/articles`: 取得対象、入力順を問わず両端を含む日時範囲、最大 5 個の検索語を AND 条件で検索する
- `GET /api/articles/:id`: 記事本文を既定 8,000 文字、最大 16,000 文字ずつ返す

通常画面の検索条件、実効日時の新しい順、日時と記事 ID によるカーソル、本文分割方法は MCP と共通です。
条件と最大件数はすべて省略でき、件数を省略した場合は `conf/mcp.toml` の `DefaultArticleLimit` を使います。
検索画面の最大件数欄には、この `DefaultArticleLimit` の実際の設定値を初期表示します。
検索パネルは既定で閉じ、日時範囲を横並びの `～` で指定します。
両方の日時を指定した場合は、どちらへ早い日時を入力しても、早い方を下限、遅い方を上限として検索します。
片方だけを指定した場合は、左側を下限、右側を上限として扱います。
最大 5 個の検索語は個別の入力欄へ指定します。
一覧は本文を表示せず、1列内にヘッドライン、取得対象、更新日時をまとめて表示します。更新日時がない場合は投稿日時を使用し、API が返した最大件数内を Grid.js で 1 ページ 10 件にローカルページングします。
API の本文先頭 500 文字、keyset cursor、続きの有無は維持しますが、通常画面の一覧には本文抜粋、件数、続き文言、追加取得ボタンを表示しません。
詳細の長い本文は「続きを読む」で続きだけを取得します。
API は `COUNT` と `OFFSET` を使わず、DB 問い合わせ期限を 60 秒とします。

API は閉域ネットワーク内の同一オリジン利用に限定し、認証と CORS は追加しません。
見出しと本文はプレーンテキストとして描画し、元記事リンクは `http` または `https` の URL だけを表示します。
詳しい入出力と制約は [docs/web-api.md](docs/web-api.md) を参照してください。

### MCP による記事参照

MCP の Streamable HTTP エンドポイントは次の URL です。

```text
http://localhost:8080/mcp
```

別端末から接続する場合は `localhost` を NewsStore 稼働端末の IP アドレスへ置き換えてください。
アプリケーション内の認証は設けていないため、管理画面と同じ閉域ネットワーク境界で接続元を制限してください。

MCP は次の読み取り専用 tool だけを公開します。

- `list_sources`: 未削除の取得対象について `news_sources.id`、設定名、種類、有効状態を返す
- `search_articles`: 取得対象、日時範囲、最大 5 個の検索語を組み合わせて記事を検索し、本文先頭 500 文字を返す
- `get_article`: 記事 ID を指定し、本文を既定 8,000 文字、最大 16,000 文字ずつ返す

`search_articles` の条件はすべて省略でき、無条件時は `conf/mcp.toml` の既定件数を最新記事から返します。
指定した検索語はすべて AND 条件になり、各語は見出しまたは本文の部分一致で判定します。
日時は公開日時を優先し、未設定の記事は取得日時を使って両端を含む範囲検索と新しい順の並べ替えを行います。
日時を両方指定した場合は入力順を問わず、早い方を下限、遅い方を上限へ自動的に正規化します。
片方だけを指定した場合は、`date_from` を下限、`date_to` を上限として扱います。
最大件数は 1 件から 100 件まで指定できます。
ページングは日時と記事 ID を組み合わせた不透明カーソル方式とし、全件数の集計や `OFFSET` を行いません。
詳細本文も PostgreSQL で必要な範囲だけを切り出すため、長い記事を一度にメモリーへ読み込みません。
セッション、SSE、resources、prompts、MCP のログ配信機能は使用しません。
詳しい入出力と制約は [docs/mcp.md](docs/mcp.md) を参照してください。

### Reuters の登録例

- コネクタ: `Reuters`
- 取得内容: ホワイトリストから選択
- 最大取得件数: 必要な件数

Reuters では URL や内部 API 設定を入力しません。
取得先 URL は `https://jp.reuters.com`、Web サイト識別子は `reuters-japan`、一覧の開始位置は `0` に固定し、一覧 API と詳細 API もコードで固定します。
DB の `news_sources.endpoint_url` には固定 URL を保持し、`settings` には選択結果に対応する `section_id` だけを新規保存します。

選択できる取得内容は次のとおりです。

| 取得内容 | `section_id` | 根拠 |
| --- | --- | --- |
| 経済 | `/economy/` | 参照現行実装で取得を実証済み |
| 日本 | `/world/japan/` | 現行一覧 API で取得確認済み |
| ワールド | `/world/` | 同上 |
| 欧州 | `/world/europe/` | 同上 |
| ウクライナ情勢 | `/world/ukraine/` | 同上 |
| ロシア | `/world/russia/` | 同上 |
| 中東・アフリカ | `/world/mideast/` | 同上 |
| 特集 安全保障問題 | `/world/security/` | 同上 |

全取得内容は現行一覧 API で記事一覧を確認済みですが、Reuters 側の仕様変更や公開状況によって取得できなくなる場合があります。
旧版で保存された日本セクション `/ja/` は、取得時に現行値 `/world/japan/` へ自動変換します。
Reuters コネクタは、`masayoshi4649/GetArticleFromReuters` で確認した通信仕様を基に独立実装しています。
利用する API は公開 Web の内部 API であり、Reuters の仕様変更で取得できなくなる可能性があります。

### RSS / Atom の登録

取得先 URL へフィード URL を指定します。
「リンク先ページから本文を抽出する」を有効にすると、リンク先の `article`、`main`、`body` 内の段落を順に探索します。
抽出できない場合はフィード内の本文または説明へフォールバックします。
RSS / Atom を選択している間は Reuters 固有欄を表示せず、Reuters を選択している間は RSS 固有欄を表示しません。

取得先サイトの利用規約、robots 方針、適切な取得間隔を確認したうえで登録してください。

## コマンド

マイグレーションだけを実行:

```powershell
go run . migrate -conf conf
```

取得対象 ID を指定して同期実行:

```powershell
go run . fetch -conf conf -source-id 1
```

ヘルスチェック:

```text
GET /healthz
```

DB 接続を含めて正常な場合は HTTP 200 を返します。

## テストと配布ビルド

ソースコードを変更した後は、プロジェクトルートでテストに続けて配布ビルドを必ず実行します。

```powershell
.\test.ps1
.\build.ps1
```

`build.ps1` は `dist` フォルダ全体を作り直し、Windows と Linux x64 を独立して持ち出せる次の構成を生成します。

| 対象 | 実行ファイル |
| --- | --- |
| Windows x64 (`windows/amd64`) | `dist/Windows/newsstore.exe` |
| Linux x64 (`linux/amd64`) | `dist/linux/newsstore` |

各フォルダには、実行ファイルに加えて設定サンプル `conf/conf.toml.sample`、配布用 `README.md`、`LICENSE` を配置します。
実際の `system.toml`、`mcp.toml`、`postgres.toml` は認証情報を含み得るため、`dist` へコピーしません。

```text
dist/
├── Windows/
│   ├── newsstore.exe
│   ├── conf/
│   │   └── conf.toml.sample
│   ├── README.md
│   └── LICENSE
└── linux/
    ├── newsstore
    ├── conf/
    │   └── conf.toml.sample
    ├── README.md
    └── LICENSE
```

配布ビルドは `CGO_ENABLED=0`、`-trimpath`、`-ldflags "-s -w"` を使用します。両方のビルドと必要ファイルの準備に成功した後でだけ、既存の `dist` と入れ替えます。
`dist/conf` ではなく各 OS フォルダ内の `conf` を使用するため、配布先では対象 OS のフォルダへ移動してから実行してください。
初回起動前に `conf/conf.toml.sample` を `conf/conf.toml` へコピーし、接続先に合わせて編集します。

テストを個別に実行する場合は次のコマンドを使用します。

```powershell
go test ./...
go vet ./...
go mod tidy -diff
go build .
```

通常テストは実 PostgreSQL や外部サイトへ接続しません。

## データの扱い

- 記事は `(source_id, external_id, revision_id)` 単位で保存します。
- 同じ記事の同じリビジョンは再実行しても増えません。
- 別リビジョンは履歴として別行に保存します。
- 取得対象の削除は論理削除で、保存済み記事と取得履歴を保持します。
- 本文はプレーンテキストとして保存・表示します。
