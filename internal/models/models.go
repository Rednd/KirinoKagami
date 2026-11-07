// Package models contains the beatmap types served by the API. The JSON
// layout of Set and Beatmap matches cheesegull exactly so existing clients
// (bancho.py, gulag, Ripple forks, ...) work unchanged.
package models

import "time"

// Ranked status values (osu! API v1 "approved" numbering, used by cheesegull).
const (
	StatusGraveyard = -2
	StatusWIP       = -1
	StatusPending   = 0
	StatusRanked    = 1
	StatusApproved  = 2
	StatusQualified = 3
	StatusLoved     = 4
)

// Set is a beatmapset in cheesegull format.
type Set struct {
	ID               int       `json:"SetID"`
	ChildrenBeatmaps []Beatmap `json:"ChildrenBeatmaps"`
	RankedStatus     int       `json:"RankedStatus"`
	ApprovedDate     time.Time `json:"ApprovedDate"`
	LastUpdate       time.Time `json:"LastUpdate"`
	LastChecked      time.Time `json:"LastChecked"`
	Artist           string    `json:"Artist"`
	Title            string    `json:"Title"`
	Creator          string    `json:"Creator"`
	Source           string    `json:"Source"`
	Tags             string    `json:"Tags"`
	HasVideo         bool      `json:"HasVideo"`
	Genre            int       `json:"Genre"`
	Language         int       `json:"Language"`
	Favourites       int       `json:"Favourites"`

	// Internal fields, not part of the cheesegull API.
	ArtistUnicode string `json:"-"`
	TitleUnicode  string `json:"-"`
	CreatorID     int    `json:"-"`
	HasStoryboard bool   `json:"-"`
	// GenreKnown is false when the source data did not include genre and
	// language; the stored values are then left untouched on upsert.
	GenreKnown bool `json:"-"`
}

// Beatmap is a single difficulty in cheesegull format.
type Beatmap struct {
	ID               int     `json:"BeatmapID"`
	ParentSetID      int     `json:"ParentSetID"`
	DiffName         string  `json:"DiffName"`
	FileMD5          string  `json:"FileMD5"`
	Mode             int     `json:"Mode"`
	BPM              float64 `json:"BPM"`
	AR               float32 `json:"AR"`
	OD               float32 `json:"OD"`
	CS               float32 `json:"CS"`
	HP               float32 `json:"HP"`
	TotalLength      int     `json:"TotalLength"`
	HitLength        int     `json:"HitLength"`
	Playcount        int     `json:"Playcount"`
	Passcount        int     `json:"Passcount"`
	MaxCombo         int     `json:"MaxCombo"`
	DifficultyRating float64 `json:"DifficultyRating"`
}

// ModeMask returns a bitmask of the modes present in the set's beatmaps.
func (s *Set) ModeMask() uint8 {
	var m uint8
	for _, b := range s.ChildrenBeatmaps {
		if b.Mode >= 0 && b.Mode < 8 {
			m |= 1 << uint(b.Mode)
		}
	}
	return m
}
