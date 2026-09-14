# jiikko/homebrew-tap の Formula/esa.rb に置く想定。
# 利用者は: brew install jiikko/tap/esa
#
# 前提: esa-client を独立リポジトリ github.com/jiikko/esa-cli に切り出し、
#       Go モジュールをそのルートに置く（go.mod がルートにある状態）。
class Esa < Formula
  desc "社内 esa (esa.io) を Chrome cookie 認証で参照する読み取り専用 CLI"
  homepage "https://github.com/jiikko/esa-cli"
  # リリースタグごとに url と sha256 を更新する。
  # sha256 の求め方:
  #   curl -sL https://github.com/jiikko/esa-cli/archive/refs/tags/v0.1.0.tar.gz | shasum -a 256
  url "https://github.com/jiikko/esa-cli/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_TARBALL_SHA256"
  license "MIT"
  head "https://github.com/jiikko/esa-cli.git", branch: "main"

  depends_on "go" => :build
  depends_on :macos # Keychain / Chrome cookie 復号が macOS 前提

  def install
    # std_go_args は -o #{bin}/esa（formula 名）を設定する。
    system "go", "build", *std_go_args(ldflags: "-s -w"), "."
  end

  test do
    assert_match "esa - ", shell_output("#{bin}/esa --help")
    # 引数不足は終了コード 2（使い方エラー）
    output = shell_output("#{bin}/esa search 2>&1", 2)
    assert_match "検索クエリを指定してください", output
  end
end
