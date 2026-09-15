package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/neur0map/prowl/internal/gateway"
)

// Enrolling a Prowl login into the pool.
//
// A subscription or an api key configured in Prowl is spendable capacity the
// router could not previously reach — the pool only knew about keys pasted
// into the dashboard. Enrolling adds a pool row that REFERENCES the login
// instead of copying its secret, so refreshes are picked up and signing out
// revokes the pool's access too.

func (s *Server) registerLoginRoutes() {
	s.mux.HandleFunc("GET /api/logins", s.RequireSession(s.handleLoginsList))
	s.mux.HandleFunc("POST /api/logins/{id}/enroll", s.RequireSession(s.handleLoginEnroll))
	s.mux.HandleFunc("DELETE /api/logins/{id}/enroll", s.RequireSession(s.handleLoginWithdraw))
}

func (s *Server) handleLoginsList(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	src := s.engine.CredentialSource()
	if src == nil {
		// Not an error: a gateway running without the harness simply has no
		// logins to borrow.
		WriteJSON(w, http.StatusOK, map[string]any{"logins": []any{}})
		return
	}

	enrolled, err := s.enrolledLogins(r.Context())
	if err != nil {
		WriteBareError(w, http.StatusInternalServerError, err.Error())
		return
	}

	logins := src.Linkable(r.Context())
	out := make([]map[string]any, 0, len(logins))
	for _, login := range logins {
		keyID, isEnrolled := enrolled[strings.ToLower(login.ID)]
		row := map[string]any{
			"id": login.ID, "name": login.Name, "kind": login.Kind,
			"detail": login.Detail, "enrolled": isEnrolled,
			// Offered is what enrolling would contribute; models is what it
			// already has. Showing both is what makes the action legible.
			"offered": len(src.Models(r.Context(), login.ID)),
			"models":  0,
		}
		if isEnrolled {
			row["keyId"] = keyID
			row["models"] = s.engine.LoginModelCount(r.Context(), keyID)
		}
		out = append(out, row)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"logins": out})
}

// loginKeyLabel is the provider's own display name, falling back to its id.
func loginKeyLabel(src gateway.CredentialSource, ctx context.Context, id string) string {
	for _, p := range src.Linkable(ctx) {
		if strings.EqualFold(p.ID, id) && strings.TrimSpace(p.Name) != "" {
			return p.Name
		}
	}
	return id
}

func (s *Server) handleLoginEnroll(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if id == "" {
		WriteBareError(w, http.StatusBadRequest, "a provider id is required")
		return
	}
	src := s.engine.CredentialSource()
	if src == nil {
		WriteBareError(w, http.StatusConflict,
			"this gateway is running on its own, so it has no Prowl logins to borrow")
		return
	}

	// Enrolling something Prowl cannot actually serve would add a pool member
	// that fails on its first request.
	secret, known, err := src.Credential(r.Context(), id)
	switch {
	case err != nil:
		WriteBareError(w, http.StatusBadGateway, err.Error())
		return
	case !known:
		WriteBareError(w, http.StatusNotFound,
			"Prowl has no login for "+id+"; sign in first, then enroll it")
		return
	case secret == "":
		WriteBareError(w, http.StatusConflict,
			"the "+id+" login holds no credential yet; sign in again and retry")
		return
	}

	enrolled, err := s.enrolledLogins(r.Context())
	if err != nil {
		WriteBareError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keyID, ok := enrolled[id]; ok {
		// Enrolling again converges rather than no-opping: a login enrolled
		// before models were seeded, or one whose catalogue has since grown,
		// otherwise stays permanently without anything to route to.
		seeded, err := s.engine.SeedLoginModels(r.Context(), keyID, id, src.Models(r.Context(), id))
		if err != nil {
			slog.Warn("Could not refresh an enrolled login's models",
				"provider", id, "error", err)
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"enrolled": true, "keyId": keyID, "models": seeded,
		})
		return
	}

	// The label names the provider, because it is what every surface shows
	// next to the model: "Prowl login" told the operator nothing about who
	// actually serves a request or which subscription it spends.
	keyID, err := s.engine.Vault().AddLinked(r.Context(), id, loginKeyLabel(src, r.Context(), id))
	if err != nil {
		WriteBareError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The credential alone is inert: without models the router has nothing to
	// send to it and the Models page shows no change, which is what made
	// enrolling look like it did nothing.
	seeded, err := s.engine.SeedLoginModels(r.Context(), keyID, id, src.Models(r.Context(), id))
	if err != nil {
		slog.Warn("Enrolled a login but could not seed its models",
			"provider", id, "error", err)
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"enrolled": true, "keyId": keyID, "models": seeded,
	})
}

func (s *Server) handleLoginWithdraw(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	enrolled, err := s.enrolledLogins(r.Context())
	if err != nil {
		WriteBareError(w, http.StatusInternalServerError, err.Error())
		return
	}
	keyID, ok := enrolled[id]
	if !ok {
		WriteBareError(w, http.StatusNotFound, id+" is not in the pool")
		return
	}
	if err := s.engine.Vault().Delete(r.Context(), keyID); err != nil {
		WriteBareError(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"enrolled": false})
}

// enrolledLogins maps a provider id to the pool row that links to it.
func (s *Server) enrolledLogins(ctx context.Context) (map[string]int64, error) {
	rows, err := s.engine.DB().QueryContext(ctx,
		"SELECT id, encrypted_key FROM api_keys")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int64{}
	for rows.Next() {
		var (
			id     int64
			stored string
		)
		if err := rows.Scan(&id, &stored); err != nil {
			return nil, err
		}
		if ref, linked := gateway.LinkedRef(stored); linked {
			out[ref] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
