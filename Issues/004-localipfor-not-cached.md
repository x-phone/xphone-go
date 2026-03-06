# localIPFor() called multiple times per call instead of being cached

## Status: RESOLVED

## Fix

Cached `localIPFor(cfg.Host)` on `sipUA.localIP` at construction time (reused in `dial()`). Cached on `phone.localIP` at construction time (passed to both outbound and inbound calls). `ensureRTPPort()` retains a fallback call for test code that doesn't set `localIP`.
