package xphone

import "encoding/xml"

// dialogInfo represents the root <dialog-info> element of RFC 4235.
type dialogInfo struct {
	XMLName xml.Name      `xml:"dialog-info"`
	Dialogs []dialogEntry `xml:"dialog"`
}

// dialogEntry represents a single <dialog> element.
type dialogEntry struct {
	State string `xml:"state"`
}

// RFC 4235 dialog state values.
const (
	dialogStateConfirmed  = "confirmed"
	dialogStateEarly      = "early"
	dialogStateProceeding = "proceeding"
	dialogStateTerminated = "terminated"
)

// parseDialogInfo parses a dialog-info+xml body and derives an ExtensionState.
//
// Mapping:
//   - No dialogs or all terminated → ExtensionAvailable
//   - Any confirmed               → ExtensionOnThePhone
//   - Any early/proceeding        → ExtensionRinging
//   - Otherwise                   → ExtensionUnknown
func parseDialogInfo(body string) (ExtensionState, error) {
	var info dialogInfo
	if err := xml.Unmarshal([]byte(body), &info); err != nil {
		return ExtensionUnknown, err
	}
	if len(info.Dialogs) == 0 {
		return ExtensionAvailable, nil
	}

	hasConfirmed := false
	hasEarly := false
	allTerminated := true

	for _, d := range info.Dialogs {
		switch d.State {
		case dialogStateConfirmed:
			hasConfirmed = true
			allTerminated = false
		case dialogStateEarly, dialogStateProceeding:
			hasEarly = true
			allTerminated = false
		case dialogStateTerminated:
			// still terminated
		default:
			allTerminated = false
		}
	}

	switch {
	case hasConfirmed:
		return ExtensionOnThePhone, nil
	case hasEarly:
		return ExtensionRinging, nil
	case allTerminated:
		return ExtensionAvailable, nil
	default:
		return ExtensionUnknown, nil
	}
}
