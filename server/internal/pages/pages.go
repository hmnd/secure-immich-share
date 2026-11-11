// Package pages renders the visitor-facing HTML with templ. Every page works
// without JavaScript; the web build's islands (web/src/islands.ts) add the
// lightbox, downloads and the measured grid layout on top.
package pages

import (
	"slices"
	"strconv"

	"github.com/hmnd/secure-immich-share/server/internal/httpapi/contract"
)

// Head is a page's <title> and Open Graph tags.
type Head struct {
	Title string
	// OGImage is an absolute URL, or empty.
	OGImage string
}

const DefaultTitle = "Shared photos"

// Share is a gallery page.
type Share struct {
	Path string
	// Kind and Ref identify the link for the islands' API calls.
	Kind string
	Ref  string
	// MediaBase is /media/{kind}/{ref}.
	MediaBase string
	View      contract.ShareView
	Photo     *Photo
}

// Photo is the photo shown over the gallery.
type Photo struct {
	Index       int
	Description *string
}

// Locked is a password form; Message explains a failed attempt.
type Locked struct {
	Message string
}

type Message struct {
	Title  string
	Detail string
	Retry  string
}

// islandData is what the islands read from the page.
type islandData struct {
	Kind  string             `json:"kind"`
	Ref   string             `json:"ref"`
	Share contract.ShareView `json:"share"`
	Photo *int               `json:"photo"`
}

func (s Share) islandData() islandData {
	d := islandData{Kind: s.Kind, Ref: s.Ref, Share: s.View}
	if s.Photo != nil {
		d.Photo = &s.Photo.Index
	}
	return d
}

func (s Share) photoURL(id string) string { return s.Path + "?photo=" + id }

func (s Share) mediaURL(it contract.ShareItem, variant contract.MediaVariant) string {
	if !slices.Contains(it.Variants, variant) {
		return ""
	}
	return s.MediaBase + "/" + it.ID + "/" + string(variant)
}

func (s Share) title() string {
	if s.View.Title != nil && *s.View.Title != "" {
		return *s.View.Title
	}
	return DefaultTitle
}

func counter(i, n int) string { return strconv.Itoa(i+1) + " / " + strconv.Itoa(n) }
