package rein

import (
	"encoding/json"
	"io"
	"log"
)

type Chamber struct {
	Name      string    `json:"name"`
	Buildable bool      `json:"isBuildable"`
	App       bool      `json:"isApp"`
	Toggles   []Toggle  `json:"toggles"`
	Children  []Chamber `json:"children"`
}

func (c *Chamber) Print(w io.Writer, pretty bool) {
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}

	if err := enc.Encode(c); err != nil {
		log.Println(err)
	}
}
