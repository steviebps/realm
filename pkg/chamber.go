package rein

type Chamber struct {
	Name     string    `json:"name"`
	Toggles  []Toggle  `json:"toggles"`
	Children []Chamber `json:"children"`
}
