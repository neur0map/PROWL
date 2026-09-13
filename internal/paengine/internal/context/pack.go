package context

import (
	"encoding/json"
	"fmt"
)

// Pack ranks and fits candidates into an explicit estimated context budget.
func Pack(request Request, candidates []Candidate, estimator CostEstimator) (Packet, error) {
	if err := request.Validate(); err != nil {
		return Packet{}, err
	}
	if estimator == nil {
		estimator = ByteQuarterEstimator{}
	}
	if request.BudgetTokens == 0 && request.BudgetBytes == 0 {
		request.BudgetTokens = 1800
	}
	packet := emptyPacket(request)
	packet.Budget.RequestedTokens = request.BudgetTokens
	packet.Budget.RequestedBytes = request.BudgetBytes
	ranked := diversify(RankCandidates(candidates))
	seen := map[string]bool{}
	for _, candidate := range ranked {
		if seen[candidate.ID] {
			packet.Omitted["duplicate"]++
			continue
		}
		seen[candidate.ID] = true
		content, usedMode := contentForMode(candidate, request.Mode)
		costText := candidate.Title + "\n" + candidate.Summary + "\n" + content
		tokens := estimator.Tokens(costText)
		bytesCost := len([]byte(costText))
		if !fits(packet.Budget, request, tokens, bytesCost) && request.Mode != ModeCompact {
			content, usedMode = contentForMode(candidate, ModeCompact)
			costText = candidate.Title + "\n" + candidate.Summary + "\n" + content
			tokens = estimator.Tokens(costText)
			bytesCost = len([]byte(costText))
		}
		if !fits(packet.Budget, request, tokens, bytesCost) {
			omitBudgetItem(&packet, candidate.ID)
			continue
		}
		item := candidate.Item
		item.Content = content
		if item.Content == item.Summary {
			item.Content = ""
		}
		item.EstimatedTokens = tokens
		item.WhySelected = uniqueStrings(append(item.WhySelected, "packed as "+string(usedMode)))
		if item.WhySelected == nil {
			item.WhySelected = []string{}
		}
		if item.Audience == nil {
			item.Audience = []string{"assistant", "user"}
		}
		if item.Citations == nil {
			item.Citations = []Citation{}
		}
		packet.Items = append(packet.Items, item)
		packet.Budget.EstimatedTokens += tokens
		packet.Budget.EstimatedBytes += bytesCost
		packet.Budget.ExactBytes += bytesCost
	}
	packet.Summary = fmt.Sprintf("Selected %d context item(s).", len(packet.Items))
	_, err := EncodeBounded(&packet, estimator, func(packet Packet) ([]byte, error) {
		return json.Marshal(packet)
	})
	return packet, err
}

func contentForMode(candidate Candidate, mode Mode) (string, Mode) {
	switch mode {
	case ModeFull:
		if candidate.FullContent != "" {
			return candidate.FullContent, ModeFull
		}
		fallthrough
	case ModeStandard:
		if candidate.StandardContent != "" {
			return candidate.StandardContent, ModeStandard
		}
		fallthrough
	default:
		return candidate.CompactContent, ModeCompact
	}
}

func fits(current Budget, request Request, tokens, bytesCost int) bool {
	if request.BudgetTokens > 0 && current.EstimatedTokens+tokens > request.BudgetTokens {
		return false
	}
	if request.BudgetBytes > 0 && current.EstimatedBytes+bytesCost > request.BudgetBytes {
		return false
	}
	return true
}

func diversify(candidates []Candidate) []Candidate {
	var first, repeats []Candidate
	seenSource := map[string]bool{}
	for _, candidate := range candidates {
		source := candidate.ID
		if len(candidate.Citations) > 0 && candidate.Citations[0].Path != "" {
			source = candidate.Citations[0].Path
		}
		if !seenSource[source] {
			seenSource[source] = true
			first = append(first, candidate)
		} else {
			repeats = append(repeats, candidate)
		}
	}
	return append(first, repeats...)
}

// EncodeBounded measures the complete transport representation, including its
// envelope, escaping, and budget fields. Lower-ranked detail is removed before
// evidence. The returned bytes are exactly the representation that was measured.
func EncodeBounded(packet *Packet, estimator CostEstimator, encode func(Packet) ([]byte, error)) ([]byte, error) {
	if estimator == nil {
		estimator = ByteQuarterEstimator{}
	}
	for {
		var encoded []byte
		stable := false
		for range 8 {
			var err error
			encoded, err = encode(*packet)
			if err != nil {
				return nil, err
			}
			size, tokens := len(encoded), estimator.Tokens(string(encoded))
			if packet.Budget.ExactBytes == size && packet.Budget.EstimatedTokens == tokens && packet.Budget.EstimatedBytes == size {
				stable = true
				break
			}
			packet.Budget.ExactBytes, packet.Budget.EstimatedBytes = size, size
			packet.Budget.EstimatedTokens = tokens
		}
		if !stable {
			return nil, fmt.Errorf("context budget estimator did not converge")
		}
		if (packet.Budget.RequestedBytes == 0 || len(encoded) <= packet.Budget.RequestedBytes) &&
			(packet.Budget.RequestedTokens == 0 || packet.Budget.EstimatedTokens <= packet.Budget.RequestedTokens) {
			return encoded, nil
		}
		if len(packet.OmittedIDs) > 1 || len(packet.Items) == 1 && len(packet.OmittedIDs) > 0 {
			packet.OmittedIDs = packet.OmittedIDs[:len(packet.OmittedIDs)-1]
			continue
		}
		if len(packet.Items) > 0 {
			last := &packet.Items[len(packet.Items)-1]
			if last.Content != "" && last.Summary != "" {
				last.Content = ""
				last.EstimatedTokens = estimator.Tokens(last.Title + "\n" + last.Summary)
				last.WhySelected = append(last.WhySelected, "detail omitted to fit the response; recover with context get")
				continue
			}
			omitBudgetItem(packet, last.ID)
			packet.Items = packet.Items[:len(packet.Items)-1]
			packet.Summary = fmt.Sprintf("Selected %d context item(s).", len(packet.Items))
			continue
		}
		if len(packet.OmittedIDs) > 0 {
			packet.OmittedIDs = packet.OmittedIDs[:len(packet.OmittedIDs)-1]
			continue
		}
		return nil, fmt.Errorf("context budget too small for response metadata: need at least %d bytes (~%d estimated tokens); increase the budget or shorten the question", len(encoded), packet.Budget.EstimatedTokens)
	}
}

func omitBudgetItem(packet *Packet, id string) {
	packet.Omitted["budget"]++
	if len(packet.OmittedIDs) < 3 {
		packet.OmittedIDs = append(packet.OmittedIDs, id)
	}
	if packet.Omitted["budget"] == 1 {
		packet.Next = append(packet.Next, "Recover omitted_ids with context get <id> --mode full and a larger budget; use find/def for a precise symbol.")
	}
}
