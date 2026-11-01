# 018 (retro): Cookie を送信先で絞る変更と、testwait の module 名で go mod tidy が落ちた件の振り返り

起票日: 2026-11-01

## 概要

chromecookie の読み取りを送信先ホストで絞り（esa-cli / slack-cli / newrelic-nrql-cli へ展開し、ReadCookies を削除）、その取り込みで表に出た「取り込む側の go mod tidy が落ちる」を dotfiles の lint で止めるまでのセッションの振り返り。本文は反証レビューの後に入れる。
