# UAC and UAS dialog implementations have 6 duplicated method pairs

## Problem

In `sipgo_dialog.go`, `sipgoDialogUAC` and `sipgoDialogUAS` share nearly identical implementations for 6 methods:
- `SendReInvite`, `SendRefer`, `CallID`, `OnNotify`, `Header`, `Headers`

Only `Respond`, `SendBye`, and `SendCancel` differ because the underlying session types differ (`clientSession` vs `serverSession`).

## Proposed fix

Extract a shared base struct holding `mu`, `invite`, `response`, `onNotify` and the common methods. Embed it in both UAC and UAS types. Only the differing methods remain on the concrete types.
