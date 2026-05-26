class Escrow < Formula
  desc "Supply-chain package proxy — age gate, OSV scan, and file caching for 7 ecosystems"
  homepage "https://github.com/jverhoeks/escrow"
  url "https://github.com/jverhoeks/escrow/archive/refs/tags/v1.1.0.tar.gz"
  sha256 "9728c10bda383ca06360b0936472ec7bdcc69332d2414fee1c83f5e2ededcbea"
  license "MIT"
  head "https://github.com/jverhoeks/escrow.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build",
           *std_go_args(ldflags: "-s -w -X main.version=#{version}"),
           "-trimpath",
           "./cmd/escrow"

    system "go", "build",
           "-o", bin/"escrow-cli",
           "-ldflags", "-s -w -X main.version=#{version} -X main.installBinDir=#{HOMEBREW_PREFIX}/bin",
           "-trimpath",
           "./cmd/escrow-cli"

    # Install default config to $(brew --prefix)/etc/escrow/escrow.toml
    (etc/"escrow").mkpath
    (etc/"escrow"/"escrow.toml").write default_config unless (etc/"escrow"/"escrow.toml").exist?
  end

  # Runs as a background service via `brew services start escrow`
  service do
    run [opt_bin/"escrow", "--config=#{etc}/escrow/escrow.toml"]
    keep_alive true
    user           "_escrow"
    log_path       var/"log/escrow.log"
    error_log_path var/"log/escrow.error.log"
    working_dir    var/"escrow"
  end

  def post_install
    # Working directory for cache, allow/block lists.
    (var/"escrow").mkpath

    # Pre-create log files so the service can write to them from first launch.
    (var/"log").mkpath
    [var/"log/escrow.log", var/"log/escrow.error.log"].each do |f|
      f.open("a") {} unless f.exist?
    end

    # Hand ownership to _escrow if the account already exists.
    # Account creation requires root and must be done manually — see caveats.
    if system("id", "-u", "_escrow", out: File::NULL, err: File::NULL)
      FileUtils.chown "_escrow", nil, var/"escrow"
      FileUtils.chown "_escrow", nil, var/"log/escrow.log"
      FileUtils.chown "_escrow", nil, var/"log/escrow.error.log"
    end
  end

  def caveats
    <<~EOS
      Escrow config is at:
        #{etc}/escrow/escrow.toml

      Edit it to enable ecosystems and set your policy, then start the service:
        brew services start escrow

      The service runs as the _escrow system account.  Run these once to
      create the account and hand over the data directories:
        sudo sysadminctl -addUser _escrow -fullName "Escrow Proxy" \
          -home /var/empty -shell /usr/bin/false -roleAccount
        sudo chown _escrow #{var}/escrow #{var}/log/escrow.log #{var}/log/escrow.error.log
      Then restart the service so it picks up the new owner:
        brew services restart escrow

      If you use the companion macOS app, open Settings → Proxy Service User
      and set it to _escrow so the pf traffic-redirect rules grant the proxy
      outbound access.

      Dashboard (after first start):
        http://localhost:7888/dashboard
        Credentials are printed to the log on first boot:
        #{var}/log/escrow.log

      To point npm at escrow:
        npm config set registry http://localhost:7888

      To point Go at escrow:
        go env -w GOPROXY=http://localhost:7888/go,off
    EOS
  end

  test do
    # Start escrow in background and verify it responds
    port = free_port
    config = testpath/"escrow.toml"
    config.write <<~TOML
      [server]
        host = "127.0.0.1"
        port = #{port}
      [storage]
        backend = "memory"
      [ecosystems]
        npm = true
      [dashboard]
        enabled = false
    TOML

    pid = fork { exec bin/"escrow", "--config=#{config}" }
    sleep 2
    assert_match "ok", shell_output("curl -sf http://127.0.0.1:#{port}/healthz")
  ensure
    Process.kill("TERM", pid)
  end

  private

  def default_config
    <<~TOML
      # escrow — supply-chain package proxy
      # Docs: https://github.com/jverhoeks/escrow
      # Dashboard: http://localhost:7888/dashboard  (credentials printed on first start)

      [server]
        host               = "127.0.0.1"
        port               = 7888
        log_level          = "info"
        access_log_path    = "~/.cache/escrow/access.log"
        access_log_max_days = 30

      [storage]
        backend = "disk"
        [storage.disk]
          path             = "~/.cache/escrow"
          max_size_gb      = 10
          purge_interval_m = 60

      [ecosystems]
        npm  = true
        pypi = true
        # go       = false
        # cargo    = false
        # composer = false
        # nuget    = false
        # maven    = false

      [policy]
        [policy.age]
          min_days = 7
          action   = "block"

        [policy.osv]
          min_severity = "MEDIUM"
          action       = "block"

      [dashboard]
        enabled = true
        path    = "/dashboard"
        # Credentials are auto-generated on first start and printed to the log.

      [alerts]
        webhook_url = ""

      allowlist_path = "~/.cache/escrow/allowlist.json"
      blocklist_path = "~/.cache/escrow/blocklist.json"
    TOML
  end
end
