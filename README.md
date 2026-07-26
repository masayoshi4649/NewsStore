# NewsStore

RSS / Atom と Reuters からニュース記事の見出し・本文を取得し、PostgreSQL へリビジョン単位で保存する Go アプリです。
Gin 製の管理画面から取得対象、記事、取得履歴を管理できます。

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
- コネクタ種別に応じた RSS / Reuters 固有入力欄の排他表示

設計情報は [docs/architecture.md](docs/architecture.md)、
[docs/database.md](docs/database.md)、
[docs/connectors.md](docs/connectors.md) を参照してください。

## 必要な環境

- Go 1.24.2 以上
- PostgreSQL
- 取得先へ接続できるネットワーク
- 管理画面へ接続できる端末を制限した閉域ネットワーク

## 設定

実行設定は `conf/system.toml`、PostgreSQL 接続設定は `conf/postgres.toml` に分けて配置します。
全設定の記載例は `conf/conf.toml.sample` を参照してください。
`conf/postgres.toml` はパスワードを含むため Git 管理対象外です。

複数ファイルがある場合はファイル名昇順で読み込み、後のファイルで指定した項目だけを上書きします。
通常は次の責務で分割します。

- `conf/system.toml`: `[SYSTEM]` の待受ポート
- `conf/postgres.toml`: `[postgres]`

`conf/system.toml`:

```toml
[SYSTEM]
Port = 8080
```

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

管理画面は閉域ネットワーク内での利用を前提とします。
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
トップ URL `/` は取得対象一覧 `/admin/sources` へリダイレクトされます。
閉域ネットワーク内の別端末からは、`localhost` を NewsStore 稼働端末の IP アドレスへ置き換えてください。
有効な取得対象は作成後すぐに定期取得キューへ入ります。

管理画面はダークテーマを既定とします。
`web/lib/bulma` のローカル Bulma をバイナリへ埋め込んで配信するため、CDN への接続は不要です。
取得対象の編集画面ではコネクタ種別に応じた固有欄だけを表示し、非表示側の入力は無効化します。

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

## テスト

```powershell
./test.ps1
```

または個別に実行します。

```powershell
go test ./...
go vet ./...
go build .
```

通常テストは実 PostgreSQL や外部サイトへ接続しません。

## データの扱い

- 記事は `(source_id, external_id, revision_id)` 単位で保存します。
- 同じ記事の同じリビジョンは再実行しても増えません。
- 別リビジョンは履歴として別行に保存します。
- 取得対象の削除は論理削除で、保存済み記事と取得履歴を保持します。
- 本文はプレーンテキストとして保存・表示します。
