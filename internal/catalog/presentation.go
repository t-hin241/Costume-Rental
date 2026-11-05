package catalog

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"webee_rentor/internal/domainerr"
)

// CategoryID identifies a catalog category.
type CategoryID string

const (
	maxCategories  = 10
	maxDescription = 4000
	maxImages      = 10
	maxImageURL    = 2000
	maxImageAlt    = 200
)

// Image is the address of a picture stored outside the system, with the text
// that describes it.
type Image struct {
	URL string
	Alt string
}

// Validate accepts https addresses and site-relative paths only, so an image
// can never carry a script or point at a plain-http page.
func (i Image) Validate() error {
	switch {
	case !strings.HasPrefix(i.URL, "https://") && !(strings.HasPrefix(i.URL, "/") && !strings.HasPrefix(i.URL, "//")):
		return domainerr.Invalid("images", "must be an https address or a site path")
	case len(i.URL) > maxImageURL:
		return domainerr.Invalid("images", fmt.Sprintf("address too long, at most %d bytes", maxImageURL))
	case utf8.RuneCountInString(i.Alt) > maxImageAlt:
		return domainerr.Invalid("images", fmt.Sprintf("alt text at most %d characters", maxImageAlt))
	}
	return nil
}

// Category groups offerings for browsing. It never affects price or stock.
type Category struct {
	ID       CategoryID
	Slug     string
	Name     string
	Image    *Image
	Position int
}

// Validate checks a category.
func (c Category) Validate() error {
	switch {
	case c.ID == "":
		return domainerr.Invalid("id", "required")
	case len(c.Slug) > maxSlugLength || !slugPattern.MatchString(c.Slug):
		return domainerr.Invalid("slug", "must be lowercase letters, digits and single hyphens")
	case strings.TrimSpace(c.Name) == "" || utf8.RuneCountInString(c.Name) > 120:
		return domainerr.Invalid("name", "required, at most 120 characters")
	}
	if c.Image != nil {
		return c.Image.Validate()
	}
	return nil
}

func validatePresentation(categories []CategoryID, description string, images []Image) error {
	if len(categories) > maxCategories {
		return domainerr.Invalid("category_ids", fmt.Sprintf("at most %d categories", maxCategories))
	}
	seen := make(map[CategoryID]bool, len(categories))
	for _, c := range categories {
		if c == "" || seen[c] {
			return domainerr.Invalid("category_ids", "categories must be present and unique")
		}
		seen[c] = true
	}
	if utf8.RuneCountInString(description) > maxDescription {
		return domainerr.Invalid("description", fmt.Sprintf("at most %d characters", maxDescription))
	}
	if len(images) > maxImages {
		return domainerr.Invalid("images", fmt.Sprintf("at most %d images", maxImages))
	}
	for _, i := range images {
		if err := i.Validate(); err != nil {
			return err
		}
	}
	return nil
}
