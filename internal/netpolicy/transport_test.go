package netpolicy

import (
	"net"
	"testing"
)

/*
TestIsBlockedIP は、公開 IP と内部・特殊用途 IP の判定を検証します。

機能:
  - IPv4とIPv6の公開アドレスを許可し、内部・予約・特殊用途アドレスを遮断することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestIsBlockedIP(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		address string
		blocked bool
	}{
		{name: "公開IPv4", address: "8.8.8.8", blocked: false},
		{name: "公開IPv6", address: "2606:4700:4700::1111", blocked: false},
		{name: "ループバックIPv4", address: "127.0.0.1", blocked: true},
		{name: "ループバックIPv6", address: "::1", blocked: true},
		{name: "プライベートIPv4", address: "10.0.0.1", blocked: true},
		{name: "プライベートIPv6", address: "fd00::1", blocked: true},
		{name: "メタデータ相当", address: "169.254.169.254", blocked: true},
		{name: "共有アドレス", address: "100.64.0.1", blocked: true},
		{name: "未指定", address: "0.0.0.0", blocked: true},
		{name: "thisネットワーク", address: "0.0.0.1", blocked: true},
		{name: "ベンチマークIPv4", address: "198.18.0.1", blocked: true},
		{name: "文書用IPv4", address: "203.0.113.1", blocked: true},
		{name: "予約済みIPv4", address: "240.0.0.1", blocked: true},
		{name: "NAT64変換用IPv6", address: "64:ff9b::1", blocked: true},
		{name: "文書用IPv6", address: "2001:db8::1", blocked: true},
		{name: "6to4IPv6", address: "2002:c000:0204::1", blocked: true},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			actual := IsBlockedIP(net.ParseIP(testCase.address))
			if actual != testCase.blocked {
				t.Errorf(
					"IsBlockedIP(%q) = %t, 期待値は %t",
					testCase.address,
					actual,
					testCase.blocked,
				)
			}
		})
	}
}

// ----------------------------------------

/*
TestNewTransportDisablesEnvironmentProxy は、外部取得が環境プロキシを自動利用しないことを検証します。

機能:
  - 生成したHTTP Transportが環境プロキシを使わず、ネットワーク制限付きDialContextを持つことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestNewTransportDisablesEnvironmentProxy(t *testing.T) {
	t.Parallel()

	transport := NewTransport(false)
	if transport.Proxy != nil {
		t.Error("Transport.Proxy は nil を期待")
	}
	if transport.DialContext == nil {
		t.Error("内部ネットワーク制限付き DialContext を期待")
	}
}
