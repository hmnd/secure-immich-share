// Package share resolves a visitor's link into a share listing:
// cached per Immich share address, gated by unlock sessions for password
// shares, and evicted as soon as Immich reports the share gone. It maps
// Immich data onto its own types; nothing Immich-specific leaves it.
package share

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/immich"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/ratelimit"
)

var (
	// ErrNotFound covers invalid links and gone shares.
	ErrNotFound = errors.New("share: not found")
	// ErrUnavailable: Immich is unreachable and nothing is cached.
	ErrUnavailable = errors.New("share: upstream unavailable")
	// ErrInvalidPassword: wrong password.
	ErrInvalidPassword = errors.New("share: invalid password")
	// ErrNotLocked: unlock on a share without a password.
	ErrNotLocked = errors.New("share: not password protected")
)

// RateLimitedError: the unlock throttle refused the attempt.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e RateLimitedError) Error() string {
	return fmt.Sprintf("share: rate limited for %s", e.RetryAfter)
}

// Kind is the share kind.
type Kind string

const (
	KindAlbum      Kind = "album"
	KindIndividual Kind = "individual"
)

// ItemKind is what an item shows.
type ItemKind string

const (
	ItemImage ItemKind = "image"
	ItemVideo ItemKind = "video"
	ItemLive  ItemKind = "live"
)

// Item is one servable asset of a share.
type Item struct {
	ID        string
	Kind      ItemKind
	Ratio     float64
	Thumbhash *string
	// TakenAt is the local date (YYYY-MM-DD), or empty when unknown.
	TakenAt string
	// LiveVideoID is the motion part of a live photo.
	LiveVideoID string
}

// Share is a resolved listing. It is shared between visitors and must not
// be modified.
type Share struct {
	ID            string
	Target        linkref.Target
	Kind          Kind
	Title         *string
	Description   *string
	ExpiresAt     *time.Time
	AllowDownload bool
	HasPassword   bool
	CoverID       *string
	Items         map[string]Item
	Order         []string
	// ListingHash changes when items are added, removed or change kind.
	ListingHash string

	// token is the Immich share token the listing was fetched with
	// (password shares).
	token string
}

// Auth is how to address Immich for this share.
func (s *Share) Auth() immich.Auth { return immich.Auth{Target: s.Target, Token: s.token} }

func (s *Share) Item(id string) (Item, bool) {
	it, ok := s.Items[id]
	return it, ok
}

// State is a visitor's access to a resolved share.
type State string

const (
	StatePublic   State = "public"
	StateLocked   State = "locked"
	StateUnlocked State = "unlocked"
)

// Result is a resolution. Share is set exactly when State is public or
// unlocked; Clear lists unlock cookies that turned stale and must be
// cleared.
type Result struct {
	State State
	Share *Share
	Clear []string
}

// Detail is the per-asset data the media pipeline needs.
type Detail struct {
	ID          string
	Format      sanitize.Format
	Checksum    string
	UpdatedAt   time.Time
	LiveVideoID string
	Description *string
	Edited      bool
}

type Options struct {
	Client   *immich.Client
	Clock    clock.Clock
	Logger   *slog.Logger
	TTL      time.Duration
	Throttle *ratelimit.Throttle
}

type Service struct {
	client   *immich.Client
	clock    clock.Clock
	log      *slog.Logger
	ttl      time.Duration
	throttle *ratelimit.Throttle
	flight   singleflight.Group

	mu      sync.Mutex
	entries *ttlCache[string, *entry] // by target ID
	ids     *ttlCache[string, string] // target ID → Immich share ID, learned from Immich
	details *ttlCache[[2]string, Detail]
}

type entry struct {
	share     *Share
	tokenHash string // sha256 of the token that fetched it (password shares)
}

const (
	detailTTL = 120 * time.Second
	idTTL     = 24 * time.Hour

	maxEntries = 1_000
	maxIDs     = 100_000
	maxDetails = 100_000
)

func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Service{
		client: o.Client, clock: o.Clock, log: o.Logger, ttl: o.TTL, throttle: o.Throttle,
		entries: newTTLCache[string, *entry](maxEntries), ids: newTTLCache[string, string](maxIDs),
		details: newTTLCache[[2]string, Detail](maxDetails),
	}
}

// Resolve returns the visitor's view of the share at target, given their
// unlock sessions.
func (s *Service) Resolve(ctx context.Context, target linkref.Target, sessions []Session) (Result, error) {
	tid := target.ID()
	now := s.clock.Now()
	s.mu.Lock()
	e, _ := s.entries.get(tid, now)
	knownID, _ := s.ids.get(tid, now)
	s.mu.Unlock()

	cands := sessions
	if knownID != "" {
		cands = slices.DeleteFunc(slices.Clone(sessions), func(se Session) bool { return se.ShareID != knownID })
	}

	if e != nil {
		if !e.share.HasPassword {
			return Result{State: StatePublic, Share: e.share}, nil
		}
		for _, se := range cands {
			if se.ShareID == e.share.ID && linkref.Hash(se.Token) == e.tokenHash {
				return Result{State: StateUnlocked, Share: e.share}, nil
			}
		}
		if len(cands) == 0 {
			return Result{State: StateLocked}, nil
		}
		// A cookie whose token differs from the cached one: either the
		// cache predates a password change, or the cookie does. Ask Immich.
	}
	return s.fetch(ctx, target, cands, knownID)
}

// flightTimeout bounds a singleflight call to Immich.
const flightTimeout = time.Minute

// flightContext detaches a singleflight call from the visitor who started
// it: waiters share its result, so one visitor leaving must not fail the
// others.
func flightContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
}

// fetch loads the listing from Immich with the candidate tokens.
func (s *Service) fetch(ctx context.Context, target linkref.Target, cands []Session, knownID string) (Result, error) {
	tid := target.ID()
	tokens := make([]string, 0, len(cands))
	for _, c := range cands {
		tokens = append(tokens, c.Token)
	}
	slices.Sort(tokens)
	key := tid + "\x00" + linkref.Hash(strings.Join(tokens, ","))
	v, err, _ := s.flight.Do(key, func() (any, error) {
		ctx, cancel := flightContext(ctx)
		defer cancel()
		return s.load(ctx, target, cands)
	})
	if err != nil {
		switch {
		case errors.Is(err, immich.ErrPasswordRequired):
			res := Result{State: StateLocked}
			if knownID != "" {
				for _, c := range cands {
					res.Clear = append(res.Clear, CookieName(c.ShareID))
				}
			}
			return res, nil
		case errors.Is(err, immich.ErrGone):
			s.evictTarget(tid)
			return Result{}, ErrNotFound
		case errors.Is(err, ErrNotFound):
			s.evictTarget(tid)
			return Result{}, ErrNotFound
		default:
			s.log.WarnContext(ctx, "share: immich unavailable", "share", tid[:8], "error", err)
			return Result{}, ErrUnavailable
		}
	}
	sh := v.(*Share)
	if !sh.HasPassword {
		return Result{State: StatePublic, Share: sh}, nil
	}
	return Result{State: StateUnlocked, Share: sh}, nil
}

// load fetches and caches a listing. With tokens, Immich accepts the
// request when any of them belongs to the share.
func (s *Service) load(ctx context.Context, target linkref.Target, cands []Session) (*Share, error) {
	tokens := make([]string, 0, len(cands))
	for _, c := range cands {
		tokens = append(tokens, c.Token)
	}
	auth := immich.Auth{Target: target, Token: strings.Join(tokens, ",")}
	link, err := s.client.GetMine(ctx, auth)
	if err != nil {
		return nil, err
	}
	s.learnID(target, link.ID)
	token := ""
	if link.Password != nil && *link.Password != "" {
		for _, c := range cands {
			if c.ShareID == link.ID {
				token = c.Token
			}
		}
		if token == "" {
			// Immich accepted a token of a different share id: never.
			return nil, immich.ErrPasswordRequired
		}
	}
	auth.Token = token
	sh, err := s.listing(ctx, link, auth)
	if err != nil {
		return nil, err
	}
	sh.token = token
	now := s.clock.Now()
	until := now.Add(s.ttl)
	if sh.ExpiresAt != nil {
		if !now.Before(*sh.ExpiresAt) {
			return nil, ErrNotFound
		}
		until = minTime(until, *sh.ExpiresAt)
	}
	s.mu.Lock()
	s.entries.put(target.ID(), &entry{share: sh, tokenHash: linkref.Hash(token)}, until, now)
	s.mu.Unlock()
	return sh, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (s *Service) learnID(target linkref.Target, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.ids.put(target.ID(), id, now.Add(idTTL), now)
}

// listing maps the share's assets onto items.
func (s *Service) listing(ctx context.Context, link immich.SharedLink, auth immich.Auth) (*Share, error) {
	sh := &Share{
		ID:            link.ID,
		Target:        auth.Target,
		ExpiresAt:     link.ExpiresAt,
		AllowDownload: link.AllowDownload,
		HasPassword:   link.Password != nil && *link.Password != "",
		Items:         map[string]Item{},
	}
	if !link.AllowDownload || !link.ShowMetadata {
		s.log.WarnContext(ctx, "share: Immich share lacks Allow download or Show metadata; media can't be stripped from originals",
			"share", linkref.Hash8(link.ID))
	}
	switch link.Type {
	case "ALBUM":
		if link.Album == nil {
			return nil, fmt.Errorf("%w: album share without album", immich.ErrUpstream)
		}
		sh.Kind = KindAlbum
		sh.Title = nonEmpty(link.Album.AlbumName)
		sh.Description = nonEmpty(link.Album.Description)
		if sh.Description == nil && link.Description != nil {
			sh.Description = nonEmpty(*link.Description)
		}
		if err := s.albumItems(ctx, link.Album.ID, auth, sh); err != nil {
			return nil, err
		}
		if link.Album.AlbumThumbnailAssetID != nil {
			if _, ok := sh.Items[*link.Album.AlbumThumbnailAssetID]; ok {
				sh.CoverID = link.Album.AlbumThumbnailAssetID
			}
		}
	case "INDIVIDUAL":
		sh.Kind = KindIndividual
		if link.Description != nil {
			sh.Title = nonEmpty(*link.Description)
		}
		now := s.clock.Now()
		for _, a := range link.Assets {
			if a.IsTrashed || !servableVisibility(a.Visibility) {
				continue
			}
			it := Item{ID: a.ID, Kind: itemKind(a.Type == "IMAGE", a.LivePhotoVideoID), Ratio: ratio(a.Width, a.Height),
				Thumbhash: a.Thumbhash, TakenAt: localDate(a.LocalDateTime)}
			if it.Kind == "" {
				continue
			}
			if a.LivePhotoVideoID != nil {
				it.LiveVideoID = *a.LivePhotoVideoID
			}
			sh.Items[a.ID] = it
			sh.Order = append(sh.Order, a.ID)
			s.storeDetail(sh.ID, detailOf(a), now)
		}
	default:
		return nil, fmt.Errorf("%w: unknown share type", immich.ErrUpstream)
	}
	if sh.CoverID == nil && len(sh.Order) > 0 {
		sh.CoverID = &sh.Order[0]
	}
	sh.ListingHash = listingHash(sh)
	return sh, nil
}

func (s *Service) albumItems(ctx context.Context, albumID string, auth immich.Auth, sh *Share) error {
	buckets, err := s.client.Buckets(ctx, auth, albumID)
	if err != nil {
		return err
	}
	for _, b := range buckets {
		ba, err := s.client.Bucket(ctx, auth, albumID, b.TimeBucket)
		if err != nil {
			// A partial listing is a failed listing.
			return err
		}
		for i := range ba.Len() {
			if ba.IsTrashed[i] || !servableVisibility(ba.Visibility[i]) {
				continue
			}
			id := ba.ID[i]
			if _, dup := sh.Items[id]; dup {
				continue
			}
			kind := itemKind(ba.IsImage[i], ba.LivePhotoVideoID[i])
			it := Item{ID: id, Kind: kind, Ratio: sane(ba.Ratio[i]), Thumbhash: ba.Thumbhash[i],
				TakenAt: offsetDate(ba.FileCreatedAt[i], ba.LocalOffsetHours[i])}
			if ba.LivePhotoVideoID[i] != nil {
				it.LiveVideoID = *ba.LivePhotoVideoID[i]
			}
			sh.Items[id] = it
			sh.Order = append(sh.Order, id)
		}
	}
	return nil
}

// servableVisibility drops locked-folder and hidden (live photo motion
// parts) assets.
func servableVisibility(v string) bool {
	switch strings.ToLower(v) {
	case "", "timeline", "archive":
		return true
	}
	return false
}

func itemKind(isImage bool, live *string) ItemKind {
	switch {
	case isImage && live != nil && *live != "":
		return ItemLive
	case isImage:
		return ItemImage
	default:
		return ItemVideo
	}
}

func ratio(w, h *int) float64 {
	if w == nil || h == nil || *w <= 0 || *h <= 0 {
		return 1
	}
	return sane(float64(*w) / float64(*h))
}

// sane replaces ratios that aren't positive and finite with 1; display
// clamps them further.
func sane(r float64) float64 {
	if math.IsNaN(r) || math.IsInf(r, 0) || r <= 0 {
		return 1
	}
	return r
}

// localDate takes Immich's localDateTime (local wall time marked UTC) and
// keeps the date only.
func localDate(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

// offsetDate applies the timeline's localOffsetHours to fileCreatedAt.
func offsetDate(fileCreatedAt string, offsetHours float64) string {
	t, err := time.Parse(time.RFC3339Nano, fileCreatedAt)
	if err != nil {
		return ""
	}
	return t.UTC().Add(time.Duration(offsetHours * float64(time.Hour))).Format(time.DateOnly)
}

func nonEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func listingHash(sh *Share) string {
	h := sha256.New()
	for _, id := range sh.Order {
		it := sh.Items[id]
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\n", id, it.Kind, it.LiveVideoID)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func detailOf(a immich.Asset) Detail {
	d := Detail{
		ID:        a.ID,
		Format:    sanitize.ClassifyMime(a.OriginalMimeType, a.Type == "VIDEO"),
		Checksum:  a.Checksum,
		UpdatedAt: a.UpdatedAt,
		Edited:    a.IsEdited,
	}
	if a.LivePhotoVideoID != nil {
		d.LiveVideoID = *a.LivePhotoVideoID
	}
	if a.ExifInfo != nil && a.ExifInfo.Description != nil {
		d.Description = nonEmpty(*a.ExifInfo.Description)
	}
	return d
}

func (s *Service) storeDetail(shareID string, d Detail, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.details.put([2]string{shareID, d.ID}, d, now.Add(detailTTL), now)
}

// Detail returns an asset's detail. id must be an item of sh or the motion
// part of one.
func (s *Service) Detail(ctx context.Context, sh *Share, id string) (Detail, error) {
	if !s.servable(sh, id) {
		return Detail{}, ErrNotFound
	}
	s.mu.Lock()
	d, ok := s.details.get([2]string{sh.ID, id}, s.clock.Now())
	s.mu.Unlock()
	if ok {
		return d, nil
	}
	v, err, _ := s.flight.Do("detail\x00"+sh.ID+"\x00"+id, func() (any, error) {
		ctx, cancel := flightContext(ctx)
		defer cancel()
		a, err := s.client.Asset(ctx, sh.Auth(), id)
		if err != nil {
			return nil, err
		}
		if a.ID != id {
			return nil, fmt.Errorf("%w: asset id mismatch", immich.ErrUpstream)
		}
		d := detailOf(a)
		s.storeDetail(sh.ID, d, s.clock.Now())
		return d, nil
	})
	if err != nil {
		if errors.Is(err, immich.ErrGone) {
			s.Evict(sh.ID)
			return Detail{}, ErrNotFound
		}
		return Detail{}, ErrUnavailable
	}
	return v.(Detail), nil
}

func (s *Service) servable(sh *Share, id string) bool {
	if _, ok := sh.Items[id]; ok {
		return true
	}
	for _, it := range sh.Items {
		if it.LiveVideoID == id {
			return true
		}
	}
	return false
}

// Evict drops every cached listing of an Immich share.
func (s *Service) Evict(shareID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries.deleteFunc(func(_ string, e *entry) bool { return e.share.ID == shareID })
	s.details.deleteFunc(func(k [2]string, _ Detail) bool { return k[0] == shareID })
}

func (s *Service) evictTarget(tid string) {
	s.mu.Lock()
	s.entries.delete(tid)
	id, _ := s.ids.get(tid, s.clock.Now())
	s.mu.Unlock()
	if id != "" {
		s.Evict(id)
	}
}

// throttleKeys identify the share for the unlock throttle: its address (a
// token decrypts to the same address as its key or slug link) and, once
// known, its Immich id. Both are checked, so learning the id never resets a
// throttle.
func (s *Service) throttleKeys(target linkref.Target) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := []string{"target:" + target.ID()}
	if id, _ := s.ids.get(target.ID(), s.clock.Now()); id != "" {
		keys = append(keys, "id:"+id)
	}
	return keys
}

// Unlock checks a password with Immich and returns the session to store in
// the visitor's cookie. ip is the client address for the throttle.
func (s *Service) Unlock(ctx context.Context, target linkref.Target, password, ip string) (Session, error) {
	keys := s.throttleKeys(target)
	if ok, wait := s.throttle.Allow(ip, keys...); !ok {
		return Session{}, RateLimitedError{RetryAfter: wait}
	}
	token, err := s.client.Login(ctx, target, password)
	switch {
	case errors.Is(err, immich.ErrInvalidPassword):
		s.throttle.Fail(ip, keys...)
		return Session{}, ErrInvalidPassword
	case errors.Is(err, immich.ErrGone):
		s.evictTarget(target.ID())
		return Session{}, ErrNotFound
	case errors.Is(err, immich.ErrNotPasswordProtected):
		return Session{}, ErrNotLocked
	case err != nil:
		return Session{}, ErrUnavailable
	}
	s.throttle.Succeed(ip, keys...)
	link, err := s.client.GetMine(ctx, immich.Auth{Target: target, Token: token})
	if err != nil {
		if errors.Is(err, immich.ErrGone) {
			return Session{}, ErrNotFound
		}
		return Session{}, ErrUnavailable
	}
	s.learnID(target, link.ID)
	exp := s.clock.Now().Add(SessionTTL)
	if link.ExpiresAt != nil {
		exp = minTime(exp, *link.ExpiresAt)
	}
	return Session{ShareID: link.ID, Token: token, Exp: exp}, nil
}
