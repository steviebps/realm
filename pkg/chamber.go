package realm

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Chamber is a Tree Node struct that contain Toggles and children Chambers
type Chamber struct {
	Name        string                         `json:"name"`
	IsBuildable bool                           `json:"isBuildable"`
	IsApp       bool                           `json:"isApp"`
	Toggles     map[string]*OverrideableToggle `json:"toggles"`
	Children    []*Chamber                     `json:"children,omitempty"`
}

type chamberAlias Chamber

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
func (c *Chamber) InheritWith(inherited map[string]*OverrideableToggle) {
	for key := range inherited {
		if _, ok := c.Toggles[key]; !ok {
			c.Toggles[key] = inherited[key]
		}
	}
}

// TraverseAndBuild will traverse all Chambers while inheriting their parent Toggles and executes a callback on each Chamber node.
// Traversing will stop if callback returns true.
func (c *Chamber) TraverseAndBuild(callback func(Chamber) bool) {

	// if callback returns true, stop traversing
	// consumer was only looking to build up to this point
	if callback(*c) {
		return
	}

	for i := range c.Children {
		c.Children[i].InheritWith(c.Toggles)
		c.Children[i].TraverseAndBuild(callback)
	}
}

// GetToggleValue returns the toggle with the specified toggleName at the specified version.
// Will return nil if the toggle does not exist
func (c *Chamber) GetToggleValue(toggleName string, version string) interface{} {
	var t *OverrideableToggle
	var ok bool

	if t, ok = c.Toggles[toggleName]; !ok {
		return nil
	}

	return t.GetValueAt(version)
}

// UnmarshalJSON Custom UnmarshalJSON method for validating Chamber
func (c *Chamber) UnmarshalJSON(b []byte) error {

	var alias chamberAlias

	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}

	*c = Chamber(alias)

	if c.Name == "" {
		return errors.New("all chambers must have a name")
	}

	if c.IsApp && len(c.Children) > 0 {
		return fmt.Errorf("%q is an app and cannot have children. Set isApp to false to allow children", c.Name)
	}

	return nil
}
