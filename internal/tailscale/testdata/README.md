# Test fixtures

The shapes here are what `tailscale` 1.98.4 prints on Linux: the key sets of
`status-running.json`, `status-needs-login.json` and `debug-prefs.json` were
taken from a real client joined to a Headscale control plane, then every value
was replaced. Nothing in these files names a real machine, person or network:

- addresses are from the tailnet CGNAT range (`100.64.0.x`), the tailnet ULA
  (`fd7a:115c:a1e0::x`) and the documentation ranges (`192.0.2.0/24`,
  `2001:db8::/32`);
- names are `example-node`, `user@example.com` and `*.example.com`;
- every key is zeroed (`nodekey:000…`, `privkey:000…`), which is also what the
  client itself prints for the private ones.

`status-with-warning.txt` is the combined output a mismatched client and daemon
produce (a warning on stderr ahead of the JSON), `status-not-running.txt` the
client's words when tailscaled does not answer, and `up-interactive.txt` what
`tailscale up --timeout=…` prints when it has no pre-auth key.

When a parser is wrong on someone's machine, their output — scrubbed the same
way — becomes the next fixture.
