package httpapi

import (
	"net/http"

	"webee_rentor/internal/identity"
)

// mediaPath is where stored images are served. Catalog records keep this site
// path, so images do not depend on the host name.
const mediaPath = "/api/v1/media/"

func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	if err := s.limit(r, "image-user", string(p.UserID), s.cfg.RateLimits.ImageUploadPerUser); err != nil {
		writeError(w, r, err)
		return
	}
	img, err := s.svc.Media.UploadImage(r.Context(), p, r.Body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": mediaPath + img.Name, "content_type": img.ContentType, "size": img.Size})
}

func (s *Server) mediaFile(w http.ResponseWriter, r *http.Request) {
	f, format, err := s.svc.Media.Open(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", format.ContentType)
	h.Set("Cache-Control", "public, max-age=31536000, immutable") // names are random and files never change
	http.ServeContent(w, r, "", f.ModTime, f)
}
