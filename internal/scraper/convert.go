package scraper

import (
	"strings"
	"time"

	"kirinokagami/internal/models"
	"kirinokagami/internal/osu"
)

// convertSet maps an API v2 beatmapset into the cheesegull model.
func convertSet(s *osu.Beatmapset) *models.Set {
	set := &models.Set{
		ID:            s.ID,
		RankedStatus:  s.Ranked,
		Artist:        s.Artist,
		ArtistUnicode: s.ArtistUnicode,
		Title:         s.Title,
		TitleUnicode:  s.TitleUnicode,
		Creator:       s.Creator,
		CreatorID:     s.UserID,
		Source:        s.Source,
		Tags:          s.Tags,
		HasVideo:      s.Video,
		HasStoryboard: s.Storyboard,
		Favourites:    s.FavouriteCount,
	}
	if s.RankedDate != nil {
		set.ApprovedDate = s.RankedDate.UTC()
	}
	switch {
	case s.LastUpdated != nil:
		set.LastUpdate = s.LastUpdated.UTC()
	case s.SubmittedDate != nil:
		set.LastUpdate = s.SubmittedDate.UTC()
	default:
		set.LastUpdate = time.Now().UTC()
	}
	if g, l, ok := s.GenreLanguage(); ok {
		set.Genre, set.Language, set.GenreKnown = g, l, true
	}
	set.ChildrenBeatmaps = make([]models.Beatmap, 0, len(s.Beatmaps))
	for i := range s.Beatmaps {
		set.ChildrenBeatmaps = append(set.ChildrenBeatmaps, convertBeatmap(&s.Beatmaps[i], s.ID))
	}
	return set
}

func convertBeatmap(b *osu.Beatmap, setID int) models.Beatmap {
	if b.BeatmapsetID != 0 {
		setID = b.BeatmapsetID
	}
	bm := models.Beatmap{
		ID:               b.ID,
		ParentSetID:      setID,
		DiffName:         b.Version,
		Mode:             b.ModeInt,
		BPM:              b.BPM,
		AR:               b.AR,
		OD:               b.Accuracy,
		CS:               b.CS,
		HP:               b.Drain,
		TotalLength:      b.TotalLength,
		HitLength:        b.HitLength,
		Playcount:        b.Playcount,
		Passcount:        b.Passcount,
		DifficultyRating: b.DifficultyRating,
	}
	if b.Checksum != nil {
		bm.FileMD5 = strings.ToLower(*b.Checksum)
	}
	if b.MaxCombo != nil {
		bm.MaxCombo = *b.MaxCombo
	}
	return bm
}
