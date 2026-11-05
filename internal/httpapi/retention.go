package httpapi

import (
	"net/http"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/retention"
	"webee_rentor/internal/retention/retentionapp"
)

type profileReviewDTO struct {
	UserID       identity.UserID     `json:"user_id"`
	Email        string              `json:"email"`
	FullName     string              `json:"full_name"`
	Phone        string              `json:"phone"`
	LastActivity time.Time           `json:"last_activity_at"`
	Blockers     []retention.Blocker `json:"blockers"`
}

// profileRetention lists customer profiles of accounts unused for the
// retention period, for an admin to review (decision D39).
func (s *Server) profileRetention(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, total, err := s.svc.Retention.InactiveProfiles(r.Context(), p, page, size)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageBody[profileReviewDTO]{Page: page, PageSize: size, Total: total,
		Items: mapped(rows, func(v retentionapp.ProfileReview) profileReviewDTO {
			return profileReviewDTO{UserID: v.UserID, Email: v.Contact.Email, FullName: v.Contact.Name, Phone: v.Contact.Phone,
				LastActivity: v.LastActivity, Blockers: nonNil(v.Blockers)}
		})})
}

func (s *Server) eraseProfile(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.svc.Retention.EraseProfile(r.Context(), p, id, reason); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
