package message

import "encoding/json"

// TurnSettingsProvider carries local history metadata between Prowl's message
// conversion and its provider adapter. Provider SDKs ignore this namespace.
const TurnSettingsProvider = "prowl.turn_settings"

// TurnSettings records explicit model controls and response preferences at
// their position in history. It is not displayed as user-authored text.
type TurnSettings struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	FocusMode       string `json:"focus_mode,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
}

func (TurnSettings) isPart() {}

// Options implements Fantasy's provider-options marker interface.
func (*TurnSettings) Options() {}

func (s TurnSettings) MarshalJSON() ([]byte, error) {
	type plain TurnSettings
	return json.Marshal(plain(s))
}

func (s *TurnSettings) UnmarshalJSON(data []byte) error {
	type plain TurnSettings
	return json.Unmarshal(data, (*plain)(s))
}

// TurnSettings returns the latest settings recorded on this message.
func (m *Message) TurnSettings() TurnSettings {
	for i := len(m.Parts) - 1; i >= 0; i-- {
		if settings, ok := m.Parts[i].(TurnSettings); ok {
			return settings
		}
	}
	return TurnSettings{}
}
