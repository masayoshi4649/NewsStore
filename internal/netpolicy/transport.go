// Package netpolicy は、外部 HTTP が内部ネットワークへ到達しないための接続制限を提供します。
package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// blockedNetworks は、標準のIP判定で除外されない特殊用途アドレス空間です。
var blockedNetworks = mustNetworks(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"5f00::/16",
)

/*
NewTransport は、外部記事取得向けの HTTP Transport を生成します。

機能:
  - 環境プロキシを使用せず取得先へ直接接続する
  - DNS 解決したすべての IP を接続直前に検証して DNS rebinding を防ぐ
  - 既定ではループバック、プライベート、リンクローカル等を拒否する

引数:
  - allowPrivateNetworks bool: 内部ネットワーク宛の接続を明示的に許可する場合は true

返り値:
  - *http.Transport: 接続ポリシーを設定した HTTP Transport
*/
func NewTransport(allowPrivateNetworks bool) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if allowPrivateNetworks {
		return transport
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport.DialContext = func(
		ctx context.Context,
		network string,
		address string,
	) (net.Conn, error) {
		return dialPublicAddress(ctx, dialer, network, address)
	}
	return transport
}

// ----------------------------------------

/*
dialPublicAddress は、名前解決結果がすべて公開 IP の場合だけ TCP 接続します。

機能:
  - host:port を分離してコンテキスト付き DNS 解決を行う
  - 一つでも拒否対象 IP が含まれる名前を安全側へ倒して拒否する
  - 利用可能な公開 IP を順番に試行する

引数:
  - ctx context.Context: DNS 解決と接続の期限・キャンセルを伝えるコンテキスト
  - dialer *net.Dialer: 検証後の IP へ接続する Dialer
  - network string: tcp、tcp4、tcp6 等のネットワーク名
  - address string: host:port 形式の接続先

返り値:
  - net.Conn: 検証済み公開 IP への接続
  - error: アドレス、名前解決、ポリシー、または接続に失敗した場合のエラー
*/
func dialPublicAddress(
	ctx context.Context,
	dialer *net.Dialer,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("外部 HTTP 接続先を解析できません: %w", err)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("外部 HTTP 接続先を名前解決できません: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("外部 HTTP 接続先の IP アドレスがありません")
	}
	for _, resolved := range addresses {
		if IsBlockedIP(resolved.IP) {
			return nil, fmt.Errorf("内部ネットワーク宛の外部 HTTP 接続を拒否しました: host=%q", host)
		}
	}

	var connectionErrors []error
	for _, resolved := range addresses {
		connection, err := dialer.DialContext(
			ctx,
			network,
			net.JoinHostPort(resolved.IP.String(), port),
		)
		if err == nil {
			return connection, nil
		}
		connectionErrors = append(connectionErrors, err)
	}
	return nil, fmt.Errorf("外部 HTTP 接続に失敗しました: %w", errors.Join(connectionErrors...))
}

// ----------------------------------------

/*
IsBlockedIP は、外部取得で拒否すべき内部または特殊用途 IP か判定します。

機能:
  - ループバック、プライベート、リンクローカル、未指定、マルチキャストを拒否する
  - 共有、文書用、ベンチマーク、予約済み、変換用等の特殊用途アドレス空間を拒否する

引数:
  - ip net.IP: 判定する IPv4 または IPv6 アドレス

返り値:
  - bool: 外部取得で拒否する IP の場合は true
*/
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() {
		return true
	}
	for _, network := range blockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ----------------------------------------

/*
mustNetworks は、固定 CIDR 文字列をネットワーク一覧へ変換します。

機能:
  - パッケージ初期化時に固定値の誤りを即座に検出する

引数:
  - values ...string: CIDR 表記のネットワーク文字列

返り値:
  - []*net.IPNet: 解析済みネットワーク一覧
*/
func mustNetworks(values ...string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(fmt.Sprintf("固定ネットワーク %q を解析できません: %v", value, err))
		}
		networks = append(networks, network)
	}
	return networks
}
