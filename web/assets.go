// Package webassets は、NewsStoreのWeb画面で使用するローカルライブラリを提供します。
package webassets

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
)

//go:embed lib
var libraryFiles embed.FS

/*
FileSystem は、バイナリへ埋め込んだWebライブラリをHTTP配信用に返します。

機能:
  - web/lib配下のBulmaなどのローカルライブラリを公開する
  - /libへマウントした後の相対パスを解決できるhttp.FileSystemを生成する

引数:
  - なし

返り値:
  - http.FileSystem: web/lib直下をルートとして読み取るファイルシステム
  - error: 埋め込みファイルシステムのルート取得に失敗した場合のエラー。成功時はnil
*/
func FileSystem() (http.FileSystem, error) {
	libraryRoot, err := fs.Sub(libraryFiles, "lib")
	if err != nil {
		return nil, fmt.Errorf("Webライブラリの埋め込みルートを取得できません: %w", err)
	}
	return http.FS(libraryRoot), nil
}
