package feed

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

// Agents tag what they save: explore for content found on the internet and
// generated for content they wrote. normalizeContent rejects an article with
// both and applies the generated-article rules; IngestDiscoveryArticle adds
// explore and rejects generated.
const (
	TagExplore   = "explore"
	TagGenerated = "generated"

	MaxArticleTags = 10
	MaxTagBytes    = 32
)

// These are ErrInvalid narrowed to one rule, so SafeError can explain it.
var (
	errInvalidTags      = fmt.Errorf("%w: tags", ErrInvalid)
	errInvalidGenerated = fmt.Errorf("%w: generated article", ErrInvalid)
)

// tagInput accepts a slug in either case. Matching ASCII before lowercasing
// keeps Unicode case mapping (such as the Kelvin sign to k) out of tags.
var tagInput = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)

// NormalizeTags trims and lowercases each tag, which must then be a slug of
// letters and digits joined by single hyphens, at most MaxTagBytes long.
// Anything else is rejected, never repaired. The result holds at most
// MaxArticleTags distinct tags in sorted order, so an equal set always has the
// same retry hash and cursor, and is nil when there are none.
func NormalizeTags(input []string) ([]string, error) {
	var tags []string
	for _, raw := range input {
		tag := strings.TrimSpace(raw)
		if len(tag) > MaxTagBytes || !tagInput.MatchString(tag) {
			return nil, errInvalidTags
		}
		tags = append(tags, strings.ToLower(tag))
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	if len(tags) > MaxArticleTags {
		return nil, errInvalidTags
	}
	return tags, nil
}

// tagFilterHash identifies a normalized tag filter inside a list cursor. An
// unfiltered list has no hash, so its cursors keep their earlier encoding.
func tagFilterHash(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	// Slugs contain no commas, so the joined form is unambiguous.
	hash := sha256.Sum256([]byte(strings.Join(tags, ",")))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// ListTagsInput selects a shelf as ListInput does: active articles by default,
// Archive with Archived or Trash with Trashed, never both.
type ListTagsInput struct {
	Archived bool `json:"archived,omitzero"`
	Trashed  bool `json:"trashed,omitzero"`
}

// TagList holds every tag on one shelf with how many of its articles have it,
// most used first and then alphabetically.
type TagList struct {
	Tags []sqlite.TagCount `json:"tags"`
}

// ListTags shows the tags already in use, so agents can reuse them instead of
// inventing synonyms and the native app can offer them as list filters.
func (s *Service) ListTags(ctx context.Context, p identity.Principal, in ListTagsInput) (TagList, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return TagList{}, err
	}
	if in.Archived && in.Trashed {
		return TagList{}, ErrInvalid
	}
	tags, err := s.store.TagCounts(ctx, p.Owner(), in.Archived, in.Trashed)
	if err != nil {
		return TagList{}, err
	}
	return TagList{Tags: tags}, nil
}
