package models

import (
	"encoding/json"
	"testing"
	"time"
)

// The JSON keys must match cheesegull exactly.
func TestCheesegullJSONShape(t *testing.T) {
	s := Set{ID: 1, LastUpdate: time.Unix(0, 0).UTC(), ChildrenBeatmaps: []Beatmap{{ID: 2, ParentSetID: 1}}, ArtistUnicode: "hidden"}
	b, _ := json.Marshal(s)
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"SetID", "ChildrenBeatmaps", "RankedStatus", "ApprovedDate", "LastUpdate", "LastChecked",
		"Artist", "Title", "Creator", "Source", "Tags", "HasVideo", "Genre", "Language", "Favourites"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing set key %q", k)
		}
	}
	if len(m) != 15 {
		t.Errorf("set has %d keys, want 15: %s", len(m), b)
	}
	bm := m["ChildrenBeatmaps"].([]any)[0].(map[string]any)
	for _, k := range []string{"BeatmapID", "ParentSetID", "DiffName", "FileMD5", "Mode", "BPM", "AR", "OD", "CS", "HP",
		"TotalLength", "HitLength", "Playcount", "Passcount", "MaxCombo", "DifficultyRating"} {
		if _, ok := bm[k]; !ok {
			t.Errorf("missing beatmap key %q", k)
		}
	}
}
