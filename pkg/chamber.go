package rein

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Chamber is a Tree Node struct that contain Toggles and children Chambers
type Chamber struct {
	Name        string             `json:"name"`
	IsBuildable bool               `json:"isBuildable"`
	IsApp       bool               `json:"isApp"`
	Toggles     map[string]*Toggle `json:"toggles"`
	Children    []*Chamber         `json:"children"`
}

// EncodeWith takes a writer and encodes JSON to that writer
func (c *Chamber) EncodeWith(w io.Writer, pretty bool) error {
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}

	if err := enc.Encode(c); err != nil {
		return err
	}

	return nil
}

// FindByName will return the first child or nth-grandchild with the matching name. BFS.
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

// InheritWith will take a map of toggles to inherit from
// so that any toggles that do not exist in this chamber will be written to the map
func (c *Chamber) InheritWith(inherited map[string]*Toggle) {
	for key := range inherited {
		if _, ok := c.Toggles[key]; !ok {
			c.Toggles[key] = inherited[key]
		}
	}
}

// TraverseAndBuild will traverse all children Chambers and trickle down all Toggles with a callback
func (c *Chamber) TraverseAndBuild(callback func(*Chamber)) {

	callback(c)

	for i := range c.Children {
		c.Children[i].InheritWith(c.Toggles)
		c.Children[i].TraverseAndBuild(callback)
	}
}

// UnmarshalJSON Custom UnmarshalJSON method for validating Chamber
func (c *Chamber) UnmarshalJSON(b []byte) error {

	var alias chamberAlias

	err := json.Unmarshal(b, &alias)
	if err != nil {
		return err
	}

	*c = alias.toOverride()

	if c.Name == "" {
		return errors.New("Chambers must have a name")
	}

	if c.IsApp && len(c.Children) > 0 {
		return fmt.Errorf("%q is an app and cannot have children. Set isApp to false to allow children", c.Name)
	}

	return nil
}

type chamberAlias Chamber

func (c chamberAlias) toOverride() Chamber {
	return Chamber{
		Name:        c.Name,
		IsBuildable: c.IsBuildable,
		IsApp:       c.IsApp,
		Toggles:     c.Toggles,
		Children:    c.Children,
	}
}
