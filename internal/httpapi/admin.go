package httpapi

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

const (
	maxIDLength     = 64
	maxReasonLength = 500
)

func pathUserID(r *http.Request) (identity.UserID, error) {
	id := r.PathValue("id")
	if id == "" || len(id) > maxIDLength {
		return "", domainerr.Invalid("id", "must be a user id")
	}
	return identity.UserID(id), nil
}

func pathRole(r *http.Request) (identity.Role, error) {
	role := identity.Role(r.PathValue("role"))
	if !role.Valid() {
		return "", domainerr.Invalid("role", "unknown role")
	}
	return role, nil
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func decodeReason(w http.ResponseWriter, r *http.Request) (string, error) {
	var req reasonRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Reason) == "" || utf8.RuneCountInString(req.Reason) > maxReasonLength {
		return "", domainerr.Invalid("reason", "required, at most 500 characters")
	}
	return req.Reason, nil
}

type userResponse struct {
	ID        identity.UserID     `json:"id"`
	Email     string              `json:"email"`
	Status    identity.UserStatus `json:"status"`
	Roles     []identity.Role     `json:"roles"`
	CreatedAt time.Time           `json:"created_at"`
}

func (s *Server) adminUser(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	u, err := s.identity.User(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	roles := u.Roles
	if roles == nil {
		roles = []identity.Role{}
	}
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, Status: u.Status, Roles: roles, CreatedAt: u.CreatedAt})
}

func (s *Server) grantRole(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.roleChange(w, r, p, true)
}

func (s *Server) revokeRole(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.roleChange(w, r, p, false)
}

func (s *Server) roleChange(w http.ResponseWriter, r *http.Request, p identity.Principal, grant bool) {
	id, err := pathUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	role, err := pathRole(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if grant {
		err = s.identity.GrantRole(r.Context(), p, id, role, reason)
	} else {
		err = s.identity.RevokeRole(r.Context(), p, id, role, reason)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) lockUser(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.statusChange(w, r, p, true)
}

func (s *Server) unlockUser(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.statusChange(w, r, p, false)
}

func (s *Server) statusChange(w http.ResponseWriter, r *http.Request, p identity.Principal, lock bool) {
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
	if lock {
		err = s.identity.LockUser(r.Context(), p, id, reason)
	} else {
		err = s.identity.UnlockUser(r.Context(), p, id, reason)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type matrixResponse struct {
	Version string                    `json:"version"`
	Matrix  identity.PermissionMatrix `json:"matrix"`
}

func (s *Server) permissionMatrix(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	m, version, err := s.identity.PermissionMatrix(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixResponse{Version: version, Matrix: m.Normalized()})
}

type publishMatrixRequest struct {
	Version string                    `json:"version"`
	Matrix  identity.PermissionMatrix `json:"matrix"`
	Reason  string                    `json:"reason"`
}

func (s *Server) publishPermissionMatrix(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	var req publishMatrixRequest
	if err := decodeJSON(w, r, &req, largeBody); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Version) > maxIDLength {
		writeError(w, r, domainerr.Invalid("version", "at most 64 characters"))
		return
	}
	if utf8.RuneCountInString(req.Reason) > maxReasonLength {
		writeError(w, r, domainerr.Invalid("reason", "at most 500 characters"))
		return
	}
	if err := s.identity.PublishMatrix(r.Context(), p, req.Version, req.Matrix, req.Reason); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, matrixResponse{Version: req.Version, Matrix: req.Matrix.Normalized()})
}

func (s *Server) resetMFA(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
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
	if err := s.identity.ResetMFA(r.Context(), p, id, reason); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
