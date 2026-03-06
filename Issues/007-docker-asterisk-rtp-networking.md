# Docker/Asterisk integration tests fail due to macOS networking

## Status: RESOLVED

## Problem

Integration tests E3, E5, and E6 failed when running against Docker Asterisk on macOS:

- **E3 (Remote BYE)**: Inbound side never receives BYE notification
- **E5 (DTMF)**: DTMF digit never received — RTP path not working through Docker NAT
- **E6 (Echo)**: No RTP echo response — packets sent to container IP (10.200.1.x) are not routable from macOS host

Root cause: `local_net=10.0.0.0/8` in `pjsip_transport.conf` included the host's LAN IP (e.g. 10.0.0.7). Asterisk treated xphone as "local" and used the container IP (10.200.1.2) in SDP instead of `external_media_address` (127.0.0.1). Docker Desktop macOS doesn't support direct container IP routing.

## Fix

Updated `entrypoint.sh` to detect the container's actual subnet via `hostname -i` and derive a narrow `/24` `local_net` instead of using broad RFC1918 ranges. Now only `10.200.1.0/24` and `127.0.0.0/8` are `local_net`, so the host IP is treated as "external" and `external_media_address=127.0.0.1` is applied to SDP.

E1, E2, E3, E5, E6 all pass. E4 (Hold/Resume) still skipped due to Asterisk re-INVITE collision.
