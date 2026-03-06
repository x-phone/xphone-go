# sipcli

Interactive terminal SIP client built with the [xphone](https://github.com/x-phone/xphone-go) library. Demonstrates the event-driven API: registration, inbound/outbound calls, hold, resume, DTMF, mute, and blind transfer.

## Run

```bash
cd examples/sipcli
go run . -server pbx.example.com -user 1001 -pass secret
```

## Install

Build and install to `~/bin` (must be in your `$PATH`):

```bash
make install-sipcli   # from the repo root
sipcli -profile work  # run from anywhere
```

## Profiles

Save credentials in `~/.sipcli.yaml` to avoid retyping them:

```yaml
profiles:
  work:
    server: 100.96.49.117
    user: 1001
    pass: password123
  home:
    server: sip.provider.com
    user: john
    pass: secret
    transport: tcp
```

Then load by name:

```bash
sipcli -profile work
```

Flags override profile values:

```bash
sipcli -profile work -transport tcp
```

## Commands

| Command | Description |
|---------|-------------|
| `dial <target>` | Place an outbound call (`1002` or `sip:1002@host`) |
| `accept` | Accept an incoming call |
| `reject` | Reject an incoming call (486 Busy) |
| `hangup` | End the active call |
| `hold` | Put the call on hold |
| `resume` | Resume a held call |
| `mute` | Suppress outbound audio |
| `unmute` | Resume outbound audio |
| `dtmf <digits>` | Send DTMF tones (e.g. `dtmf 1234#`) |
| `transfer <target>` | Blind transfer to another extension |
| `quit` | Disconnect and exit |

Shorthand: `d` for dial, `a` for accept, `h` for hangup, `q` for quit.
