package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/denisbrodbeck/machineid"
	"github.com/google/uuid"
)

// Claude Code inference-fingerprint constants. These mirror the values the
// current Claude Code CLI presents on the Anthropic wire; keep them in sync
// with a real CLI release when refreshing the fingerprint.
const (
	// claudeCodeVersion is the Claude Code CLI version on the wire.
	claudeCodeVersion = "2.1.257"
	// claudeCodeSDKVersion is the @anthropic-ai/sdk version bundled by that release.
	claudeCodeSDKVersion = "0.112.1"
	// claudeCodeUserAgent is the User-Agent Claude Code's inference entrypoint sends.
	claudeCodeUserAgent = "claude-cli/" + claudeCodeVersion + " (external, cli)"
	// claudeCodeSystemInstruction is the identity block Claude Code prepends to
	// every request. Anthropic requires OAuth (Claude Pro/Max) requests to
	// present this Claude Code identity.
	claudeCodeSystemInstruction = "You are Claude Code, Anthropic's official CLI for Claude."
	// claudeToolPrefix isolates the agent's custom tools from Anthropic's
	// built-in tool names on OAuth requests.
	claudeToolPrefix = "_"

	// oauthBeta is the anthropic-beta flag that gates OAuth credential
	// acceptance. Without it the subscription token is rejected.
	oauthBeta = "oauth-2025-04-20"
)

// claudeCodeBetas are the beta flags Claude Code sends on every OAuth inference
// request. oauth-2025-04-20 is required for the credential to be accepted; the
// rest present the request as Claude Code. They are model-independent and are
// merged with (never substituted for) betas the caller already set, so
// model-specific feature betas remain the caller's responsibility.
var claudeCodeBetas = []string{
	"claude-code-20250219",
	oauthBeta,
	"interleaved-thinking-2025-05-14",
	"fine-grained-tool-streaming-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"mid-conversation-system-2026-04-07",
}

// anthropicBuiltinToolNames are Anthropic's server-side tool names, which are
// never prefixed on the wire.
var anthropicBuiltinToolNames = map[string]struct{}{
	"web_search":     {},
	"code_execution": {},
	"text_editor":    {},
	"computer":       {},
}

// mapStainlessOS maps Go's GOOS to the Stainless SDK wire value.
func mapStainlessOS(goos string) string {
	switch strings.ToLower(goos) {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	default:
		return "Other::" + strings.ToLower(goos)
	}
}

// mapStainlessArch maps Go's GOARCH to the Stainless SDK wire value.
func mapStainlessArch(goarch string) string {
	switch strings.ToLower(goarch) {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return "other::" + strings.ToLower(goarch)
	}
}

// claudeCodeStaticHeaders returns the static X-Stainless-* fingerprint headers
// Claude Code's runtime emits. Arch/OS reflect the host so the fingerprint
// stays internally consistent.
func claudeCodeStaticHeaders() map[string]string {
	return map[string]string{
		"X-Stainless-Arch":            mapStainlessArch(runtime.GOARCH),
		"X-Stainless-Lang":            "js",
		"X-Stainless-OS":              mapStainlessOS(runtime.GOOS),
		"X-Stainless-Package-Version": claudeCodeSDKVersion,
		"X-Stainless-Retry-Count":     "0",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v26.3.0",
		"X-Stainless-Timeout":         "600",
	}
}

// mergeBetas returns claudeCodeBetas followed by any caller betas not already
// present, order-preserving and de-duplicated. This guarantees the OAuth and
// Claude Code betas lead while keeping model-specific betas the caller added.
func mergeBetas(existing string) string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(claudeCodeBetas)+4)
	add := func(b string) {
		b = strings.TrimSpace(b)
		if b == "" {
			return
		}
		if _, ok := seen[b]; ok {
			return
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}
	for _, b := range claudeCodeBetas {
		add(b)
	}
	for _, b := range strings.Split(existing, ",") {
		add(b)
	}
	return strings.Join(out, ",")
}

const claudeBillingHeaderPrefix = "x-anthropic-billing-header:"

// createClaudeBillingHeader builds the `x-anthropic-billing-header` system-block
// text, including the `cch=00000` placeholder patchCch later rewrites. The
// version suffix is a 3-hex fingerprint derived from the first user message and
// the CLI version, matching Claude Code's computeFingerprint. Indices are taken
// over UTF-16 code units to match the CLI's JavaScript string indexing exactly.
func createClaudeBillingHeader(firstUserMessageText string) string {
	units := utf16.Encode([]rune(firstUserMessageText))
	selected := []uint16{'0', '0', '0'}
	for i, index := range [...]int{4, 7, 20} {
		if index < len(units) {
			selected[i] = units[index]
		}
	}
	k := string(utf16.Decode(selected))
	sum := sha256.Sum256([]byte("59cf53e54c78" + k + claudeCodeVersion))
	versionSuffix := hex.EncodeToString(sum[:2])[:3]
	return fmt.Sprintf("%s cc_version=%s.%s; cc_entrypoint=cli; %s;",
		claudeBillingHeaderPrefix, claudeCodeVersion, versionSuffix, cchPlaceholderText)
}

// Device-id derivation domains, matching the OMP reference so attribution is
// stable per install (and per account when known).
const (
	deviceIDInstallDomain = "omp-claude-device-id-v1:"
	deviceIDAccountDomain = "omp-claude-device-id-v2"
)

// deriveClaudeDeviceID derives a stable device id from a machine-stable install
// id and, when known, the account id. Mirrors OMP's deriveClaudeDeviceId.
func deriveClaudeDeviceID(installID, accountID string) string {
	h := sha256.New()
	if accountID != "" {
		h.Write([]byte(deviceIDAccountDomain))
		h.Write([]byte{0})
		h.Write([]byte(installID))
		h.Write([]byte{0})
		h.Write([]byte(accountID))
		return hex.EncodeToString(h.Sum(nil))
	}
	h.Write([]byte(deviceIDInstallDomain))
	h.Write([]byte(installID))
	return hex.EncodeToString(h.Sum(nil))
}

// installID returns a machine-stable, app-scoped identifier used to seed the
// device id. It is privacy-preserving (a keyed hash of the machine id) and
// requires no disk state. Empty on platforms where the machine id is
// unavailable, in which case metadata.user_id is skipped.
var installID = sync.OnceValue(func() string {
	id, _ := machineid.ProtectedID("prowl-claude")
	return id
})

// resolveMetadataUserID resolves the `metadata.user_id` for an OAuth request.
// A caller-supplied id that already matches Claude Code's attribution shape is
// kept; anything else is replaced with a freshly generated Claude-Code-style
// JSON envelope so attribution stays consistent. Returns ("", false) when no
// stable install id is available (the field is then left unset rather than
// fabricated).
func resolveMetadataUserID(existing, sessionID, accountID string) (string, bool) {
	if existing != "" && (isClaudeCloakingUserID(existing) || isClaudeJSONUserID(existing)) {
		return existing, true
	}
	install := installID()
	if install == "" {
		return "", false
	}
	if sessionID == "" {
		sessionID = strings.ToLower(uuid.NewString())
	}
	envelope := struct {
		DeviceID    string `json:"device_id"`
		SessionID   string `json:"session_id"`
		AccountUUID string `json:"account_uuid,omitempty"`
	}{deriveClaudeDeviceID(install, accountID), sessionID, accountID}
	data, _ := json.Marshal(envelope)
	return string(data), true
}

// isClaudeCloakingUserID reports whether id matches Claude Code's opaque
// cloaking user id shape (user_<64hex>_account_<uuid>_session_<uuid>).
func isClaudeCloakingUserID(id string) bool {
	if !strings.HasPrefix(id, "user_") {
		return false
	}
	rest := id[len("user_"):]
	hexPart, rest, ok := strings.Cut(rest, "_account_")
	if !ok || len(hexPart) != 64 || !isHex(hexPart) {
		return false
	}
	acct, sess, ok := strings.Cut(rest, "_session_")
	if !ok {
		return false
	}
	return isUUID(acct) && isUUID(sess)
}

// isClaudeJSONUserID reports whether id is the `{session_id, ...}` JSON envelope
// Claude Code sends as metadata.user_id.
func isClaudeJSONUserID(id string) bool {
	if !strings.HasPrefix(strings.TrimSpace(id), "{") {
		return false
	}
	var env struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(id), &env); err != nil {
		return false
	}
	return env.SessionID != ""
}

// applyClaudeToolPrefix prepends the tool prefix unless name is a built-in tool.
// Applying the prefix unconditionally lets the response-name map round-trip
// a custom tool literally named "_foo" without confusing it with "foo".
func applyClaudeToolPrefix(name string) string {
	if _, ok := anthropicBuiltinToolNames[strings.ToLower(name)]; ok {
		return name
	}
	return claudeToolPrefix + name
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return len(s) > 0
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}
