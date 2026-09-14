class Esa < Formula
  desc "esa (esa.io) を Chrome cookie 認証で参照する読み取り専用 CLI"
  homepage "https://github.com/jiikko/esa-cli"
  url "https://github.com/jiikko/esa-cli/archive/refs/tags/v0.1.1.tar.gz"
  sha256 "a0bcb1a3680d9512b0dc6ec3e27da18bec997f8902b6aed4acb71d2171c46fa9"
  version "0.1.1"
  license "MIT"
  head "https://github.com/jiikko/esa-cli.git", branch: "main"

  depends_on "go" => :build
  depends_on :macos

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/esa"
  end

  test do
    assert_match "esa - ", shell_output("#{bin}/esa --help")
    # 引数不足は終了コード 2（使い方エラー）
    output = shell_output("#{bin}/esa search 2>&1", 2)
    assert_match "検索クエリを指定してください", output
  end
end
