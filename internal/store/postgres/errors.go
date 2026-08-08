package postgres

import "errors"

var (
	// ErrNotFound は、指定された永続化データが存在しないことを表します。
	ErrNotFound = errors.New("対象データが見つかりません")
	// ErrInvalidSourceOrder は、取得対象の表示順序が現在の未削除一覧と一致しないことを表します。
	ErrInvalidSourceOrder = errors.New("取得対象の表示順序が現在の一覧と一致しません")
)
