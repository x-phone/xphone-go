# UAC and UAS dialog implementations have 6 duplicated method pairs

## Status: RESOLVED

## Problem

`sipgoDialogUAC` and `sipgoDialogUAS` shared nearly identical implementations for 6 methods: `SendReInvite`, `SendRefer`, `CallID`, `OnNotify`, `Header`, `Headers`, and `SendBye`.

## Fix

Extracted `dialogBase` struct with shared fields (`mu`, `sess`, `invite`, `response`, `onNotify`) and all 7 common methods. Both UAC and UAS embed `dialogBase`. Only `Respond` and `SendCancel` remain on the concrete types (they differ between UAC and UAS).
