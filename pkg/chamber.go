package rein

import (
	"encoding/json"
	"fmt"
	"io"
)

type Chamber struct {
	Name      string     `json:"name"`
	Buildable bool       `json:"isBuildable"`
	App       bool       `json:"isApp"`
	Toggles   []*Toggle  `json:"toggles"`
	Children  []*Chamber `json:"children"`
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
	queue := make([]*Chamber, 0)
	queue = append(queue, c)

	for len(queue) > 0 {
		nextUp := queue[0]
		queue = queue[1:]

		if nextUp.Name == name {
			return nextUp
		}

		if len(nextUp.Children) > 0 {
			for i := range nextUp.Children {
				queue = append(queue, nextUp.Children[i])
			}
		}
	}
	return nil
}
