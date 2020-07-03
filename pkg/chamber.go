package rein

import (
	"encoding/json"
	"fmt"
	"io"
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
		fmt.Printf("Encoding error: %v\n", err)
	}
}

func (c *Chamber) FindByName(name string) *Chamber {
	if c.Name == name {
		return c
	}

	if len(c.Children) > 0 {
		for _, child := range c.Children {
			found := child.FindByName(name)
			if found != nil {
				return found
			}
		}
	}

	return nil
}
