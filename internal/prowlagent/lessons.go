package prowlagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/neur0map/prowl/internal/config"
)

// ErrNoBundle reports that the project has no prowl-agent knowledge bundle, so
// there is nothing to review or delete. It is the common case for a project
// that has never recorded a lesson, and every review-side call below returns it
// (checkable with errors.Is) so a caller can tell "no bundle yet" apart from a
// real filesystem or engine failure.
var ErrNoBundle = errors.New("prowlagent: no knowledge bundle")

// bundleDirName is the per-project workspace directory the engine creates. It
// duplicates workspace.Dir in the embedded engine because that package is
// engine-internal and cannot be imported here, and because the review-side
// operations below read the proposal inbox straight from disk (the engine
// exposes no list-proposals command; see ListProposals).
const bundleDirName = ".prowl"

// Proposal is a pending knowledge review item. It mirrors the metadata the
// engine keeps in each proposal's proposal.json, plus the candidate's Title and
// Body read from candidate.md so a reviewer sees what is proposed without a
// second lookup.
type Proposal struct {
	ID         string `json:"id"`
	Operation  string `json:"operation"`
	TargetPath string `json:"target_path"`
	Status     string `json:"status"`
	Author     string `json:"author,omitempty"`
	CreatedAt  string `json:"created_at"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body,omitempty"`
}

// CandidatePath returns the on-disk candidate file for a pending proposal, so
// a reviewer can edit a lesson's wording before accepting it.
func CandidatePath(workingDir, id string) (string, error) {
	proposals, _, err := bundlePaths(workingDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(proposals, id, "candidate.md")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("proposal %s has no candidate file: %w", id, err)
	}
	return path, nil
}

// bundlePaths walks up from workingDir to the project's .prowl bundle and
// returns its proposal-inbox and accepted-knowledge directories. It mirrors the
// engine's own upward search for the common, non-linked-worktree case. A missing
// bundle is reported as ErrNoBundle so callers can tell "nothing recorded yet"
// apart from a real failure.
func bundlePaths(workingDir string) (proposals, knowledge string, err error) {
	dir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", "", err
	}
	for {
		cand := filepath.Join(dir, bundleDirName)
		fi, statErr := os.Stat(cand)
		switch {
		case statErr == nil && fi.IsDir():
			return filepath.Join(cand, "proposals"),
				filepath.Join(cand, "knowledge"), nil
		case statErr != nil && !os.IsNotExist(statErr):
			return "", "", statErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", ErrNoBundle
		}
		dir = parent
	}
}

// ListProposals returns the pending review inbox: every proposal still awaiting
// a human decision, with its metadata and the candidate's title and body.
//
// The embedded engine has no list-proposals command. `prowl-agent knowledge
// list --help` reports "List accepted knowledge documents", so `knowledge list`
// covers accepted docs only. The proposal inbox is instead a directory of
// <id>/proposal.json plus <id>/candidate.md files, so this reads it directly. A
// project with no bundle returns ErrNoBundle; a bundle that simply has no
// pending proposals returns an empty slice.
func ListProposals(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) ([]Proposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proposalsDir, _, err := bundlePaths(workingDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(proposalsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read proposal inbox: %w", err)
	}
	var proposals []Proposal
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		proposal, err := readProposal(proposalsDir, entry.Name())
		if err != nil {
			return nil, err
		}
		// Only proposals still awaiting review belong in the inbox; accepted
		// and rejected ones keep their audit record on disk but are decided.
		if proposal == nil || proposal.Status != "proposed" {
			continue
		}
		proposals = append(proposals, *proposal)
	}
	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].CreatedAt == proposals[j].CreatedAt {
			return proposals[i].ID < proposals[j].ID
		}
		return proposals[i].CreatedAt < proposals[j].CreatedAt
	})
	return proposals, nil
}

// readProposal loads one inbox entry: its proposal.json metadata and the
// candidate.md title and body. A directory without a proposal.json is not an
// inbox entry and yields (nil, nil).
func readProposal(proposalsDir, id string) (*Proposal, error) {
	metaData, err := os.ReadFile(filepath.Join(proposalsDir, id, "proposal.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read proposal %s: %w", id, err)
	}
	var meta struct {
		ID         string `json:"id"`
		Operation  string `json:"operation"`
		TargetPath string `json:"target_path"`
		Status     string `json:"status"`
		Author     string `json:"author"`
		CreatedAt  string `json:"created_at"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return nil, fmt.Errorf("parse proposal %s: %w", id, err)
	}
	proposal := Proposal{
		ID: meta.ID, Operation: meta.Operation, TargetPath: meta.TargetPath,
		Status: meta.Status, Author: meta.Author, CreatedAt: meta.CreatedAt,
	}
	if candidate, err := os.ReadFile(filepath.Join(proposalsDir, id, "candidate.md")); err == nil {
		proposal.Title, proposal.Body = splitCandidate(candidate)
	}
	return &proposal, nil
}

// splitCandidate extracts the title and Markdown body from an OKF candidate. The
// format is YAML frontmatter between --- fences followed by the body; the title
// is the frontmatter's title scalar. A full YAML parser is unnecessary here:
// duplicate detection and review both need only the human-readable text.
func splitCandidate(data []byte) (title, body string) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", strings.TrimSpace(text)
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", strings.TrimSpace(text)
	}
	front := rest[:end]
	body = strings.TrimSpace(rest[end+len("\n---"):])
	for _, line := range strings.Split(front, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "title:"); ok {
			title = strings.Trim(strings.TrimSpace(value), "\"'")
			break
		}
	}
	return title, body
}

// AcceptProposal accepts a reviewed proposal, moving it into accepted knowledge.
// It runs `prowl-agent knowledge accept <id> --json`, whose help reads "Accept a
// reviewed proposal atomically". A missing bundle is reported as ErrNoBundle;
// any other failure is wrapped.
func AcceptProposal(ctx context.Context, opts *config.ProwlAgentOptions, workingDir, id string) error {
	return decideProposal(ctx, opts, workingDir, "accept", id)
}

// RejectProposal rejects a proposal without changing accepted knowledge. It runs
// `prowl-agent knowledge reject <id> --json`, whose help reads "Reject a
// proposal without changing accepted knowledge". A missing bundle is reported as
// ErrNoBundle; any other failure is wrapped.
func RejectProposal(ctx context.Context, opts *config.ProwlAgentOptions, workingDir, id string) error {
	return decideProposal(ctx, opts, workingDir, "reject", id)
}

// decideProposal runs the accept or reject engine verb after confirming a bundle
// exists, so "no bundle" is reported as ErrNoBundle rather than surfacing as an
// opaque engine error.
func decideProposal(ctx context.Context, opts *config.ProwlAgentOptions, workingDir, action, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("knowledge %s: proposal id is required", action)
	}
	if _, _, err := bundlePaths(workingDir); err != nil {
		return err // ErrNoBundle, or a real filesystem failure resolving it.
	}
	_, stderr, err := Run(ctx, opts, workingDir, "knowledge", action, id, "--json")
	if err != nil {
		if message := strings.TrimSpace(stderr); message != "" {
			return fmt.Errorf("knowledge %s: %s", action, message)
		}
		return fmt.Errorf("knowledge %s: %w", action, err)
	}
	return nil
}

// DeleteAccepted removes an accepted knowledge document from the bundle.
//
// The engine has no delete verb: `knowledge` exposes init, list, show, lint,
// propose, accept, reject, and export, none of which removes an accepted doc. So
// this deletes the canonical Markdown file directly. path is bundle-relative —
// the Path field of a KnowledgeDoc, e.g. lessons/foo.md. The derived SQLite
// metadata keeps referencing the doc until the next knowledge sync reconciles it
// (an accept re-syncs the store), which is safe because retrieval reads the
// canonical files, and a listing parses them from disk.
//
// A path that escapes the knowledge directory is refused, so a caller can never
// delete outside the bundle. A missing bundle is reported as ErrNoBundle.
func DeleteAccepted(ctx context.Context, opts *config.ProwlAgentOptions, workingDir, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, knowledgeDir, err := bundlePaths(workingDir)
	if err != nil {
		return err
	}
	rel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
	if rel == "" || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to delete %q: path escapes the knowledge bundle", path)
	}
	target := filepath.Join(knowledgeDir, rel)
	// Defence in depth: confirm the joined path is still inside the bundle
	// before touching the filesystem.
	within, relErr := filepath.Rel(knowledgeDir, target)
	if relErr != nil || within == ".." ||
		strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to delete %q: path escapes the knowledge bundle", path)
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("delete accepted knowledge %q: %w", path, err)
	}
	return nil
}

// ExistingLesson is a recorded lesson a new one could duplicate — an accepted
// knowledge document or a pending proposal — reduced to the human-readable text
// a duplicate would repeat.
type ExistingLesson struct {
	Title string
	Body  string
}

// ExistingLessons gathers every recorded lesson a new one could duplicate:
// accepted knowledge documents and still-pending proposals. It is the corpus
// LessonMatchesExisting checks against. A project with no bundle simply has no
// prior lessons, so an absent bundle collapses to an empty corpus rather than an
// error — a first lesson is never a duplicate.
func ExistingLessons(ctx context.Context, opts *config.ProwlAgentOptions, workingDir string) ([]ExistingLesson, error) {
	var lessons []ExistingLesson

	docs, err := ListKnowledge(ctx, opts, workingDir)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		lessons = append(lessons, ExistingLesson{
			Title: doc.Title,
			Body:  showKnowledgeBody(ctx, opts, workingDir, doc.Path),
		})
	}

	proposals, err := ListProposals(ctx, opts, workingDir)
	if err != nil && !errors.Is(err, ErrNoBundle) {
		return nil, err
	}
	for _, proposal := range proposals {
		lessons = append(lessons, ExistingLesson{Title: proposal.Title, Body: proposal.Body})
	}
	return lessons, nil
}

// showKnowledgeBody returns the Markdown body of one accepted knowledge document
// via `knowledge show <path> --json`. It is used only for duplicate detection,
// so a document whose body cannot be read contributes its title alone rather
// than failing the whole check.
func showKnowledgeBody(ctx context.Context, opts *config.ProwlAgentOptions, workingDir, path string) string {
	out, _, err := Run(ctx, opts, workingDir, "knowledge", "show", path, "--json")
	if err != nil {
		return ""
	}
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		return ""
	}
	return payload.Body
}

// nearDuplicateThreshold is the token-overlap ratio (Jaccard) above which two
// normalised lessons are treated as the same insight reworded.
const nearDuplicateThreshold = 0.8

// LessonMatchesExisting reports whether a proposed lesson (its title and body)
// is a near-duplicate of any already-recorded lesson. Comparison is normalised —
// lowercased, punctuation stripped, whitespace collapsed — so re-punctuating or
// re-casing a lesson cannot slip a duplicate past the check. A near-duplicate on
// either the title or the body counts: automatic recording must not file the
// same insight twice.
func LessonMatchesExisting(title, body string, existing []ExistingLesson) bool {
	proposed := []string{title, body}
	for _, prior := range existing {
		for _, fresh := range proposed {
			if nearDuplicate(fresh, prior.Title) || nearDuplicate(fresh, prior.Body) {
				return true
			}
		}
	}
	return false
}

// nearDuplicate reports whether two texts express the same lesson after
// normalisation. They match when their normalised forms are equal, when one
// fully embeds the other (a restatement that only adds context), or when their
// word sets overlap past nearDuplicateThreshold.
func nearDuplicate(a, b string) bool {
	na, nb := normalizeLesson(a), normalizeLesson(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	shorter, longer := na, nb
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	// A restatement that embeds a prior lesson verbatim is a duplicate; the
	// length floor keeps a short shared phrase from matching by accident.
	if len(shorter) >= 24 && strings.Contains(longer, shorter) {
		return true
	}
	return jaccard(wordSet(na), wordSet(nb)) >= nearDuplicateThreshold
}

// normalizeLesson lowercases text, drops punctuation, and collapses runs of
// whitespace to single spaces so cosmetic edits do not defeat comparison.
func normalizeLesson(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
			continue
		}
		pendingSpace = true
	}
	return b.String()
}

// wordSet is the set of space-separated words in an already-normalised string.
func wordSet(s string) map[string]struct{} {
	fields := strings.Fields(s)
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

// jaccard is the intersection-over-union of two word sets, 0 when either is
// empty.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for word := range a {
		if _, ok := b[word]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
