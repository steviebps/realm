package cmd

import (
	"github.com/steviebps/realm/utils"
)

type RealmConfig struct {
	Server ServerConfig `json:"server,omitempty"`
}

func parseConfig[T any](path string) (*T, error) {
	file, err := utils.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config T
	if err := utils.ReadInterfaceWith(file, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
