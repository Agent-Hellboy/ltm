# systemd unit (optional)

Opt-in way to keep `ltm` recording across reboots instead of running
`sudo ltm start` by hand. `ltm start`/`ltm stop` remain the default, portable
path (see [docs/recording.md](../../docs/recording.md)); nothing here is
required to use ltm.

```bash
go build -o bin/ltm ./cmd/ltm
sudo install -m 0755 bin/ltm /usr/bin/ltm
sudo install -m 0644 contrib/systemd/ltm.service /etc/systemd/system/ltm.service
sudo systemctl daemon-reload
sudo systemctl enable --now ltm
```

See [docs/recording.md](../../docs/recording.md#systemd-optional) for
querying, uninstalling, and the unprivileged-user variant.
