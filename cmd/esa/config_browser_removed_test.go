package main

import (
	"errors"
	"testing"
)

// 廃止した `browser` キーが config のキー集合へ戻ってこないことを固定する（issue 003）。
//
// 🚨 `configKeys` の中身を直接 assert するのでは不十分。守りたい契約は
// 「利用者が `esa config set browser Brave` と打ったとき、使い方エラー（rc=2）で止まる」こと。
// `configKeys` にだけ戻して `fileConfig` にフィールドが無い状態だと、configSet の switch は
// どの case にも当たらずに saveFileConfig へ落ち、**「保存しました」と表示しながら何も
// 書かれない**という、利用者から見ていちばん悪い形になる。そこを起点から押さえる。
func TestConfigSetRejectsRemovedBrowserKey(t *testing.T) {
	// 🚨 テストの正しさに依存せず、書き込み先を実行前にサンドボックスへ閉じ込める。
	// このテストが守る契約が壊れた（= キーが復活した）瞬間、configSet は早期 return せず
	// saveFileConfig まで到達し、**利用者の本物の ~/.config/esa-cli/config.yml を
	// 書き換える**。「落ちるはずだから書かれない」は、落ちなくなった時にだけ効かない。
	// configDir() は XDG_CONFIG_HOME を見るので、ここで差し替えれば実行前に閉じられる。
	//
	// 🚨 この隔離は**契約が守られている限り一度も実行されない**（configSet は
	// configKeys の検査で早期 return し、loadFileConfig / saveFileConfig へ到達しない）。
	// 「緑だから隔離も効いている」とは言えないので、効くことは変異で確かめてある:
	// configKeys に "browser": true を戻すと saveFileConfig まで到達し、この行を
	// 消した状態では実 HOME の config.yml が `{}` で上書きされた（team/profile ごと消える）。
	// この行を消す変更をするなら、同じ変異をもう一度当てて確かめること。
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := configSet("browser", "Brave")
	if err == nil {
		t.Fatal("configSet(\"browser\", ...) がエラーを返さなかった（廃止したキーが復活している）")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("usageError を期待したが %T: %v", err, err)
	}
}

// config get 側も同じ契約（読み出しだけは通る、を作らない）。
func TestConfigGetRejectsRemovedBrowserKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // configGet は読むだけだが、経路を揃える
	err := configGet("browser")
	if err == nil {
		t.Fatal("configGet(\"browser\") がエラーを返さなかった（廃止したキーが復活している）")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("usageError を期待したが %T: %v", err, err)
	}
}
